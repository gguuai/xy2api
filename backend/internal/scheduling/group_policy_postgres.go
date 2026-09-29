package scheduling

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
)

func readStoredGroupPolicy(ctx context.Context, q sqlQueryer, groupID int64) (GroupPolicy, bool, error) {
	p := DefaultGroupPolicy(groupID)
	var version int64
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT version,policy FROM scheduling_group_policies WHERE group_id=$1", groupID).Scan(&version, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, false, fmt.Errorf("decode group scheduling policy: %w", err)
	}
	p.GroupID, p.Version = groupID, version
	if err = ValidateGroupPolicy(p); err != nil {
		return p, false, err
	}
	if p.Accounts == nil {
		p.Accounts = []AccountRule{}
	}
	return p, true, nil
}

// ReadGroupPolicy is the request-path read. Legacy model policies are never
// consulted and account membership/capability stays with the gateway candidates.
func (s *PostgresStore) ReadGroupPolicy(ctx context.Context, groupID int64) (GroupPolicy, error) {
	if err := s.ready(); err != nil {
		return GroupPolicy{}, err
	}
	if groupID < 0 {
		return GroupPolicy{}, ErrInvalidControl
	}
	p, _, err := readStoredGroupPolicy(ctx, s.db, groupID)
	return p, err
}

func verifySchedulingGroup(ctx context.Context, q sqlQueryer, groupID int64, lock bool) error {
	if groupID == 0 {
		return nil
	}
	query := "SELECT id FROM groups WHERE id=$1 AND deleted_at IS NULL"
	if lock {
		query += " FOR SHARE"
	}
	var id int64
	err := q.QueryRowContext(ctx, query, groupID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSchedulingGroupNotFound
	}
	return err
}

