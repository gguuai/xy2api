package scheduling

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func groupTestRule(id int64, priority int, weight int64) AccountRule {
	return AccountRule{AccountID: id, Priority: &priority, Weight: weight}
}

func TestGroupPolicyValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*GroupPolicy)
	}{
		{"missing_priority", func(p *GroupPolicy) { p.Accounts[0].Priority = nil }},
		{"negative_weight", func(p *GroupPolicy) { p.Accounts[0].Weight = -1 }},
		{"excess_weight", func(p *GroupPolicy) { p.Accounts[0].Weight = 1000001 }},
		{"duplicate_account", func(p *GroupPolicy) { p.Accounts = append(p.Accounts, p.Accounts[0]) }},
		{"fill_order", func(p *GroupPolicy) { p.Accounts[0].FillOrder = 1 }},
		{"invalid_group", func(p *GroupPolicy) { p.GroupID = -1 }},
		{"too_short", func(p *GroupPolicy) { p.FirstOutputTimeoutMS = 999 }},
		{"no_total_budget", func(p *GroupPolicy) { p.TotalWaitTimeoutMS = 1 }},
		{"too_long", func(p *GroupPolicy) { p.TotalWaitTimeoutMS = 7200001 }},
		{"zero_attempts", func(p *GroupPolicy) { p.MaxAttempts = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := DefaultGroupPolicy(1)
			p.Accounts = []AccountRule{groupTestRule(1, 0, 0)}
			tc.mutate(&p)
			require.ErrorIs(t, ValidateGroupPolicy(p), ErrInvalidControl)
		})
	}
	p := DefaultGroupPolicy(0)
	p.Accounts = []AccountRule{groupTestRule(1, -1, 0), groupTestRule(2, 0, 1000000)}
	require.NoError(t, ValidateGroupPolicy(p))
}

func TestGroupPolicyProjectionFiltersMembershipAndAddsAccounts(t *testing.T) {
	p := DefaultGroupPolicy(1)
	p.Accounts = []AccountRule{groupTestRule(1, 10, 7), groupTestRule(99, 1, 100)}
	got := projectGroupPolicy(p, []AccountRule{groupTestRule(1, 50, 1), groupTestRule(2, 20, 1)})
	require.Equal(t, []AccountRule{groupTestRule(1, 10, 7), groupTestRule(2, 20, 1)}, got.Accounts)
	require.Len(t, p.Accounts, 2)
}

func TestReadGroupPolicyNeverReadsLegacyOrMembers(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	m.ExpectQuery(regexp.QuoteMeta("SELECT version,policy FROM scheduling_group_policies WHERE group_id=$1")).WithArgs(int64(8)).WillReturnError(sql.ErrNoRows)
	p, err := NewPostgresStore(db).ReadGroupPolicy(context.Background(), 8)
	require.NoError(t, err)
	require.Equal(t, DefaultGroupPolicy(8), p)
	require.NoError(t, m.ExpectationsWereMet())
}

func TestReadGroupPolicyRejectsInvalidStoredSettings(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	m.ExpectQuery("SELECT version,policy FROM scheduling_group_policies").WithArgs(int64(8)).WillReturnRows(sqlmock.NewRows([]string{"version", "policy"}).AddRow(2, `{"first_output_timeout_ms":0}`))
	_, err = NewPostgresStore(db).ReadGroupPolicy(context.Background(), 8)
	require.ErrorIs(t, err, ErrInvalidControl)
	require.NoError(t, m.ExpectationsWereMet())
}
