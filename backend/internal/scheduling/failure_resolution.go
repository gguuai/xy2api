package scheduling

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type UnknownResolution struct {
	ExpectedVersion int64     `json:"expected_version"`
	Source          string    `json:"source"`
	Reference       string    `json:"reference"`
	ObservedAt      time.Time `json:"observed_at"`
	Outcome         string    `json:"outcome"`
	UsagePending    bool      `json:"usage_pending"`
}
type UnknownAttemptDetail struct {
	TicketID     string            `json:"ticket_id"`
	AccountID    int64             `json:"account_id"`
	State        string            `json:"state"`
	Outcome      string            `json:"outcome"`
	Version      int64             `json:"version"`
	DispatchedAt time.Time         `json:"dispatched_at"`
	Metrics      json.RawMessage   `json:"metrics"`
	Audit        []json.RawMessage `json:"audit"`
}

func (r UnknownResolution) Validate(now time.Time) error {
	if r.ExpectedVersion < 0 || len(strings.TrimSpace(r.Reference)) < 4 || len(r.Reference) > 512 || strings.ContainsAny(r.Reference, "\r\n") || r.ObservedAt.IsZero() || r.ObservedAt.After(now.Add(time.Minute)) {
		return ErrInvalidControl
	}
	switch r.Source {
	case "provider_status", "provider_receipt", "operator_verified_log":
	default:
		return ErrInvalidControl
	}
	switch r.Outcome {
	case "completed", "upstream_error", "cancelled", "not_sent":
	default:
		return ErrInvalidControl
	}
	if r.Outcome == "not_sent" && r.UsagePending {
		return ErrInvalidControl
	}
	return nil
}
func (s *PostgresStore) GetUnknownAttempt(ctx context.Context, ticket string) (UnknownAttemptDetail, error) {
	var r UnknownAttemptDetail
	r.Audit = []json.RawMessage{}
	if err := s.ready(); err != nil {
		return r, err
	}
	err := s.db.QueryRowContext(ctx, "SELECT ticket_id,account_id,state,outcome,resolution_version,dispatched_at,metrics FROM scheduling_attempts WHERE ticket_id=$1", ticket).Scan(&r.TicketID, &r.AccountID, &r.State, &r.Outcome, &r.Version, &r.DispatchedAt, &r.Metrics)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrControlNotFound
	}
	if err != nil {
		return r, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT jsonb_build_object('actor_id',actor_id,'action',action,'evidence',evidence,'created_at',created_at) FROM scheduling_failure_audit WHERE ticket_id=$1 ORDER BY id", ticket)
	if err != nil {
		return r, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return r, err
		}
		r.Audit = append(r.Audit, json.RawMessage(raw))
	}
	return r, rows.Err()
}

// The operator attests externally verified evidence; this API never treats time
// elapsed or a local cancellation as proof of the provider's terminal execution.
func (s *PostgresStore) ResolveUnknownWithEvidence(ctx context.Context, ticket string, r UnknownResolution, actor int64) (UnknownAttemptDetail, error) {
	var empty UnknownAttemptDetail
	if err := s.ready(); err != nil {
		return empty, err
	}
	if err := r.Validate(time.Now()); err != nil {
		return empty, err
	}
	if actor <= 0 {
		return empty, ErrInvalidControl
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer func() { _ = tx.Rollback() }()
	var accountID, familyID int64
	if err = tx.QueryRowContext(ctx, "SELECT account_id,family_id FROM scheduling_attempts WHERE ticket_id=$1", ticket).Scan(&accountID, &familyID); err != nil {
		return empty, err
	}
	controls := []ControlSnapshot{}
	for _, k := range []struct {
		scope string
		id    int64
	}{{ScopeFamily, familyID}, {ScopeAccount, accountID}} {
		c, e := scanControl(ctx, tx, accountID, k.scope, k.id, true)
		if e != nil {
			return empty, e
		}
		controls = append(controls, c)
	}
	var state, outcome string
	var version int64
	var dispatched time.Time
	if err = tx.QueryRowContext(ctx, "SELECT state,outcome,resolution_version,dispatched_at FROM scheduling_attempts WHERE ticket_id=$1 FOR UPDATE", ticket).Scan(&state, &outcome, &version, &dispatched); err != nil {
		return empty, err
	}
	if r.ObservedAt.Before(dispatched) {
		return empty, ErrInvalidControl
	}
	raw, _ := json.Marshal(r)
	if state == "settled" && version == r.ExpectedVersion+1 {
		var same bool
		err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM scheduling_failure_audit WHERE ticket_id=$1 AND action='unknown_resolution' AND evidence=$2::jsonb)", ticket, string(raw)).Scan(&same)
		if err != nil {
			return empty, err
		}
		if !same || outcome != r.Outcome {
			return empty, ErrVersionConflict
		}
		if err = tx.Commit(); err != nil {
			return empty, err
		}
		return s.GetUnknownAttempt(ctx, ticket)
	}
	if state != "unknown" || version != r.ExpectedVersion {
		return empty, ErrVersionConflict
	}
	_, err = tx.ExecContext(ctx, "UPDATE scheduling_attempts SET state='settled',outcome=$2,usage_pending=($3 AND NOT usage_acknowledged),resolution_version=resolution_version+1,settled_at=NOW() WHERE ticket_id=$1", ticket, r.Outcome, r.UsagePending)
	if err != nil {
		return empty, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO scheduling_failure_audit(account_id,ticket_id,actor_id,action,evidence) VALUES($1,$2,$3,'unknown_resolution',$4::jsonb)", accountID, ticket, actor, string(raw))
	if err != nil {
		return empty, err
	}
	for i := range controls {
		if err = persistDrainState(ctx, tx, &controls[i]); err != nil {
			return empty, err
		}
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return s.GetUnknownAttempt(ctx, ticket)
}

type FailureAdminStore interface {
	PermitFailureRecovery(context.Context, int64, FailureRecoveryEvidence, int64) (AccountFailureState, error)
	GetFailureDomains(context.Context, int64) (AccountFailureDomains, error)
	PutFailureDomains(context.Context, AccountFailureDomains, int64) (AccountFailureDomains, error)
	InspectFailureDomains(context.Context, int64, string) (AccountFailureState, error)
	GetUnknownAttempt(context.Context, string) (UnknownAttemptDetail, error)
	ResolveUnknownWithEvidence(context.Context, string, UnknownResolution, int64) (UnknownAttemptDetail, error)
}
