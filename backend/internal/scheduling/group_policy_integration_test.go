package scheduling

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func isolatedGroupPolicyStore(t *testing.T) (*PostgresStore, *sql.DB) {
	t.Helper()
	s, db := isolatedControlStore(t)
	_, err := db.Exec(`ALTER TABLE accounts ADD COLUMN priority INTEGER NOT NULL DEFAULT 50;
 CREATE TABLE groups(id BIGINT PRIMARY KEY,deleted_at TIMESTAMPTZ);
 CREATE TABLE account_groups(account_id BIGINT NOT NULL REFERENCES accounts(id),group_id BIGINT NOT NULL REFERENCES groups(id),PRIMARY KEY(account_id,group_id));
 INSERT INTO groups(id) VALUES(10),(20);
 INSERT INTO account_groups(account_id,group_id) VALUES(1,10),(1,20),(2,10);`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/262_group_account_scheduling.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	return s, db
}

func TestGroupPolicyPostgresMembershipAndCAS(t *testing.T) {
	s, db := isolatedGroupPolicyStore(t)
	ctx := context.Background()
	got, err := s.GetGroupPolicy(ctx, 10)
	require.NoError(t, err)
	require.False(t, got.Configured)
	require.Zero(t, got.Version)
	require.Len(t, got.Policy.Accounts, 2)
	require.EqualValues(t, 120000, got.Policy.FirstOutputTimeoutMS)
	require.EqualValues(t, 240000, got.Policy.TotalWaitTimeoutMS)
	got.Policy.Accounts[0] = groupTestRule(1, 10, 7)
	saved, err := s.PutGroupPolicy(ctx, got.Policy, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, saved.Version)
	other, err := s.GetGroupPolicy(ctx, 20)
	require.NoError(t, err)
	require.EqualValues(t, 50, *other.Policy.Accounts[0].Priority)
	require.EqualValues(t, 1, other.Policy.Accounts[0].Weight)
	stale := saved.Policy
	stale.Accounts = []AccountRule{groupTestRule(3, 0, 1)}
	_, err = s.PutGroupPolicy(ctx, stale, 1)
	require.ErrorIs(t, err, ErrInvalidControl)
	_, err = s.PutGroupPolicy(ctx, saved.Policy, 0)
	require.ErrorIs(t, err, ErrVersionConflict)
	_, err = db.Exec("INSERT INTO account_groups(account_id,group_id) VALUES(3,10)")
	require.NoError(t, err)
	got, err = s.GetGroupPolicy(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got.Policy.Accounts, 3)
	require.EqualValues(t, 1, got.Version)
	for _, a := range got.Policy.Accounts {
		if a.AccountID == 3 {
			require.EqualValues(t, 50, *a.Priority)
			require.EqualValues(t, 1, a.Weight)
		}
	}
	_, err = db.Exec("DELETE FROM account_groups WHERE account_id=2 AND group_id=10")
	require.NoError(t, err)
	got, err = s.GetGroupPolicy(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got.Policy.Accounts, 2)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, e := s.PutGroupPolicy(ctx, got.Policy, 1); errs <- e }()
	}
	close(start)
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, ErrVersionConflict) {
			conflict++
		} else {
			require.NoError(t, e)
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, conflict)
	got, err = s.GetGroupPolicy(ctx, 10)
	require.NoError(t, err)
	require.EqualValues(t, 2, got.Version)
	_, err = s.GetGroupPolicy(ctx, 999)
	require.ErrorIs(t, err, ErrSchedulingGroupNotFound)
	_, err = s.PutGroupPolicy(ctx, DefaultGroupPolicy(999), 0)
	require.ErrorIs(t, err, ErrSchedulingGroupNotFound)
}

func TestGroupPolicyPostgresDefaultScope(t *testing.T) {
	s, db := isolatedGroupPolicyStore(t)
	ctx := context.Background()
	standard, err := s.GetGroupPolicy(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, "ungrouped", standard.DefaultScope)
	require.Len(t, standard.Policy.Accounts, 1)
	require.EqualValues(t, 3, standard.Policy.Accounts[0].AccountID)
	invalid := standard.Policy
	invalid.Accounts = []AccountRule{groupTestRule(1, 0, 1)}
	_, err = s.PutGroupPolicy(ctx, invalid, 0)
	require.ErrorIs(t, err, ErrInvalidControl)
	simple := NewPostgresStoreWithDefaultGroup(db, true)
	all, err := simple.GetGroupPolicy(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, "all_accounts", all.DefaultScope)
	require.Len(t, all.Policy.Accounts, 3)
	saved, err := simple.PutGroupPolicy(ctx, all.Policy, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, saved.Version)
	// A later standard-mode reader cannot expose grouped accounts from saved simple settings.
	standard, err = s.GetGroupPolicy(ctx, 0)
	require.NoError(t, err)
	require.Len(t, standard.Policy.Accounts, 1)
	require.EqualValues(t, 3, standard.Policy.Accounts[0].AccountID)
}

func TestGroupPolicyPostgresLegacyConflictsAreReadOnly(t *testing.T) {
	s, db := isolatedGroupPolicyStore(t)
	ctx := context.Background()
	_, err := db.Exec(`INSERT INTO scheduling_policies(group_id,model,version,policy) VALUES
 (10,'a',1,'{"enabled":true,"accounts":[{"account_id":1,"priority":1,"traffic_weight":7}]}'),
 (10,'b',1,'{"enabled":true,"accounts":[{"account_id":1,"priority":2,"traffic_weight":3}]}');`)
	require.NoError(t, err)
	got, err := s.GetGroupPolicy(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got.MigrationWarnings, 2)
	require.Equal(t, "legacy_model_rule_conflict", got.MigrationWarnings[1].Code)
	require.Equal(t, []int64{1}, got.MigrationWarnings[1].AccountIDs)
	require.EqualValues(t, 50, *got.Policy.Accounts[0].Priority)
	require.EqualValues(t, 1, got.Policy.Accounts[0].Weight)
	_, err = s.PutGroupPolicy(ctx, got.Policy, 0)
	require.NoError(t, err)
	var oldCount, oldVersion int
	require.NoError(t, db.QueryRow("SELECT count(*),sum(version) FROM scheduling_policies").Scan(&oldCount, &oldVersion))
	require.Equal(t, 2, oldCount)
	require.Equal(t, 2, oldVersion)
	hot, err := s.ReadGroupPolicy(ctx, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, hot.Version)
	require.EqualValues(t, 50, *hot.Accounts[0].Priority)
}
