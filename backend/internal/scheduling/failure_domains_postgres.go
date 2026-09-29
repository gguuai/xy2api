package scheduling

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/lib/pq"
)

var ErrFailureDomainBlocked = errors.New("declared failure domain unavailable or recovery in flight")

type FailureGate struct {
	Key         string     `json:"key"`
	Scope       string     `json:"scope"`
	Reason      string     `json:"reason"`
	Version     int64      `json:"version"`
	Blocked     bool       `json:"blocked"`
	ReadyAfter  *time.Time `json:"ready_after,omitempty"`
	ProbeActive bool       `json:"probe_active"`
}
type AccountFailureState struct {
	Domains  AccountFailureDomains `json:"domains"`
	Gates    []FailureGate         `json:"gates"`
	Eligible bool                  `json:"eligible"`
}

func readFailureAdmission(ctx context.Context, q sqlQueryer, id int64, model string) (FailureAdmission, error) {
	a := FailureAdmission{AccountID: id, Model: model, Domains: AccountFailureDomains{AccountID: id}}
	var credentials []byte
	var extra []byte
	var platform, accountType string
	err := q.QueryRowContext(ctx, "SELECT c.id,c.credentials,COALESCE(c.extra,'{}'::jsonb),c.platform,c.type,0,''::text,''::text FROM accounts a JOIN accounts c ON c.id=COALESCE(a.parent_account_id,a.id) AND c.deleted_at IS NULL AND c.parent_account_id IS NULL WHERE a.id=$1 AND a.deleted_at IS NULL AND (a.parent_account_id IS NULL OR (c.platform='openai' AND c.type='oauth'))", id).Scan(&a.CredentialOwnerID, &credentials, &extra, &platform, &accountType, &a.Domains.Version, &a.Domains.QuotaPoolID, &a.Domains.AvailabilityPoolID)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrControlNotFound
	}
	if err != nil {
		return a, err
	}
	return decodeFailureAdmission(a, credentials, extra, platform, accountType)
}

// Share identity decoding between single-winner admission and batch preflight.
func decodeFailureAdmission(a FailureAdmission, credentials, extra []byte, platform, accountType string) (FailureAdmission, error) {
	a.Credential = CredentialFingerprint(credentials)
	var credentialData, extraData map[string]any
	if json.Unmarshal(credentials, &credentialData) != nil || json.Unmarshal(extra, &extraData) != nil {
		return a, ErrInvalidControl
	}
	a.HealthIdentity = StableHealthIdentity(platform, accountType, credentialData, extraData)
	if a.Credential == "" {
		return a, ErrInvalidControl
	}
	a.SetKeys()
	return a, nil
}

func sameFailureIdentity(a, b FailureAdmission) bool {
	return a.CredentialOwnerID == b.CredentialOwnerID && a.Credential == b.Credential && a.HealthIdentity == b.HealthIdentity && a.Domains == b.Domains
}

// Lock actual credential owner as well as the logical account. Re-read after
// locking also detects a concurrent reparent; no stale owner can authorize send.
func lockFailureIdentity(ctx context.Context, tx *sql.Tx, id, ownerID int64) error {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM accounts WHERE deleted_at IS NULL AND id IN ($1,$2) ORDER BY id FOR SHARE", id, ownerID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var owner int64
		if err = rows.Scan(&owner); err != nil {
			return err
		}
		n++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if n == 0 {
		return ErrControlNotFound
	}
	return nil
}

func (s *PostgresStore) FreezeFailureAdmission(ctx context.Context, id int64, model string) (FailureAdmission, error) {
	if err := s.ready(); err != nil {
		return FailureAdmission{}, err
	}
	return readFailureAdmission(ctx, s.db, id, model)
}

