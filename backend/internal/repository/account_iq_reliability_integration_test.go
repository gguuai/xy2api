//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestIQReliabilityMigrationBackfill(t *testing.T) {
	// Execute the shipped migration statement against temporary tables shaped like
	// pre-upgrade rows, without rewriting the already migrated integration schema.
	tx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`CREATE TEMP TABLE accounts(id bigint,iq_check jsonb,deleted_at timestamptz) ON COMMIT DROP;
CREATE TEMP TABLE account_iq_check_results(id bigint,account_id bigint,config_revision text,status text,reason text,finished_at timestamptz) ON COMMIT DROP;
INSERT INTO accounts VALUES
(1,'{"revision":"current","status":"unknown","reason":"http_503","interval_minutes":1,"daily_request_limit":96}',NULL),
(2,'{"revision":"current","status":"degraded","interval_minutes":15}',NULL),
(3,'{"revision":"current","status":"degraded","interval_minutes":15,"daily_request_limit":123}',NULL);
INSERT INTO account_iq_check_results VALUES
(1,1,'current','degraded','wrong_answer',now()),
(2,1,'current','unknown','http_503',now()),
(3,1,'old','smart','correct_answer',now()),
(4,2,'old','degraded','wrong_answer',now()),
(5,3,'current','smart','correct_answer',NULL);`)
	require.NoError(t, err)
	source, err := migrations.FS.ReadFile("245_iq_check_reliability.sql")
	require.NoError(t, err)
	statement, _, ok := strings.Cut(string(source)[strings.Index(string(source), "UPDATE accounts a SET"):], ";")
	require.True(t, ok)
	_, err = tx.Exec(statement)
	require.NoError(t, err)
	for id := 1; id <= 3; id++ {
		var raw []byte
		require.NoError(t, tx.QueryRow(`SELECT iq_check FROM accounts WHERE id=$1`, id).Scan(&raw))
		var state domain.IQCheck
		require.NoError(t, json.Unmarshal(raw, &state))
		if id == 1 {
			require.Equal(t, "degraded", state.Status)
			require.NotNil(t, state.LastValidAt)
			require.Equal(t, "http_503", state.LastRunReason)
			require.Equal(t, 1, state.IntervalMinutes)
			require.Contains(t, string(raw), `"daily_request_limit": 96`)
		} else {
			require.Equal(t, "unknown", state.Status)
			require.Nil(t, state.LastValidAt)
			require.Equal(t, 15, state.IntervalMinutes)
			if id == 2 {
				require.NotContains(t, string(raw), "daily_request_limit")
			} else {
				require.Contains(t, string(raw), `"daily_request_limit": 123`)
			}
		}
	}
}

