package scheduling

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Recovery permits one guarded request; it does not declare the domain healthy.
type FailureRecoveryEvidence struct {
	GateKey         string    `json:"gate_key"`
	Model           string    `json:"model"`
	ExpectedVersion int64     `json:"expected_version"`
	DomainVersion   int64     `json:"domain_version"`
	Source          string    `json:"source"`
	Reference       string    `json:"reference"`
	ObservedAt      time.Time `json:"observed_at"`
}

func (s *PostgresStore) PermitFailureRecovery(ctx context.Context, id int64, r FailureRecoveryEvidence, actor int64) (AccountFailureState, error) {
	var empty AccountFailureState
	if err := s.ready(); err != nil {
		return empty, err
	}
	v := UnknownResolution{ExpectedVersion: r.ExpectedVersion, Source: r.Source, Reference: r.Reference, ObservedAt: r.ObservedAt, Outcome: "completed"}
	if err := v.Validate(time.Now()); err != nil {
		return empty, err
	}
	if actor <= 0 || len(r.GateKey) != 64 || r.DomainVersion < 0 {
		return empty, ErrInvalidControl
	}
	frozen, err := s.FreezeFailureAdmission(ctx, id, r.Model)
	if err != nil {
		return empty, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockFailureIdentity(ctx, tx, id, frozen.CredentialOwnerID); err != nil {
		return empty, err
	}
	current, err := readFailureAdmission(ctx, tx, id, r.Model)
	if err != nil {
		return empty, err
	}
	if !sameFailureIdentity(frozen, current) || current.Domains.Version != r.DomainVersion {
		return empty, ErrVersionConflict
	}
	matched := false
	for _, key := range current.Keys {
		if key == r.GateKey {
			matched = true
		}
	}
	if !matched {
		return empty, ErrInvalidControl
	}
	var version int64
	var updated time.Time
	var blocked bool
	var ready sql.NullTime
	err = tx.QueryRowContext(ctx, "SELECT version,updated_at,blocked,ready_after FROM scheduling_failure_gates WHERE gate_key=$1 FOR UPDATE", r.GateKey).Scan(&version, &updated, &blocked, &ready)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, ErrControlNotFound
	}
	if err != nil {
		return empty, err
	}
	if version != r.ExpectedVersion {
		return empty, ErrVersionConflict
	}
	if !blocked && !ready.Valid {
		return empty, ErrInvalidControl
	}
	if r.ObservedAt.Before(updated) {
		return empty, ErrInvalidControl
	}
	var active bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM scheduling_attempts WHERE "+admissionOccupancySQL("")+" AND $1=ANY(failure_probe_keys))", r.GateKey).Scan(&active); err != nil {
		return empty, err
	}
	if active {
		return empty, ErrFailureDomainBlocked
	}
	_, err = tx.ExecContext(ctx, "UPDATE scheduling_failure_gates SET blocked=FALSE,ready_after=NOW(),reason='verified_recovery_probe',version=version+1,updated_at=NOW() WHERE gate_key=$1", r.GateKey)
	if err != nil {
		return empty, err
	}
	raw, _ := json.Marshal(r)
	_, err = tx.ExecContext(ctx, "INSERT INTO scheduling_failure_audit(account_id,actor_id,action,evidence) VALUES($1,$2,'verified_domain_recovery',$3::jsonb)", id, actor, string(raw))
	if err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return s.InspectFailureDomains(ctx, id, r.Model)
}