// This is an observation applicability check, not a health-generation reset.
func (s *PostgresStore) FailureIdentityCurrent(ctx context.Context, frozen *FailureAdmission) (bool, error) {
	if frozen == nil {
		return true, nil
	}
	current, err := s.FreezeFailureAdmission(ctx, frozen.AccountID, frozen.Model)
	if err != nil {
		return false, err
	}
	return sameFailureIdentity(current, *frozen), nil
}
func (s *PostgresStore) GetFailureDomains(ctx context.Context, id int64) (AccountFailureDomains, error) {
	a, e := s.FreezeFailureAdmission(ctx, id, "")
	return a.Domains, e
}
func (s *PostgresStore) PutFailureDomains(ctx context.Context, c AccountFailureDomains, actor int64) (AccountFailureDomains, error) {
	if err := s.ready(); err != nil {
		return c, err
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer func() { _ = tx.Rollback() }()
	var lockedID int64
	if err = tx.QueryRowContext(ctx, "SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", c.AccountID).Scan(&lockedID); err != nil {
		return c, err
	}
	if _, err = familyFor(ctx, tx, c.AccountID); err != nil {
		return c, err
	}
	expected := c.Version
	if expected == 0 {
		err = tx.QueryRowContext(ctx, "INSERT INTO scheduling_account_failure_domains(account_id,version,quota_pool_id,availability_pool_id) VALUES($1,1,$2,$3) ON CONFLICT(account_id) DO NOTHING RETURNING version", c.AccountID, c.QuotaPoolID, c.AvailabilityPoolID).Scan(&c.Version)
	} else {
		err = tx.QueryRowContext(ctx, "UPDATE scheduling_account_failure_domains SET version=version+1,quota_pool_id=$2,availability_pool_id=$3,updated_at=NOW() WHERE account_id=$1 AND version=$4 RETURNING version", c.AccountID, c.QuotaPoolID, c.AvailabilityPoolID, expected).Scan(&c.Version)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrVersionConflict
	}
	if err != nil {
		return c, err
	}
	raw, _ := json.Marshal(c)
	if _, err = tx.ExecContext(ctx, "INSERT INTO scheduling_failure_audit(account_id,actor_id,action,evidence) VALUES($1,$2,'domain_configuration',$3::jsonb)", c.AccountID, actor, string(raw)); err != nil {
		return c, err
	}
	return c, tx.Commit()
}
func failureGates(ctx context.Context, q sqlQueryer, keys []string) ([]FailureGate, error) {
	rows, err := q.QueryContext(ctx, "SELECT gate_key,scope,reason,version,blocked,ready_after,EXISTS(SELECT 1 FROM scheduling_attempts a WHERE "+admissionOccupancySQL("a.")+" AND g.gate_key=ANY(a.failure_probe_keys)) FROM scheduling_failure_gates g WHERE gate_key=ANY($1) ORDER BY gate_key", pq.Array(keys))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []FailureGate{}
	for rows.Next() {
		var g FailureGate
		var until sql.NullTime
		if err = rows.Scan(&g.Key, &g.Scope, &g.Reason, &g.Version, &g.Blocked, &until, &g.ProbeActive); err != nil {
			return nil, err
		}
		if until.Valid {
			g.ReadyAfter = &until.Time
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
func (s *PostgresStore) InspectFailureDomains(ctx context.Context, id int64, model string) (AccountFailureState, error) {
	a, e := s.FreezeFailureAdmission(ctx, id, model)
	if e != nil {
		return AccountFailureState{}, e
	}
	gates, e := failureGates(ctx, s.db, a.Keys)
	if e != nil {
		return AccountFailureState{}, e
	}
	r := AccountFailureState{Domains: a.Domains, Gates: gates, Eligible: true}
	now := time.Now()
	for _, g := range gates {
		if g.Blocked || g.ProbeActive || (g.ReadyAfter != nil && g.ReadyAfter.After(now)) {
			r.Eligible = false
		}
	}
	return r, nil
}

// Called inside BeginDispatch after existing family/account locks, before ticket.
// Sorted open gate SHARE locks allow healthy members to dispatch concurrently.
// Only cooldown/recovery gates use exclusive locks to serialize one probe. An expired Redis lease never erases
// a live PG attempt's gate keys. Unknown executions keep a bounded hold, then
// permit one recovery probe; their remote outcomes remain unknown in the audit.
func admitFailureDomains(ctx context.Context, tx *sql.Tx, a *FailureAdmission) error {
	if a == nil {
		return nil
	}
	if err := lockFailureIdentity(ctx, tx, a.AccountID, a.CredentialOwnerID); err != nil {
		return err
	}
	current, err := readFailureAdmission(ctx, tx, a.AccountID, a.Model)
	if err != nil {
		return err
	}
	if !sameFailureIdentity(current, *a) {
		return ErrVersionConflict
	}
	a.SetKeys()
	keys := append([]string(nil), a.Keys...)
	sort.Strings(keys)
	a.ProbeVersions = map[string]int64{}
	for _, key := range keys {
		_, err = tx.ExecContext(ctx, "INSERT INTO scheduling_failure_gates(gate_key,scope,reason,version) VALUES($1,'none','open',0) ON CONFLICT(gate_key) DO NOTHING", key)
		if err != nil {
			return err
		}
		var blocked bool
		var ready sql.NullTime
		var version int64
		// The conditional SHARE read never upgrades a held lock: if the gate is
		// closed/recovering, the predicate excludes it before FOR UPDATE.
		err = tx.QueryRowContext(ctx, "SELECT blocked,ready_after,version FROM scheduling_failure_gates WHERE gate_key=$1 AND NOT blocked AND ready_after IS NULL FOR SHARE", key).Scan(&blocked, &ready, &version)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, "SELECT blocked,ready_after,version FROM scheduling_failure_gates WHERE gate_key=$1 FOR UPDATE", key).Scan(&blocked, &ready, &version)
		}
		if err != nil {
			return err
		}
		if blocked || (ready.Valid && ready.Time.After(time.Now())) {
			return ErrFailureDomainBlocked
		}
		if ready.Valid {
			var active bool
			if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM scheduling_attempts WHERE "+admissionOccupancySQL("")+" AND $1=ANY(failure_probe_keys))", key).Scan(&active); err != nil {
				return err
			}
			if active {
				return ErrFailureDomainBlocked
			}
			// The expired predecessor can still report a genuine late terminal.
			// Give this probe a new generation under the existing exclusive gate
			// lock so the predecessor's feedback CAS cannot open our live gate.
			if err = tx.QueryRowContext(ctx, "UPDATE scheduling_failure_gates SET version=version+1,updated_at=NOW() WHERE gate_key=$1 RETURNING version", key).Scan(&version); err != nil {
				return err
			}
			a.ProbeVersions[key] = version
		}
	}
	return nil
}
func persistFailureAdmission(ctx context.Context, tx *sql.Tx, ticket string, a *FailureAdmission) error {
	if a == nil {
		return nil
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	keys := []string{}
	for key := range a.ProbeVersions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	_, err = tx.ExecContext(ctx, "UPDATE scheduling_attempts SET failure_snapshot=$2::jsonb,failure_probe_keys=$3 WHERE ticket_id=$1", ticket, string(raw), pq.Array(keys))
	return err
}

// ApplyFailureFeedback is once per ticket and validates frozen identities before
// touching gates. PG is authority; Redis gate caches may only mirror this result.
func (s *PostgresStore) ApplyFailureFeedback(ctx context.Context, ticket string, d FailureDecision, completed bool) error {
	if err := s.ready(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var raw []byte
	var accountID int64
	if err = tx.QueryRowContext(ctx, "SELECT account_id,failure_snapshot FROM scheduling_attempts WHERE ticket_id=$1", ticket).Scan(&accountID, &raw); err != nil {
		return err
	}
	var a FailureAdmission
	if json.Unmarshal(raw, &a) != nil || a.AccountID == 0 {
		return ErrInvalidControl
	}
	identityMissing := false
	if err = lockFailureIdentity(ctx, tx, accountID, a.CredentialOwnerID); errors.Is(err, ErrControlNotFound) {
		identityMissing = true
	} else if err != nil {
		return err
	}
	current, err := readFailureAdmission(ctx, tx, accountID, a.Model)
	if errors.Is(err, ErrControlNotFound) {
		identityMissing = true
	} else if err != nil {
		return err
	}
	stale := identityMissing || !sameFailureIdentity(current, a)
	evidence, _ := json.Marshal(map[string]any{"decision": d, "completed": completed, "stale_identity": stale, "identity_missing": identityMissing})
	result, err := tx.ExecContext(ctx, "INSERT INTO scheduling_failure_audit(account_id,ticket_id,action,evidence) VALUES($1,$2,'failure_feedback',$3::jsonb) ON CONFLICT DO NOTHING", accountID, ticket, string(evidence))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 || stale || (len(a.ProbeVersions) == 0 && d.Effect != "cooldown" && d.Effect != "auth_block") {
		return completeFailureFeedback(ctx, tx, ticket)
	}
	// All writers take the identical sorted key order before updating any gate.
	lockKeys := append([]string(nil), a.Keys...)
	sort.Strings(lockKeys)
	for _, key := range lockKeys {
		var version int64
		if err = tx.QueryRowContext(ctx, "SELECT version FROM scheduling_failure_gates WHERE gate_key=$1 FOR UPDATE", key).Scan(&version); err != nil {
			return err
		}
	}
	if completed && len(a.ProbeVersions) > 0 {
		var state string
		if err = tx.QueryRowContext(ctx, "SELECT state FROM scheduling_attempts WHERE ticket_id=$1", ticket).Scan(&state); err != nil {
			return err
		}
		if state != "settled" {
			return ErrAttemptIdentity
		}
	}
	if d.Key != "" && (d.Effect == "cooldown" || d.Effect == "auth_block") {
		valid := false
		for _, key := range a.Keys {
			if key == d.Key {
				valid = true
			}
		}
		if !valid {
			return ErrInvalidControl
		}
		var until any
		if !d.Until.IsZero() {
			until = d.Until
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO scheduling_failure_gates(gate_key,scope,reason,blocked,ready_after,version) VALUES($1,$2,$3,$4,$5,1) ON CONFLICT(gate_key) DO UPDATE SET scope=EXCLUDED.scope,reason=EXCLUDED.reason,blocked=scheduling_failure_gates.blocked OR EXCLUDED.blocked,ready_after=GREATEST(scheduling_failure_gates.ready_after,EXCLUDED.ready_after),version=scheduling_failure_gates.version+1,updated_at=NOW()", d.Key, d.Scope, d.Reason, d.Hard, until)
		if err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(a.ProbeVersions))
	for key := range a.ProbeVersions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if completed {
			_, err = tx.ExecContext(ctx, "UPDATE scheduling_failure_gates SET ready_after=NULL,reason='recovery_confirmed',version=version+1,updated_at=NOW() WHERE gate_key=$1 AND version=$2 AND NOT blocked", key, a.ProbeVersions[key])
		} else {
			_, err = tx.ExecContext(ctx, "UPDATE scheduling_failure_gates SET ready_after=GREATEST(ready_after,NOW()+INTERVAL '30 seconds'),reason='recovery_failed',version=version+1,updated_at=NOW() WHERE gate_key=$1 AND version=$2 AND NOT blocked", key, a.ProbeVersions[key])
		}
		if err != nil {
			return err
		}
	}
	return completeFailureFeedback(ctx, tx, ticket)
}

// The outbox acknowledgement, audit receipt and gate effects commit together.
// This also handles direct feedback that precedes a delayed intent write.
func completeFailureFeedback(ctx context.Context, tx *sql.Tx, ticket string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE scheduling_attempts SET failure_feedback_done=TRUE,failure_feedback_next_retry_at=NULL,failure_feedback_retry_count=0 WHERE ticket_id=$1", ticket); err != nil {
		return err
	}
	return tx.Commit()
}
