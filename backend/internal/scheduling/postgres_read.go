package scheduling

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

// AccountActiveCounts includes uncertain attempts, even when a lease has expired.
func (s *PostgresStore) AccountActiveCounts(ctx context.Context, ids []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(ids))
	if err := s.ready(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, "SELECT account_id,COUNT(*) FROM scheduling_attempts WHERE account_id=ANY($1) AND state<>'settled' GROUP BY account_id", pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var n int
		if err = rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// CanContinueSession is strictly read-only and never spends a turn. The final
// dispatch transaction checks the same grants while holding both control locks.
func (s *PostgresStore) CanContinueSession(ctx context.Context, accountID int64, sessionID string) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	if sessionID == "" {
		return false, nil
	}
	family, err := familyFor(ctx, s.db, accountID)
	if err != nil {
		return false, err
	}
	usesGrant := false
	for _, k := range []struct {
		scope string
		id    int64
	}{{ScopeFamily, family}, {ScopeAccount, accountID}} {
		c, e := scanControl(ctx, s.db, accountID, k.scope, k.id, false)
		if e != nil {
			return false, e
		}
		if c.State == ControlRunning {
			continue
		}
		if c.Mode != "session_drain" || (c.State != ControlDraining && c.State != ControlUncertain) {
			return false, nil
		}
		var exists bool
		e = s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM scheduling_session_grants WHERE scope=$1 AND subject_id=$2 AND epoch=$3 AND session_id=$4 AND remaining_turns>0 AND expires_at>NOW())", k.scope, k.id, c.Epoch, sessionID).Scan(&exists)
		if e != nil {
			return false, e
		}
		if !exists {
			return false, nil
		}
		usesGrant = true
	}
	return usesGrant, nil
}

// CanAdmitControl is a read-only check of both scopes. It is suitable for candidate
// filtering; only BeginDispatch grants permission to send.
func (s *PostgresStore) CanAdmitControl(ctx context.Context, accountID int64, sessionID string) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	family, err := familyFor(ctx, s.db, accountID)
	if err != nil {
		return false, err
	}
	for _, k := range []struct {
		scope string
		id    int64
	}{{ScopeFamily, family}, {ScopeAccount, accountID}} {
		c, e := scanControl(ctx, s.db, accountID, k.scope, k.id, false)
		if e != nil {
			return false, e
		}
		if c.State != ControlRunning {
			return s.CanContinueSession(ctx, accountID, sessionID)
		}
	}
	return true, nil
}

// A read-only lookup for integrations that must preserve strong response ownership.
func (s *PostgresStore) Attempt(ctx context.Context, id string) (DispatchTicket, string, error) {
	t := DispatchTicket{TicketID: id}
	var state string
	if err := s.ready(); err != nil {
		return t, state, err
	}
	err := s.db.QueryRowContext(ctx, "SELECT request_id,account_id,family_id,account_epoch,family_epoch,lease_until,state FROM scheduling_attempts WHERE ticket_id=$1", id).Scan(&t.RequestID, &t.AccountID, &t.FamilyID, &t.AccountEpoch, &t.FamilyEpoch, &t.LeaseUntil, &state)
	if err == sql.ErrNoRows {
		err = ErrAttemptIdentity
	}
	return t, state, err
}

// AttemptCancellationRequested remains true after force-stop followed by resume.
// It is not an acknowledgement that the remote computation has terminated.
func (s *PostgresStore) AttemptCancellationRequested(ctx context.Context, ticketID string) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	var requested bool
	err := s.db.QueryRowContext(ctx, "SELECT cancel_requested FROM scheduling_attempts WHERE ticket_id=$1", ticketID).Scan(&requested)
	if err == sql.ErrNoRows {
		err = ErrAttemptIdentity
	}
	return requested, err
}

