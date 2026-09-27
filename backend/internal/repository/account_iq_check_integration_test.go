//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func cleanupIQTestAccount(t *testing.T, id int64) {
	t.Helper()
	_, err := integrationDB.Exec("DELETE FROM accounts WHERE id=$1", id)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1 OR payload->'account_ids' @> jsonb_build_array($1::bigint)`, id)
	require.NoError(t, err)
}

func TestIQCheckRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	cache := &schedulerCacheRecorder{}
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, cache)
	a := &service.Account{Name: "iq-lifecycle", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "fixture"}, IQCheckSettings: iqTestSettingsPtr(true, 15)}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	now := time.Now().UTC().Add(time.Second)
	run := func(answer string) {
		claims, err := repo.ClaimIQChecks(ctx, now, 10)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		old, err := repo.ListIQCheckRecords(ctx, a.ID)
		require.NoError(t, err)
		started, err := repo.StartIQCheck(ctx, claims[0], now)
		require.NoError(t, err)
		require.True(t, started)
		records, err := repo.ListIQCheckRecords(ctx, a.ID)
		require.NoError(t, err)
		require.Len(t, records, min(3, len(old)+1))
		result := iqcheck.Grade(answer)
		result.Diagnostic = &iqcheck.Diagnostic{ParserVersion: iqcheck.ParserVersion, Stage: "grade", RequestID: "fixture"}
		require.NoError(t, repo.CompleteIQCheck(ctx, claims[0], result, now.Add(time.Second)))
		now = now.Add(16 * time.Minute)
	}
	run("21")
	run("29")
	fresh, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, fresh.IsSchedulable())
	require.True(t, fresh.Schedulable)
	require.False(t, cache.accounts[a.ID].IsSchedulable())
	require.True(t, buildSchedulerMetadataAccount(*fresh).IQCheck.BlocksScheduling())
	candidates, err := repo.ListSchedulable(ctx)
	require.NoError(t, err)
	for _, c := range candidates {
		require.NotEqual(t, a.ID, c.ID)
	}
	items, page, err := repo.ListWithFilters(service.WithIQStatusFilter(ctx, "degraded"), pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "", "", 0, "")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.EqualValues(t, 1, page.Total)
	run("答案是21，因为解释可以省略。")
	records, err := repo.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, records, 3)
	require.Equal(t, "29", records[1].Answer)
	require.Equal(t, "21", records[2].Answer)
	run("21")
	records, err = repo.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, records, 3)
	require.Equal(t, "29", records[2].Answer)
	var stored int
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM account_iq_check_results WHERE account_id=$1", a.ID).Scan(&stored))
	require.Equal(t, 3, stored)
	require.NotNil(t, records[1].Diagnostic)
	_, err = integrationDB.Exec("UPDATE account_iq_check_results SET diagnostic=$1::jsonb WHERE account_id=$2", `{"oversize":"`+strings.Repeat("x", 4096)+`"}`, a.ID)
	require.Error(t, err)
	_, err = integrationDB.Exec("UPDATE account_iq_check_results SET diagnostic=NULL WHERE account_id=$1", a.ID)
	require.NoError(t, err)
	records, err = repo.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, err)
	require.Nil(t, records[0].Diagnostic)
	require.NoError(t, repo.SetSchedulable(ctx, a.ID, false))
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, fresh.IsSchedulable())
	require.NoError(t, repo.SetSchedulable(ctx, a.ID, true))
	claims, err := repo.ClaimIQChecks(ctx, now, 10)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	started, err := repo.StartIQCheck(ctx, claims[0], now)
	require.NoError(t, err)
	require.True(t, started)
	_, err = repo.ConfigureIQCheck(ctx, a.ID, iqTestSettings(false, 15))
	require.NoError(t, err)
	_, err = repo.ConfigureIQCheck(ctx, a.ID, iqTestSettings(true, 15))
	require.NoError(t, err)
	overlap, err := repo.ClaimIQChecks(ctx, now.Add(time.Second), 10)
	require.NoError(t, err)
	require.Empty(t, overlap)
	require.NoError(t, repo.CompleteIQCheck(ctx, claims[0], iqcheck.Grade("29"), now.Add(time.Second)))
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", fresh.IQCheck.Status)
	revision := fresh.IQCheck.Revision
	require.NoError(t, repo.UpdateCredentials(ctx, a.ID, map[string]any{"access_token": "refresh"}))
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, revision, fresh.IQCheck.Revision)
	_, err = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{IQCheck: iqTestSettingsPtr(false, 30)})
	require.NoError(t, err)
	require.ErrorIs(t, repo.QueueIQCheck(ctx, a.ID), service.ErrIQCheckDisabled)
	require.NoError(t, repo.Delete(ctx, a.ID))
	var count int
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM account_iq_check_results WHERE account_id=$1", a.ID).Scan(&count))
	require.Zero(t, count)
}

func TestIQCheckClaimsAcrossReplicasAndRestart(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	ids := []int64{}
	t.Cleanup(func() {
		for _, id := range ids {
			cleanupIQTestAccount(t, id)
		}
	})
	for i := 0; i < 12; i++ {
		a := &service.Account{Name: fmt.Sprintf("iq-lease-%d", i), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 15)}
		require.NoError(t, repo.Create(ctx, a))
		ids = append(ids, a.ID)
	}
	now := time.Now().Add(time.Second)
	var wg sync.WaitGroup
	results := make(chan []service.IQCheckClaim, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replica := newAccountRepositoryWithSQL(client, integrationDB, nil)
			claims, err := replica.ClaimIQChecks(ctx, now, 10)
			results <- claims
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	seen := map[int64]bool{}
	var old service.IQCheckClaim
	for claims := range results {
		for _, claim := range claims {
			require.False(t, seen[claim.AccountID])
			seen[claim.AccountID] = true
			old = claim
		}
	}
	require.Len(t, seen, 10)
	restarted := newAccountRepositoryWithSQL(client, integrationDB, nil)
	claims, err := restarted.ClaimIQChecks(ctx, now.Add(181*time.Second), 10)
	require.NoError(t, err)
	require.Len(t, claims, 10)
	require.NoError(t, restarted.CompleteIQCheck(ctx, old, iqcheck.Grade("29"), now.Add(182*time.Second)))
	fresh, err := repo.GetByID(ctx, old.AccountID)
	require.NoError(t, err)
	require.Equal(t, "unknown", fresh.IQCheck.Status)
	for _, claim := range claims {
		started, err := restarted.StartIQCheck(ctx, claim, now.Add(181*time.Second))
		require.NoError(t, err)
		require.True(t, started)
		require.NoError(t, restarted.CompleteIQCheck(ctx, claim, iqcheck.Unknown("request_failed"), now.Add(182*time.Second)))
	}
}

func iqTestSettings(enabled bool, interval int) domain.IQCheckSettings {
	return domain.IQCheckSettings{Enabled: &enabled, IntervalMinutes: &interval}
}
func iqTestSettingsPtr(enabled bool, interval int) *domain.IQCheckSettings {
	s := iqTestSettings(enabled, interval)
	return &s
}

func TestIQCheckProfilesAndIntervals(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := &service.Account{Name: "iq-profile", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "fixture"}, Extra: map[string]any{"openai_responses_mode": "force_chat_completions"}, IQCheckSettings: iqTestSettingsPtr(true, 15)}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	now := time.Now().UTC().Add(time.Second)
	start := func() service.IQCheckClaim {
		cs, e := repo.ClaimIQChecks(ctx, now, 10)
		require.NoError(t, e)
		require.Len(t, cs, 1)
		ok, e := repo.StartIQCheck(ctx, cs[0], now)
		require.NoError(t, e)
		require.True(t, ok)
		return cs[0]
	}
	c := start()
	require.Equal(t, "chat_completions", c.Protocol)
	n := 30
	_, err := repo.ConfigureIQCheck(ctx, a.ID, domain.IQCheckSettings{IntervalMinutes: &n})
	require.NoError(t, err)
	require.NoError(t, repo.CompleteIQCheck(ctx, c, iqcheck.Grade("29"), now.Add(time.Second)))
	fresh, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "degraded", fresh.IQCheck.Status)
	revision := fresh.IQCheck.Revision
	n = 45
	_, err = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{IQCheck: &domain.IQCheckSettings{IntervalMinutes: &n}})
	require.NoError(t, err)
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, revision, fresh.IQCheck.Revision)
	require.Equal(t, "degraded", fresh.IQCheck.Status)
	now = now.Add(time.Hour)
	c = start()
	model, effort, mode := "custom/model", "upstream_default", "strict"
	_, err = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{IQCheck: &domain.IQCheckSettings{Model: &model, ReasoningEffort: &effort, OutputMode: &mode}})
	require.NoError(t, err)
	require.NoError(t, repo.CompleteIQCheck(ctx, c, iqcheck.Grade("29"), now.Add(time.Second)))
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", fresh.IQCheck.Status)
	now = now.Add(time.Hour)
	c = start()
	records, err := repo.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, records, 3)
	require.Equal(t, model, records[0].Model)
	require.Equal(t, effort, records[0].Effort)
	require.Equal(t, mode, records[0].OutputMode)
	require.NoError(t, repo.CompleteIQCheck(ctx, c, iqcheck.Grade("21"), now.Add(time.Second)))
	require.NoError(t, repo.UpdateExtra(ctx, a.ID, map[string]any{"openai_responses_mode": "force_responses"}))
	now = now.Add(time.Hour)
	c = start()
	require.Equal(t, "responses", c.Protocol)
	_, err = integrationDB.Exec(`UPDATE accounts SET iq_check=iq_check||'{"status":"degraded"}'::jsonb WHERE id=$1`, a.ID)
	require.NoError(t, err)
	cs, err := repo.ClaimIQChecks(ctx, now.Add(4*time.Minute), 0)
	require.NoError(t, err)
	require.Empty(t, cs)
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", fresh.IQCheck.Status)
	require.Equal(t, "degraded", fresh.IQCheck.LastValidStatus)
	require.Equal(t, "unknown", fresh.IQCheck.LastRunStatus)
	require.Equal(t, "interrupted", fresh.IQCheck.LastRunReason)
	require.NoError(t, repo.CompleteIQCheck(ctx, c, iqcheck.Grade("29"), now.Add(5*time.Minute)))
}
