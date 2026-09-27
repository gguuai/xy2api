package scheduling

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type PostgresStore struct {
	db         *sql.DB
	hookMu     sync.RWMutex
	cancelHook CancelHook
}

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }
func (s *PostgresStore) SetCancelHook(h CancelHook) {
	s.hookMu.Lock()
	s.cancelHook = h
	s.hookMu.Unlock()
}
func (s *PostgresStore) ready() error {
	if s == nil || s.db == nil {
		return ErrSharedState
	}
	return nil
}
func (s *PostgresStore) GetPolicy(ctx context.Context, groupID int64, model string) (PolicyRecord, error) {
	r := PolicyRecord{GroupID: groupID, Model: model}
	if err := s.ready(); err != nil {
		return r, err
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT version, policy FROM scheduling_policies WHERE group_id=$1 AND model=$2", groupID, model).Scan(&r.Version, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	var p Policy
	if err = json.Unmarshal(raw, &p); err != nil {
		return r, fmt.Errorf("decode scheduling policy: %w", err)
	}
	p.Version = r.Version
	r.Policy = &p
	return r, nil
}
func (s *PostgresStore) PutPolicy(ctx context.Context, p Policy, expected int64) (PolicyRecord, error) {
	r := PolicyRecord{GroupID: p.GroupID, Model: p.Model}
	if err := s.ready(); err != nil {
		return r, err
	}
	if expected < 0 || strings.TrimSpace(p.Model) == "" || p.GroupID < 0 {
		return r, ErrInvalidControl
	}
	if err := ValidatePolicy(p); err != nil {
		return r, err
	}
	p.Version = expected + 1
	raw, err := json.Marshal(p)
	if err != nil {
		return r, err
	}
	if expected == 0 {
		err = s.db.QueryRowContext(ctx, "INSERT INTO scheduling_policies(group_id,model,version,policy) VALUES($1,$2,1,$3::jsonb) ON CONFLICT(group_id,model) DO NOTHING RETURNING version", p.GroupID, p.Model, string(raw)).Scan(&r.Version)
	} else {
		err = s.db.QueryRowContext(ctx, "UPDATE scheduling_policies SET version=version+1, policy=$3::jsonb, updated_at=NOW() WHERE group_id=$1 AND model=$2 AND version=$4 RETURNING version", p.GroupID, p.Model, string(raw), expected).Scan(&r.Version)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrVersionConflict
	}
	if err != nil {
		return r, err
	}
	p.Version = r.Version
	r.Policy = &p
	return r, nil
}

type sqlQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func familyFor(ctx context.Context, q sqlQueryer, id int64) (int64, error) {
	var family int64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(parent_account_id,id) FROM accounts WHERE id=$1 AND deleted_at IS NULL", id).Scan(&family)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrControlNotFound
	}
	return family, err
}
func scopeSubject(ctx context.Context, q sqlQueryer, id int64, scope string) (int64, error) {
	if id <= 0 {
		return 0, ErrInvalidControl
	}
	family, err := familyFor(ctx, q, id)
	if err != nil {
		return 0, err
	}
	if scope == ScopeFamily {
		return family, nil
	}
	if scope == ScopeAccount {
		return id, nil
	}
	return 0, ErrInvalidControl
}
func ensureControl(ctx context.Context, tx *sql.Tx, scope string, id int64) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO scheduling_controls(scope,subject_id,state) SELECT $1,$2,CASE WHEN $1='logical_account' AND NOT schedulable THEN 'PAUSED' ELSE 'RUNNING' END FROM accounts WHERE id=$2 AND deleted_at IS NULL ON CONFLICT(scope,subject_id) DO NOTHING", scope, id)
	return err
}
func scanControl(ctx context.Context, q sqlQueryer, accountID int64, scope string, id int64, lock bool) (ControlSnapshot, error) {
	c := ControlSnapshot{AccountID: accountID, SubjectID: id, Scope: scope, State: ControlRunning, Mode: "request_drain"}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var deadline sql.NullTime
	err := q.QueryRowContext(ctx, "SELECT state,epoch,mode,session_deadline,session_turn_limit,updated_at FROM scheduling_controls WHERE scope=$1 AND subject_id=$2"+suffix, scope, id).Scan(&c.State, &c.Epoch, &c.Mode, &deadline, &c.SessionTurnLimit, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		if scope == ScopeAccount {
			var enabled bool
			err = q.QueryRowContext(ctx, "SELECT schedulable FROM accounts WHERE id=$1 AND deleted_at IS NULL", id).Scan(&enabled)
			if err == nil && !enabled {
				c.State = ControlPaused
			}
		} else {
			err = nil
		}
		return c, err
	}
	if deadline.Valid {
		c.SessionDeadline = &deadline.Time
	}
	return c, err
}
func counts(ctx context.Context, q sqlQueryer, c *ControlSnapshot) error {
	col := "account_id"
	if c.Scope == ScopeFamily {
		col = "family_id"
	}
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FILTER(WHERE state='dispatched' AND lease_until>NOW()),COUNT(*) FILTER(WHERE state='unknown' OR (state='dispatched' AND lease_until<=NOW())),COUNT(*) FILTER(WHERE usage_pending) FROM scheduling_attempts WHERE "+col+"=$1", c.SubjectID).Scan(&c.ActiveAttempts, &c.UnknownAttempts, &c.PendingSettlements)
	if err != nil {
		return err
	}
	if err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduling_session_grants WHERE scope=$1 AND subject_id=$2 AND epoch=$3 AND remaining_turns>0 AND expires_at>NOW()", c.Scope, c.SubjectID, c.Epoch).Scan(&c.AllowedSessions); err != nil {
		return err
	}
	if c.State == ControlDraining || c.State == ControlUncertain {
		if c.UnknownAttempts > 0 {
			c.State = ControlUncertain
		} else if c.ActiveAttempts == 0 && c.AllowedSessions == 0 {
			c.State = ControlPaused
		} else {
			c.State = ControlDraining
		}
	}
	return nil
}
func (s *PostgresStore) GetControl(ctx context.Context, id int64, scope string) (ControlSnapshot, error) {
	if scope == "" {
		scope = ScopeAccount
	}
	if err := s.ready(); err != nil {
		return ControlSnapshot{}, err
	}
	subject, err := scopeSubject(ctx, s.db, id, scope)
	if err != nil {
		return ControlSnapshot{}, err
	}
	c, err := scanControl(ctx, s.db, id, scope, subject, false)
	if err != nil {
		return c, err
	}
	err = counts(ctx, s.db, &c)
	return c, err
}
func persistDrainState(ctx context.Context, tx *sql.Tx, c *ControlSnapshot) error {
	if err := counts(ctx, tx, c); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE scheduling_controls SET state=$3 WHERE scope=$1 AND subject_id=$2 AND epoch=$4 AND state IN ('DRAINING','DRAIN_UNCERTAIN')", c.Scope, c.SubjectID, c.State, c.Epoch)
	return err
}
func (s *PostgresStore) Control(ctx context.Context, cmd ControlCommand) (ControlSnapshot, error) {
	if cmd.Scope == "" {
		cmd.Scope = ScopeAccount
	}
	if err := s.ready(); err != nil {
		return ControlSnapshot{}, err
	}
	if cmd.Action != "pause" && cmd.Action != "resume" && cmd.Action != "force_stop" && cmd.Action != "session_drain" {
		return ControlSnapshot{}, ErrInvalidControl
	}
	if cmd.Action == "session_drain" && (cmd.SessionDurationSeconds < 1 || cmd.SessionDurationSeconds > 3600 || cmd.SessionMaxTurns < 1 || cmd.SessionMaxTurns > 100) {
		return ControlSnapshot{}, ErrInvalidControl
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ControlSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := scopeSubject(ctx, tx, cmd.AccountID, cmd.Scope)
	if err != nil {
		return ControlSnapshot{}, err
	}
	// Every administrative mutation joins the same family-first lock order as
	// admission and settlement. This also serializes overlapping family/account
	// force-stop UPDATEs before either can lock a different subset of tickets.
	if cmd.Scope == ScopeAccount {
		family, e := familyFor(ctx, tx, cmd.AccountID)
		if e != nil {
			return ControlSnapshot{}, e
		}
		if e = ensureControl(ctx, tx, ScopeFamily, family); e != nil {
			return ControlSnapshot{}, e
		}
		if _, e = scanControl(ctx, tx, cmd.AccountID, ScopeFamily, family, true); e != nil {
			return ControlSnapshot{}, e
		}
	}
	if err = ensureControl(ctx, tx, cmd.Scope, id); err != nil {
		return ControlSnapshot{}, err
	}
	c, err := scanControl(ctx, tx, cmd.AccountID, cmd.Scope, id, true)
	if err != nil {
		return c, err
	}
	if cmd.ExpectedEpoch != nil && *cmd.ExpectedEpoch != c.Epoch {
		return c, ErrVersionConflict
	}
	c.Epoch++
	c.Mode = "request_drain"
	c.State = ControlDraining
	c.SessionDeadline = nil
	c.SessionTurnLimit = 0
	if cmd.Action == "resume" {
		c.State = ControlRunning
	}
	if cmd.Action == "force_stop" {
		c.Mode = "force_stop"
	}
	if cmd.Action == "session_drain" {
		c.Mode = "session_drain"
		deadline := time.Now().Add(time.Duration(cmd.SessionDurationSeconds) * time.Second)
		c.SessionDeadline = &deadline
		c.SessionTurnLimit = cmd.SessionMaxTurns
	}
	err = tx.QueryRowContext(ctx, "UPDATE scheduling_controls SET state=$3,epoch=$4,mode=$5,session_deadline=$6,session_turn_limit=$7,updated_at=NOW() WHERE scope=$1 AND subject_id=$2 RETURNING updated_at", c.Scope, c.SubjectID, c.State, c.Epoch, c.Mode, c.SessionDeadline, c.SessionTurnLimit).Scan(&c.UpdatedAt)
	if err != nil {
		return c, err
	}
	// Family resume must not release a child's independent pause. No health column is cleared.
	if c.Scope == ScopeAccount {
		if _, err = tx.ExecContext(ctx, "UPDATE accounts SET schedulable=$2,updated_at=NOW() WHERE id=$1", c.SubjectID, cmd.Action == "resume"); err != nil {
			return c, err
		}
	}
	// Transactional outbox refreshes legacy snapshots; it is not the admission authority.
	if c.Scope == ScopeAccount {
		if _, err = tx.ExecContext(ctx, "INSERT INTO scheduler_outbox(event_type,account_id) VALUES('account_changed',$1)", c.SubjectID); err != nil {
			return c, err
		}
	}
	// Persist cancellation under the admission lock. A later resume may change
	// the control epoch before another node polls, but cannot erase this ticket's intent.
	if cmd.Action == "force_stop" {
		col, epochCol := "account_id", "account_epoch"
		if c.Scope == ScopeFamily {
			col, epochCol = "family_id", "family_epoch"
		}
		if _, err = tx.ExecContext(ctx, "UPDATE scheduling_attempts SET cancel_requested=TRUE WHERE "+col+"=$1 AND "+epochCol+"<$2 AND state<>'settled'", c.SubjectID, c.Epoch); err != nil {
			return c, err
		}
	}
	if cmd.Action == "session_drain" {
		col := "account_id"
		if c.Scope == ScopeFamily {
			col = "family_id"
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO scheduling_session_grants(scope,subject_id,epoch,session_id,remaining_turns,expires_at) SELECT $1,$2,$3,session_id,$4,$5 FROM scheduling_owner_sessions WHERE "+col+"=$2 AND last_seen_at>NOW()-INTERVAL '24 hours' GROUP BY session_id ON CONFLICT DO NOTHING", c.Scope, c.SubjectID, c.Epoch, c.SessionTurnLimit, c.SessionDeadline)
		if err != nil {
			return c, err
		}
	}
	if err = persistDrainState(ctx, tx, &c); err != nil {
		return c, err
	}
	if err = tx.Commit(); err != nil {
		return c, err
	}
	if cmd.Action == "force_stop" {
		s.hookMu.RLock()
		hook := s.cancelHook
		s.hookMu.RUnlock()
		if hook != nil {
			if err = hook(ctx, c); err != nil {
				c.NotificationWarning = "control committed; cancellation delivery unconfirmed"
			}
		} else {
			c.NotificationWarning = "control committed; cancellation delivery unavailable"
		}
	}
	return c, nil
}