// RecordAttemptMetrics allows only scheduler-owned, non-payload fields. Null
// timing remains unobserved; callers must not manufacture successful zero values.
func (s *PostgresStore) RecordAttemptMetrics(ctx context.Context, ticketID string, metrics any) error {
	if err := s.ready(); err != nil {
		return err
	}
	raw, err := json.Marshal(metrics)
	if err != nil {
		return err
	}
	var source map[string]json.RawMessage
	if err = json.Unmarshal(raw, &source); err != nil {
		return ErrInvalidControl
	}
	allowed := map[string]string{
		"group_id": "number", "model": "string", "dispatch_kind": "kind", "sent_at": "time", "attempt_number": "number",
		"priority": "number", "reason": "string", "metric_version": "string", "policy_version": "number",
		"first_event_ms": "number", "first_semantic_ms": "number", "first_answer_ms": "number",
		"overall_first_semantic_ms": "number", "remaining_budget_ms": "number", "stop_reason": "string",
	}
	safe := make(map[string]json.RawMessage)
	for key, kind := range allowed {
		value, exists := source[key]
		if !exists || string(value) == "null" {
			continue
		}
		if kind == "string" || kind == "time" || kind == "kind" {
			var v string
			if json.Unmarshal(value, &v) != nil || len(v) > 256 {
				return ErrInvalidControl
			}
			if kind == "time" {
				stamp, e := time.Parse(time.RFC3339Nano, v)
				if e != nil {
					return ErrInvalidControl
				}
				value, _ = json.Marshal(stamp.UTC().Format(time.RFC3339Nano))
			}
			if kind == "kind" && v != "ordinary_first" && v != "retry" && v != "probe" && v != "pin" && v != "owner" && v != "fallback" {
				return ErrInvalidControl
			}
		} else {
			var v int64
			if json.Unmarshal(value, &v) != nil {
				return ErrInvalidControl
			}
			if key != "priority" && v < 0 {
				return ErrInvalidControl
			}
		}
		safe[key] = value
	}
	raw, err = json.Marshal(safe)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, "UPDATE scheduling_attempts SET metrics=metrics || $2::jsonb,usage_pending=CASE WHEN $2::jsonb ? 'sent_at' AND NOT usage_acknowledged THEN TRUE ELSE usage_pending END WHERE ticket_id=$1", ticketID, string(raw))
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

func (s *PostgresStore) ListRequestAttempts(ctx context.Context, requestID string) ([]AttemptRecord, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(requestID) == "" || len(requestID) > 256 {
		return nil, ErrInvalidControl
	}
	rows, err := s.db.QueryContext(ctx, "SELECT ticket_id,request_id,account_id,family_id,state,outcome,cancel_requested,usage_pending,dispatched_at,settled_at,metrics FROM scheduling_attempts WHERE request_id=$1 ORDER BY dispatched_at,ticket_id", requestID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]AttemptRecord, 0)
	for rows.Next() {
		var r AttemptRecord
		var ended sql.NullTime
		var raw []byte
		if err = rows.Scan(&r.TicketID, &r.RequestID, &r.AccountID, &r.FamilyID, &r.State, &r.Outcome, &r.CancelRequested, &r.UsagePending, &r.DispatchedAt, &ended, &raw); err != nil {
			return nil, err
		}
		if ended.Valid {
			r.SettledAt = &ended.Time
		}
		r.Metrics = json.RawMessage(raw)
		result = append(result, r)
	}
	return result, rows.Err()
}

// DispatchStatistics counts confirmed physical sends. Selection reservations,
// paused admissions, and unfinished pre-send tickets never enter the denominator.
func (s *PostgresStore) DispatchStatistics(ctx context.Context, groupID int64, model string, since, until time.Time) (DispatchStats, error) {
	result := DispatchStats{GroupID: groupID, Model: model, Since: since, Until: until, Accounts: []AccountDispatchStats{}}
	if err := s.ready(); err != nil {
		return result, err
	}
	if groupID < 0 || strings.TrimSpace(model) == "" || since.IsZero() || !until.After(since) {
		return result, ErrInvalidControl
	}
	rows, err := s.db.QueryContext(ctx, "SELECT account_id,metrics->>'dispatch_kind',COUNT(*) FROM scheduling_attempts WHERE metrics->>'group_id'=$1 AND metrics->>'model'=$2 AND outcome<>'not_sent' AND metrics ? 'sent_at' AND (metrics->>'sent_at')::timestamptz >= $3 AND (metrics->>'sent_at')::timestamptz < $4 AND metrics->>'dispatch_kind' IN ('ordinary_first','retry','probe','pin','owner','fallback') GROUP BY account_id,metrics->>'dispatch_kind'", strconv.FormatInt(groupID, 10), model, since, until)
	if err != nil {
		return result, err
	}
	defer func() { _ = rows.Close() }()
	byAccount := map[int64]*AccountDispatchStats{}
	for rows.Next() {
		var id, n int64
		var kind string
		if err = rows.Scan(&id, &kind, &n); err != nil {
			return result, err
		}
		a := byAccount[id]
		if a == nil {
			a = &AccountDispatchStats{AccountID: id, ByKind: map[string]int64{"ordinary_first": 0, "retry": 0, "probe": 0, "pin": 0, "owner": 0, "fallback": 0}}
			byAccount[id] = a
		}
		a.ByKind[kind] = n
		if kind == "ordinary_first" {
			a.OrdinaryFirst = n
			result.OrdinaryFirstTotal += n
		}
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	for _, a := range byAccount {
		if result.OrdinaryFirstTotal > 0 {
			share := float64(a.OrdinaryFirst) / float64(result.OrdinaryFirstTotal)
			a.OrdinaryFirstShare = &share
		}
		result.Accounts = append(result.Accounts, *a)
	}
	sort.Slice(result.Accounts, func(i, j int) bool { return result.Accounts[i].AccountID < result.Accounts[j].AccountID })
	return result, nil
}