func (s *PostgresStore) groupPolicyMembers(ctx context.Context, q sqlQueryer, groupID int64, lock bool) ([]AccountRule, error) {
	query := "SELECT a.id,a.priority FROM accounts a"
	args := []any{}
	if groupID > 0 {
		query += " JOIN account_groups ag ON ag.account_id=a.id WHERE ag.group_id=$1 AND a.deleted_at IS NULL"
		args = append(args, groupID)
	} else {
		query += " WHERE a.deleted_at IS NULL"
		if !s.defaultGroupIncludesAll {
			query += " AND NOT EXISTS (SELECT 1 FROM account_groups ag WHERE ag.account_id=a.id)"
		}
	}
	query += " ORDER BY a.id"
	if lock {
		if groupID > 0 {
			query += " FOR SHARE OF a,ag"
		} else {
			query += " FOR SHARE OF a"
		}
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []AccountRule{}
	for rows.Next() {
		var id int64
		var priority int
		if err = rows.Scan(&id, &priority); err != nil {
			return nil, err
		}
		result = append(result, AccountRule{AccountID: id, Priority: &priority, Weight: 1})
	}
	return result, rows.Err()
}

// Removed accounts cannot appear in the effective projection. Newly attached
// accounts immediately inherit their existing account priority and weight one.
func projectGroupPolicy(p GroupPolicy, members []AccountRule) GroupPolicy {
	overrides := make(map[int64]AccountRule, len(p.Accounts))
	for _, a := range p.Accounts {
		overrides[a.AccountID] = a
	}
	p.Accounts = make([]AccountRule, 0, len(members))
	for _, a := range members {
		if override, ok := overrides[a.AccountID]; ok {
			a = override
		}
		a.FillOrder = 0
		p.Accounts = append(p.Accounts, a)
	}
	sort.Slice(p.Accounts, func(i, j int) bool {
		if *p.Accounts[i].Priority != *p.Accounts[j].Priority {
			return *p.Accounts[i].Priority < *p.Accounts[j].Priority
		}
		return p.Accounts[i].AccountID < p.Accounts[j].AccountID
	})
	return p
}

func (s *PostgresStore) groupPolicyRecord(p GroupPolicy, configured bool) GroupPolicyRecord {
	scope := "group"
	if p.GroupID == 0 {
		scope = "ungrouped"
		if s.defaultGroupIncludesAll {
			scope = "all_accounts"
		}
	}
	return GroupPolicyRecord{GroupID: p.GroupID, Version: p.Version, Configured: configured, DefaultScope: scope, Policy: p, MigrationWarnings: []GroupPolicyWarning{}}
}

// legacyGroupWarnings reads the old table only for a migration explanation. Its
// values never initialize, overwrite or influence the new group's policy.
func legacyGroupWarnings(ctx context.Context, q sqlQueryer, groupID int64, members []AccountRule) ([]GroupPolicyWarning, error) {
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT ON (model) model,policy FROM scheduling_policies WHERE group_id IN (0,$1) AND policy<>'null'::jsonb ORDER BY model,group_id DESC", groupID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	models := []string{}
	type signature struct {
		priority int
		weight   int64
	}
	first := map[int64]signature{}
	conflicts := map[int64]bool{}
	malformed := false
	for rows.Next() {
		var model string
		var raw []byte
		if err = rows.Scan(&model, &raw); err != nil {
			return nil, err
		}
		models = append(models, model)
		var old Policy
		if json.Unmarshal(raw, &old) != nil {
			malformed = true
			continue
		}
		if !old.Enabled {
			continue
		}
		explicit := map[int64]AccountRule{}
		for _, a := range old.Accounts {
			explicit[a.AccountID] = a
		}
		for _, a := range members {
			sig := signature{priority: *a.Priority, weight: 1}
			if rule, ok := explicit[a.AccountID]; ok {
				sig.weight = rule.Weight
				if rule.Priority != nil {
					sig.priority = *rule.Priority
				}
			}
			if previous, ok := first[a.AccountID]; ok && previous != sig {
				conflicts[a.AccountID] = true
			} else if !ok {
				first[a.AccountID] = sig
			}
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	warnings := []GroupPolicyWarning{}
	if len(models) > 0 {
		warnings = append(warnings, GroupPolicyWarning{Code: "legacy_model_policies_ignored", Message: "Previous model-specific settings are retained for audit and are not used by group account scheduling.", Models: models})
	}
	if len(conflicts) > 0 {
		ids := make([]int64, 0, len(conflicts))
		for id := range conflicts {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		warnings = append(warnings, GroupPolicyWarning{Code: "legacy_model_rule_conflict", Message: "Previous models specify different priorities or weights. No model was chosen automatically; review the group account settings.", AccountIDs: ids})
	}
	if malformed {
		warnings = append(warnings, GroupPolicyWarning{Code: "legacy_policy_unreadable", Message: "A previous model policy could not be read and was not imported."})
	}
	return warnings, nil
}

func (s *PostgresStore) GetGroupPolicy(ctx context.Context, groupID int64) (GroupPolicyRecord, error) {
	if err := s.ready(); err != nil {
		return GroupPolicyRecord{}, err
	}
	if groupID < 0 {
		return GroupPolicyRecord{}, ErrInvalidControl
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return GroupPolicyRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = verifySchedulingGroup(ctx, tx, groupID, false); err != nil {
		return GroupPolicyRecord{}, err
	}
	p, configured, err := readStoredGroupPolicy(ctx, tx, groupID)
	if err != nil {
		return GroupPolicyRecord{}, err
	}
	members, err := s.groupPolicyMembers(ctx, tx, groupID, false)
	if err != nil {
		return GroupPolicyRecord{}, err
	}
	r := s.groupPolicyRecord(projectGroupPolicy(p, members), configured)
	r.MigrationWarnings, err = legacyGroupWarnings(ctx, tx, groupID, members)
	if err != nil {
		return GroupPolicyRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return GroupPolicyRecord{}, err
	}
	return r, nil
}

func (s *PostgresStore) PutGroupPolicy(ctx context.Context, p GroupPolicy, expected int64) (GroupPolicyRecord, error) {
	if err := s.ready(); err != nil {
		return GroupPolicyRecord{}, err
	}
	if expected < 0 || expected == math.MaxInt64 {
		return GroupPolicyRecord{}, ErrInvalidControl
	}
	if err := ValidateGroupPolicy(p); err != nil {
		return GroupPolicyRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GroupPolicyRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = verifySchedulingGroup(ctx, tx, p.GroupID, true); err != nil {
		return GroupPolicyRecord{}, err
	}
	members, err := s.groupPolicyMembers(ctx, tx, p.GroupID, true)
	if err != nil {
		return GroupPolicyRecord{}, err
	}
	allowed := make(map[int64]bool, len(members))
	for _, a := range members {
		allowed[a.AccountID] = true
	}
	for _, a := range p.Accounts {
		if !allowed[a.AccountID] {
			return GroupPolicyRecord{}, fmt.Errorf("%w: account %d does not belong to this group", ErrInvalidControl, a.AccountID)
		}
	}
	p.Version = expected + 1
	// Persist only explicit submitted rules. Projection adds newly created members
	// without a write; omission deliberately restores that member's defaults.
	raw, err := json.Marshal(p)
	if err != nil {
		return GroupPolicyRecord{}, err
	}
	var version int64
	if expected == 0 {
		err = tx.QueryRowContext(ctx, "INSERT INTO scheduling_group_policies(group_id,version,policy) VALUES($1,1,$2::jsonb) ON CONFLICT(group_id) DO NOTHING RETURNING version", p.GroupID, string(raw)).Scan(&version)
	} else {
		err = tx.QueryRowContext(ctx, "UPDATE scheduling_group_policies SET version=version+1,policy=$3::jsonb,updated_at=NOW() WHERE group_id=$1 AND version=$2 RETURNING version", p.GroupID, expected, string(raw)).Scan(&version)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return GroupPolicyRecord{}, ErrVersionConflict
	}
	if err != nil {
		return GroupPolicyRecord{}, err
	}
	r := s.groupPolicyRecord(projectGroupPolicy(p, members), true)
	r.Version, r.Policy.Version = version, version
	r.MigrationWarnings, err = legacyGroupWarnings(ctx, tx, p.GroupID, members)
	if err != nil {
		return GroupPolicyRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return GroupPolicyRecord{}, err
	}
	return r, nil
}
