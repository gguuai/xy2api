package scheduling

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

func NewDispatchID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// BeginDispatch linearizes before the network send. A committed ticket is in-flight
// even if its first network byte occurs after a subsequent pause acknowledgement.
func (s *PostgresStore) BeginDispatch(ctx context.Context, r DispatchRequest) (DispatchTicket, error) {
	ticket := DispatchTicket{TicketID: r.TicketID, RequestID: r.RequestID, AccountID: r.AccountID}
	if err := s.ready(); err != nil {
		return ticket, err
	}
	if r.AccountID <= 0 || r.RequestID == "" || r.NodeID == "" {
		return ticket, ErrInvalidControl
	}
	if ticket.TicketID == "" {
		ticket.TicketID = NewDispatchID()
	}
	if ticket.TicketID == "" {
		return ticket, ErrSharedState
	}
	if r.LeaseDuration <= 0 {
		r.LeaseDuration = 2 * time.Minute
	}
	if r.LeaseDuration > time.Hour {
		return ticket, ErrInvalidControl
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ticket, err
	}
	defer func() { _ = tx.Rollback() }()
	family, err := familyFor(ctx, tx, r.AccountID)
	if err != nil {
		return ticket, err
	}
	if r.FamilyID != 0 && r.FamilyID != family {
		return ticket, ErrInvalidControl
	}
	ticket.FamilyID = family
	// Global lock order: credential-family first, logical-account second.
	if err = ensureControl(ctx, tx, ScopeFamily, family); err != nil {
		return ticket, err
	}
	fc, err := scanControl(ctx, tx, r.AccountID, ScopeFamily, family, true)
	if err != nil {
		return ticket, err
	}
	if err = ensureControl(ctx, tx, ScopeAccount, r.AccountID); err != nil {
		return ticket, err
	}
	ac, err := scanControl(ctx, tx, r.AccountID, ScopeAccount, r.AccountID, true)
	if err != nil {
		return ticket, err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM scheduling_attempts WHERE ticket_id=$1)", ticket.TicketID).Scan(&exists); err != nil {
		return ticket, err
	}
	if exists {
		return ticket, ErrAttemptIdentity
	}
	// Retired manual controls are not admission gates. Their rows remain as
	// transaction mutexes and historical ticket epochs; only the account switch
	// below decides whether a new request may start. Existing attempts are never
	// cancelled by changing that switch.
	if r.HardConcurrency > 0 {
		var active int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduling_attempts WHERE account_id=$1 AND "+admissionOccupancySQL(""), r.AccountID).Scan(&active); err != nil {
			return ticket, err
		}
		if active >= r.HardConcurrency {
			return ticket, ErrCapacity
		}
	}
	var schedulable bool
	var status string
	if err = tx.QueryRowContext(ctx, "SELECT schedulable,status FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR SHARE", r.AccountID).Scan(&schedulable, &status); err != nil {
		return ticket, err
	}
	if status != "active" || !schedulable {
		return ticket, ErrControlBlocked
	}
	ticket.AccountEpoch = ac.Epoch
	ticket.FamilyEpoch = fc.Epoch
	if r.Failure != nil && r.Failure.AccountID != r.AccountID {
		return ticket, ErrInvalidControl
	}
	if err = admitFailureDomains(ctx, tx, r.Failure); err != nil {
		return ticket, err
	}
	err = tx.QueryRowContext(ctx, "INSERT INTO scheduling_attempts(ticket_id,request_id,account_id,family_id,session_id,node_id,account_epoch,family_epoch,state,lease_until) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'dispatched',NOW()+($9 * INTERVAL '1 millisecond')) RETURNING lease_until", ticket.TicketID, r.RequestID, r.AccountID, family, r.SessionID, r.NodeID, ac.Epoch, fc.Epoch, r.LeaseDuration.Milliseconds()).Scan(&ticket.LeaseUntil)
	if err != nil {
		return ticket, err
	}
	if err = persistFailureAdmission(ctx, tx, ticket.TicketID, r.Failure); err != nil {
		return ticket, err
	}
	ticket.Failure = r.Failure
	if r.SessionID != "" {
		_, err = tx.ExecContext(ctx, "INSERT INTO scheduling_owner_sessions(account_id,family_id,session_id) VALUES($1,$2,$3) ON CONFLICT(account_id,session_id) DO UPDATE SET last_seen_at=NOW()", r.AccountID, family, r.SessionID)
		if err != nil {
			return ticket, err
		}
	}
	if err = tx.Commit(); err != nil {
		return ticket, err
	}
	return ticket, nil
}
func (s *PostgresStore) RenewAttempt(ctx context.Context, ticketID string, lease time.Duration) error {
	if err := s.ready(); err != nil {
		return err
	}
	if lease <= 0 || lease > time.Hour {
		return ErrInvalidControl
	}
	result, err := s.db.ExecContext(ctx, "UPDATE scheduling_attempts SET lease_until=NOW()+($2 * INTERVAL '1 millisecond') WHERE ticket_id=$1 AND state='dispatched' AND lease_until>NOW()", ticketID, lease.Milliseconds())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAttemptIdentity
	}
	return nil
}