func TestIQReliabilityPersistentRecovery(t *testing.T) {
	for _, scenario := range []string{"recover", "second_failure", "deadline", "restart_wait", "restart_reserved", "restart_sent", "config_change", "bulk_change"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
			peer := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
			a := &service.Account{Name: "iq-reliability-" + scenario, Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 15)}
			require.NoError(t, repo.Create(ctx, a))
			t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
			now := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
			start := func(at time.Time) service.IQCheckClaim {
				cs, e := repo.ClaimIQChecks(ctx, at, 2)
				require.NoError(t, e)
				require.Len(t, cs, 1)
				ok, e := repo.StartIQCheck(ctx, cs[0], at)
				require.NoError(t, e)
				require.True(t, ok)
				return cs[0]
			}
			first := start(now)
			require.NoError(t, repo.CompleteIQCheck(ctx, first, iqcheck.Grade("29"), now.Add(time.Second)))
			now = now.Add(16 * time.Minute)
			first = start(now)
			result := iqcheck.Unknown("http_503")
			after := now.Add(10 * time.Second)
			result.Diagnostic = &iqcheck.Diagnostic{RetryAfter: &after, Transport: "http"}
			require.NoError(t, repo.CompleteIQCheck(ctx, first, result, now.Add(time.Second)))
			current, e := repo.GetByID(ctx, a.ID)
			require.NoError(t, e)
			require.True(t, current.IQCheck.BlocksScheduling())
			require.Equal(t, "unknown", current.IQCheck.Status)
			require.Equal(t, "degraded", current.IQCheck.LastValidStatus)
			require.Equal(t, "retry_wait", current.IQCheck.ExecutionState)
			require.Empty(t, current.IQCheck.LeaseToken)
			require.Nil(t, current.IQCheck.StartedAt)
			records, e := repo.ListIQCheckRecords(ctx, a.ID)
			require.NoError(t, e)
			require.Len(t, records, 2)
			require.Len(t, records[0].Attempts, 1)
			require.Nil(t, records[0].FinishedAt)
			// Completing the same attempt twice cannot count, dispatch or rewrite another attempt.
			require.NoError(t, peer.CompleteIQCheck(ctx, first, iqcheck.Grade("21"), now.Add(2*time.Second)))
			cs, e := peer.ClaimIQChecks(ctx, after.Add(-time.Second), 2)
			require.NoError(t, e)
			require.Empty(t, cs)
			if scenario == "config_change" || scenario == "bulk_change" {
				model := "changed-fixture"
				settings := domain.IQCheckSettings{Model: &model}
				if scenario == "bulk_change" {
					_, e = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{IQCheck: &settings})
				} else {
					_, e = repo.ConfigureIQCheck(ctx, a.ID, settings)
				}
				require.NoError(t, e)
				current, e = repo.GetByID(ctx, a.ID)
				require.NoError(t, e)
				require.Equal(t, "unknown", current.IQCheck.Status)
				require.Nil(t, current.IQCheck.RetryAt)
				records, e = repo.ListIQCheckRecords(ctx, a.ID)
				require.NoError(t, e)
				require.Equal(t, "cancelled_by_account_change", records[0].Reason)
				require.NotNil(t, records[0].FinishedAt)
				return
			}
			if scenario == "deadline" {
				_, e = peer.ClaimIQChecks(ctx, now.Add(391*time.Second), 0)
				require.NoError(t, e)
				records, e = repo.ListIQCheckRecords(ctx, a.ID)
				require.NoError(t, e)
				require.NotNil(t, records[0].FinishedAt)
				require.Len(t, records[0].Attempts, 1)
				return
			}
			if scenario == "restart_wait" {
				repo = peer
			}
			cs, e = peer.ClaimIQChecks(ctx, after, 2)
			require.NoError(t, e)
			require.Len(t, cs, 1)
			second := cs[0]
			require.Equal(t, first.Profile, second.Profile)
			require.Equal(t, first.Revision, second.Revision)
			if scenario == "restart_reserved" {
				// An expired reservation has not spent budget; the registered attempt remains #1.
				cs, e = repo.ClaimIQChecks(ctx, after.Add(181*time.Second), 2)
				require.NoError(t, e)
				require.Len(t, cs, 1)
				second = cs[0]
				after = after.Add(181 * time.Second)
				records, e = repo.ListIQCheckRecords(ctx, a.ID)
				require.NoError(t, e)
				require.Nil(t, records[0].FinishedAt)
			}
			// Two replicas may race the same reservation; exactly one transaction can send it.
			var wg sync.WaitGroup
			started := make(chan bool, 2)
			errs := make(chan error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); ok, e := peer.StartIQCheck(ctx, second, after); started <- ok; errs <- e }()
			}
			wg.Wait()
			close(started)
			close(errs)
			for e := range errs {
				require.NoError(t, e)
			}
			count := 0
			for ok := range started {
				if ok {
					count++
				}
			}
			require.Equal(t, 1, count)
			if scenario == "restart_sent" {
				_, e = repo.ClaimIQChecks(ctx, after.Add(181*time.Second), 0)
				require.NoError(t, e)
				records, e = repo.ListIQCheckRecords(ctx, a.ID)
				require.NoError(t, e)
				require.NotNil(t, records[0].FinishedAt)
				require.Equal(t, "interrupted", records[0].Attempts[1].Reason)
				require.NoError(t, peer.CompleteIQCheck(ctx, second, iqcheck.Grade("21"), after.Add(182*time.Second)))
				current, e = repo.GetByID(ctx, a.ID)
				require.NoError(t, e)
				require.True(t, current.IQCheck.BlocksScheduling())
				return
			}
			final := iqcheck.Grade("21")
			if scenario == "second_failure" {
				final = iqcheck.Unknown("http_503")
			}
			require.NoError(t, peer.CompleteIQCheck(ctx, second, final, after.Add(time.Second)))
			current, e = repo.GetByID(ctx, a.ID)
			require.NoError(t, e)
			expectedAttempts, expectedSends := 2, 3
			if scenario == "second_failure" {
				require.NotNil(t, current.IQCheck.RetryAt)
				thirdAt := *current.IQCheck.RetryAt
				third := start(thirdAt)
				require.NoError(t, peer.CompleteIQCheck(ctx, third, final, thirdAt.Add(time.Second)))
				current, e = repo.GetByID(ctx, a.ID)
				require.NoError(t, e)
				expectedAttempts, expectedSends = 3, 4
			}
			require.Nil(t, current.IQCheck.RetryAt)
			require.Equal(t, scenario == "second_failure", current.IQCheck.BlocksScheduling())
			records, e = repo.ListIQCheckRecords(ctx, a.ID)
			require.NoError(t, e)
			require.Len(t, records, 2)
			require.Len(t, records[0].Attempts, expectedAttempts)
			require.NotNil(t, records[0].FinishedAt)
			require.Equal(t, "http_503", records[0].Attempts[0].Reason)
			require.Equal(t, final.Reason, records[0].Attempts[1].Reason)
			var sends int
			require.NoError(t, integrationDB.QueryRow(`SELECT sum(count) FROM account_iq_check_metrics_hourly WHERE account_id=$1 AND event='sent'`, a.ID).Scan(&sends))
			require.Equal(t, expectedSends, sends)
		})
	}
}
