package scheduling

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Optional capabilities preserve compatibility with read-only/admin test stores.
type SchedulingPolicyRestorer interface {
	RestorePolicyInheritance(context.Context, int64, string, int64) (PolicyRecord, error)
}
type SchedulingPolicyHealthResetter interface {
	PutPolicyWithHealthReset(context.Context, Policy, int64, []string) (PolicyRecord, error)
}

func decodePolicyRecord(r *PolicyRecord, raw []byte) error {
	var p *Policy
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("decode scheduling policy: %w", err)
	}
	if p != nil {
		p.GroupID = r.GroupID
		p.Model = r.Model
		p.Version = r.Version
		r.Diagnostics = ProfileAmbiguities(*p)
	}
	r.Policy = p
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
	err = decodePolicyRecord(&r, raw)
	return r, err
}
func (s *PostgresStore) PutPolicy(ctx context.Context, p Policy, expected int64) (PolicyRecord, error) {
	return s.PutPolicyWithHealthReset(ctx, p, expected, nil)
}

// Total deadline and minimum retry window do not change semantic health.
func sameProfileHealth(a, b LatencyProfile) bool {
	reasoning := func(v string) string {
		if v == "" {
			return ""
		}
		return NormalizeReasoningLabel(v)
	}
	return a.Name == b.Name && reasoning(a.Reasoning) == reasoning(b.Reasoning) &&
		CanonicalTransport(a.Transport) == CanonicalTransport(b.Transport) &&
		a.ContextMinTokens == b.ContextMinTokens && a.ContextMaxTokens == b.ContextMaxTokens &&
		a.HealthThresholdMS == b.HealthThresholdMS && a.RecoveryThresholdMS == b.RecoveryThresholdMS && a.AttemptTimeoutMS == b.AttemptTimeoutMS
}
func (s *PostgresStore) PutPolicyWithHealthReset(ctx context.Context, p Policy, expected int64, resetNames []string) (PolicyRecord, error) {
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
	reset := make(map[string]bool, len(resetNames))
	names := make(map[string]bool, len(p.Profiles))
	for _, v := range p.Profiles {
		names[v.Name] = true
	}
	for _, name := range resetNames {
		if strings.TrimSpace(name) == "" || !names[name] || reset[name] {
			return r, ErrInvalidControl
		}
		reset[name] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer func() { _ = tx.Rollback() }()
	inserted := false
	if expected == 0 {
		result, insertErr := tx.ExecContext(ctx, "INSERT INTO scheduling_policies(group_id,model,version,policy) VALUES($1,$2,1,'null'::jsonb) ON CONFLICT(group_id,model) DO NOTHING", p.GroupID, p.Model)
		if insertErr != nil {
			return r, insertErr
		}
		affected, countErr := result.RowsAffected()
		if countErr != nil {
			return r, countErr
		}
		inserted = affected == 1
		if !inserted {
			return r, ErrVersionConflict
		}
	}
	var raw []byte
	var version int64
	err = tx.QueryRowContext(ctx, "SELECT version,policy FROM scheduling_policies WHERE group_id=$1 AND model=$2 FOR UPDATE", p.GroupID, p.Model).Scan(&version, &raw)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !inserted && version != expected {
		return r, ErrVersionConflict
	}
	if err != nil {
		return r, err
	}
	old := PolicyRecord{GroupID: p.GroupID, Model: p.Model, Version: version}
	if err = decodePolicyRecord(&old, raw); err != nil {
		return r, err
	}
	// Never trust a revision supplied by the client, including copied global profiles.
	oldProfiles := map[string]LatencyProfile{}
	if old.Policy != nil {
		for _, v := range old.Policy.Profiles {
			oldProfiles[v.Name] = v
		}
	}
	p.Profiles = append([]LatencyProfile(nil), p.Profiles...)
	for i := range p.Profiles {
		v := &p.Profiles[i]
		previous, exists := oldProfiles[v.Name]
		if reset[v.Name] && !exists {
			return r, ErrInvalidControl
		}
		if exists && !reset[v.Name] && previous.HealthRevision > 0 && sameProfileHealth(previous, *v) {
			v.HealthRevision = previous.HealthRevision
		} else {
			if err = tx.QueryRowContext(ctx, "SELECT nextval('scheduling_profile_health_revision_seq')").Scan(&v.HealthRevision); err != nil {
				return r, err
			}
		}
	}
	p.Version = expected + 1
	raw, err = json.Marshal(p)
	if err != nil {
		return r, err
	}
	err = tx.QueryRowContext(ctx, "UPDATE scheduling_policies SET version=$5,policy=$3::jsonb,updated_at=NOW() WHERE group_id=$1 AND model=$2 AND version=$4 RETURNING version", p.GroupID, p.Model, string(raw), version, p.Version).Scan(&r.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrVersionConflict
	}
	if err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	r.Policy = &p
	return r, nil
}

// A JSON-null tombstone preserves CAS history; deleting the row would allow ABA.
func (s *PostgresStore) RestorePolicyInheritance(ctx context.Context, groupID int64, model string, expected int64) (PolicyRecord, error) {
	r := PolicyRecord{GroupID: groupID, Model: model}
	if err := s.ready(); err != nil {
		return r, err
	}
	if groupID <= 0 || strings.TrimSpace(model) == "" || expected < 1 {
		return r, ErrInvalidControl
	}
	err := s.db.QueryRowContext(ctx, "UPDATE scheduling_policies SET version=version+1,policy='null'::jsonb,updated_at=NOW() WHERE group_id=$1 AND model=$2 AND version=$3 AND policy<>'null'::jsonb RETURNING version", groupID, model, expected).Scan(&r.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrVersionConflict
	}
	return r, err
}