// SettleAttempt requires observed terminal execution; timeout of a local lease is
// not terminal evidence. Repeating settlement never double-counts or changes it.
func (s *PostgresStore) SettleAttempt(ctx context.Context, ticketID, outcome string, usagePending bool) error {
	if err := s.ready(); err != nil {
		return err
	}
	if ticketID == "" || outcome == "" {
		return ErrInvalidControl
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var accountID, familyID int64
	// Identity is immutable. Read it without holding a ticket lock, then acquire
	// family -> account -> ticket, matching admission and force-stop lock order.
	err = tx.QueryRowContext(ctx, "SELECT account_id,family_id FROM scheduling_attempts WHERE ticket_id=$1", ticketID).Scan(&accountID, &familyID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAttemptIdentity
	}
	if err != nil {
		return err
	}
	controls := make([]ControlSnapshot, 0, 2)
	for _, k := range []struct {
		scope string
		id    int64
	}{{ScopeFamily, familyID}, {ScopeAccount, accountID}} {
		c, e := scanControl(ctx, tx, accountID, k.scope, k.id, true)
		if e != nil {
			return e
		}
		controls = append(controls, c)
	}
	var state string
	err = tx.QueryRowContext(ctx, "SELECT state FROM scheduling_attempts WHERE ticket_id=$1 FOR UPDATE", ticketID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAttemptIdentity
	}
	if err != nil {
		return err
	}
	if state == "settled" {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE scheduling_attempts SET state='settled',outcome=$2,usage_pending=($3 AND NOT usage_acknowledged),settled_at=NOW() WHERE ticket_id=$1", ticketID, outcome, usagePending); err != nil {
		return err
	}
	for i := range controls {
		if err = persistDrainState(ctx, tx, &controls[i]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RecordTerminalIntent durably records an observed terminal result before the
// settlement transaction. If settlement is interrupted, the reconciler can
// complete it without guessing from an expired lease.
func (s *PostgresStore) RecordTerminalIntent(ctx context.Context, ticketID, outcome, certainty string, usagePending bool) error {
	if err := s.ready(); err != nil {
		return err
	}
	if ticketID == "" || outcome == "" || certainty == "" || len(outcome) > 256 || len(certainty) > 64 {
		return ErrInvalidControl
	}
	result, err := s.db.ExecContext(ctx, `UPDATE scheduling_attempts
		SET metrics = metrics || jsonb_build_object(
			'terminal_intent', true,
			'terminal_outcome', $2::text,
			'terminal_certainty', $3::text,
			'terminal_usage_pending', $4::boolean)
		WHERE ticket_id=$1 AND state<>'settled'`, ticketID, outcome, certainty, usagePending)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAttemptIdentity
	}
	return nil
}

// ReconcileTerminalIntents settles tickets whose terminal intent was written
// but whose first settlement transaction failed.
func (s *PostgresStore) ReconcileTerminalIntents(ctx context.Context) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ticket_id,
		metrics->>'terminal_outcome',
		COALESCE(metrics->>'terminal_usage_pending' = 'true', false)
		FROM scheduling_attempts
		WHERE state<>'settled' AND metrics->>'terminal_intent'='true'
			AND COALESCE(NULLIF(metrics->>'terminal_outcome', ''), '') <> ''
		ORDER BY dispatched_at, ticket_id
		LIMIT 100`)
	if err != nil {
		return 0, err
	}
	type intent struct {
		ticketID     string
		outcome      string
		usagePending bool
	}
	intents := make([]intent, 0)
	for rows.Next() {
		var value intent
		if err = rows.Scan(&value.ticketID, &value.outcome, &value.usagePending); err != nil {
			_ = rows.Close()
			return 0, err
		}
		intents = append(intents, value)
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	var settled int64
	var firstErr error
	for _, value := range intents {
		if err = s.SettleAttempt(ctx, value.ticketID, value.outcome, value.usagePending); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		settled++
	}
	return settled, firstErr
}

func (s *PostgresStore) MarkAttemptUnknown(ctx context.Context, ticketID string) error {
	if err := s.ready(); err != nil {
		return err
	}
	// Called after local cancellation/termination. A short hold prevents immediate
	// reuse; the existing failure cooldown still requires a single recovery probe.
	// Idempotent retries cannot extend the hold or invent a remote terminal result.
	_, err := s.db.ExecContext(ctx, "UPDATE scheduling_attempts SET state='unknown',usage_pending=NOT usage_acknowledged,local_finished_at=NOW(),unknown_hold_until=LEAST(lease_until,NOW()+INTERVAL '30 seconds') WHERE ticket_id=$1 AND state='dispatched'", ticketID)
	return err
}
func (s *PostgresStore) ReconcileExpired(ctx context.Context) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	// A crashed holder cannot keep capacity forever. Do not start another shadow
	// period on each reconciliation, and do not claim local or remote completion.
	r, err := s.db.ExecContext(ctx, "UPDATE scheduling_attempts SET state='unknown',usage_pending=NOT usage_acknowledged,unknown_hold_until=lease_until WHERE state='dispatched' AND lease_until<=NOW()")
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}
func (s *PostgresStore) ResolveUnknown(ctx context.Context, ticketID, observedOutcome string, usagePending bool) error {
	return s.SettleAttempt(ctx, ticketID, observedOutcome, usagePending)
}
func (s *PostgresStore) AcknowledgeUsage(ctx context.Context, ticketID string) error {
	if err := s.ready(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "UPDATE scheduling_attempts SET usage_pending=FALSE,usage_acknowledged=TRUE WHERE ticket_id=$1", ticketID)
	return err
}

// ForceStopApplies prevents a delayed cancellation from killing requests admitted
// after resume. The event epoch is strictly newer than any ticket it can cancel.
func ForceStopApplies(c ControlSnapshot, t DispatchTicket) bool {
	if c.Mode != "force_stop" || c.State == ControlRunning {
		return false
	}
	if c.Scope == ScopeFamily {
		return c.SubjectID == t.FamilyID && t.FamilyEpoch < c.Epoch
	}
	return c.SubjectID == t.AccountID && t.AccountEpoch < c.Epoch
}
