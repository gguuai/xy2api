package scheduling

import (
	"context"
	"database/sql"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func installOneClickAccountRevision(t *testing.T, db *sql.DB) {
	t.Helper()
	migration, err := os.ReadFile("../../migrations/266_account_monotonic_updated_at.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
}

func oneClickAccountRevision(t *testing.T, db *sql.DB, id int64) time.Time {
	t.Helper()
	var stamp time.Time
	require.NoError(t, db.QueryRow("SELECT updated_at FROM accounts WHERE id=$1", id).Scan(&stamp))
	return stamp
}

func TestOneClickAccountRevisionRejectsStaleAndEqualWriterTimestamps(t *testing.T) {
	_, db := isolatedControlStore(t)
	// A finite future fixture forces OLD+1us even when the wall clock is behind.
	original := time.Date(2200, time.January, 1, 0, 0, 0, 0, time.UTC)
	stale := original.Add(-time.Hour)
	_, err := db.Exec("UPDATE accounts SET updated_at=$1 WHERE id=1", original)
	require.NoError(t, err)
	var baseline time.Time
	var enabled bool
	// Matching stale UPDATE input demonstrates the old, unfenced behavior first.
	require.NoError(t, db.QueryRow("UPDATE accounts SET schedulable=$1,updated_at=$2 WHERE id=1 RETURNING schedulable,updated_at", false, stale).Scan(&enabled, &baseline))
	require.False(t, enabled)
	require.True(t, baseline.Equal(stale))
	require.True(t, baseline.Before(original))
	t.Logf("BASELINE off: updated_at=%s regressed=true", baseline.Format(time.RFC3339Nano))
	_, err = db.Exec("UPDATE accounts SET schedulable=TRUE,updated_at=$1 WHERE id=1", original)
	require.NoError(t, err)
	peerBefore := oneClickAccountRevision(t, db, 2)
	installOneClickAccountRevision(t, db)
	installOneClickAccountRevision(t, db)
	require.True(t, original.Equal(oneClickAccountRevision(t, db, 1)), "installation and replay must not rewrite account rows")
	previous := original
	for index, expected := range []bool{false, true, false} {
		supplied := stale
		if index == 1 {
			supplied = previous
		}
		var stamp time.Time
		require.NoError(t, db.QueryRow("UPDATE accounts SET schedulable=$1,updated_at=$2 WHERE id=1 RETURNING schedulable,updated_at", expected, supplied).Scan(&enabled, &stamp))
		require.Equal(t, expected, enabled)
		require.Equal(t, previous.Add(time.Microsecond), stamp)
		t.Logf("MODIFIED step=%d schedulable=%t updated_at=%s strictly_increased=true", index, enabled, stamp.Format(time.RFC3339Nano))
		previous = stamp
	}
	require.True(t, peerBefore.Equal(oneClickAccountRevision(t, db, 2)), "updates cannot revise another account")
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	for index := 0; index < 3; index++ {
		var stamp time.Time
		require.NoError(t, tx.QueryRow("UPDATE accounts SET updated_at=NOW() WHERE id=1 RETURNING updated_at").Scan(&stamp))
		require.Equal(t, previous.Add(time.Microsecond), stamp, "NOW() is constant within the transaction but revisions must advance")
		previous = stamp
	}
	require.NoError(t, tx.Commit())
	require.Equal(t, previous, oneClickAccountRevision(t, db, 1))
}

func TestOneClickAccountRevisionSerializesConcurrentUpdates(t *testing.T) {
	_, db := isolatedControlStore(t)
	original := time.Date(2200, time.January, 1, 0, 0, 0, 0, time.UTC)
	_, err := db.Exec("UPDATE accounts SET updated_at=$1 WHERE id=1", original)
	require.NoError(t, err)
	installOneClickAccountRevision(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const writers = 24
	type updateResult struct {
		stamp time.Time
		err   error
	}
	results := make(chan updateResult, writers)
	ready := make(chan struct{})
	var wg sync.WaitGroup
	for index := 0; index < writers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			var value updateResult
			value.err = db.QueryRowContext(ctx, "UPDATE accounts SET schedulable=NOT schedulable,extra=jsonb_set(extra,'{revision_writes}',to_jsonb(COALESCE((extra->>'revision_writes')::integer,0)+1)),updated_at=$1 WHERE id=1 RETURNING updated_at", original.Add(-time.Hour)).Scan(&value.stamp)
			results <- value
		}()
	}
	close(ready)
	wg.Wait()
	close(results)
	versions := make([]time.Time, 0, writers)
	for value := range results {
		require.NoError(t, value.err)
		versions = append(versions, value.stamp)
	}
	sort.Slice(versions, func(left, right int) bool { return versions[left].Before(versions[right]) })
	for index, stamp := range versions {
		require.Equal(t, original.Add(time.Duration(index+1)*time.Microsecond), stamp, "each row-lock winner must receive a distinct increasing revision")
	}
	var enabled bool
	var writes int
	var final time.Time
	require.NoError(t, db.QueryRow("SELECT schedulable,(extra->>'revision_writes')::integer,updated_at FROM accounts WHERE id=1").Scan(&enabled, &writes, &final))
	require.Equal(t, writers, writes, "concurrent ordinary field updates must not be lost")
	require.True(t, enabled, "an even number of atomic flips returns to the initial value")
	require.Equal(t, versions[len(versions)-1], final)
	t.Logf("concurrent UPDATE writers=%d distinct_revisions=%d first=%s final=%s fields_preserved=true", writers, len(versions), versions[0].Format(time.RFC3339Nano), final.Format(time.RFC3339Nano))
}

func TestOneClickAccountRevisionPreservesFieldsArchiveAndMode(t *testing.T) {
	store, db := isolatedControlStore(t)
	// Install the real existing soft-delete trigger to verify the two coexist.
	oldMigration, err := os.ReadFile("../../migrations/240_account_iq_check.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(oldMigration))
	require.NoError(t, err)
	before := oneClickAccountRevision(t, db, 1)
	peerBefore := oneClickAccountRevision(t, db, 2)
	modeBefore, err := store.GetSchedulingMode(context.Background())
	require.NoError(t, err)
	installOneClickAccountRevision(t, db)
	credentials, extra := "{\"api_key\":\"ordinary-edit\"}", "{\"custom_flag\":true}"
	var stamp time.Time
	require.NoError(t, db.QueryRow("UPDATE accounts SET credentials=$1,extra=$2,status='error' WHERE id=1 RETURNING updated_at", credentials, extra).Scan(&stamp))
	require.True(t, stamp.After(before), "writers omitting updated_at must still receive a newer revision")
	var gotCredentials, gotExtra, status string
	var enabled bool
	require.NoError(t, db.QueryRow("SELECT credentials::text,extra::text,status,schedulable FROM accounts WHERE id=1").Scan(&gotCredentials, &gotExtra, &status, &enabled))
	require.JSONEq(t, credentials, gotCredentials)
	require.JSONEq(t, extra, gotExtra)
	require.Equal(t, "error", status)
	require.True(t, enabled, "a field edit must not rewrite the scheduling switch")
	_, err = db.Exec("INSERT INTO account_iq_check_results(account_id,lease_token,started_at) VALUES(1,'revision-archive-fixture',NOW())")
	require.NoError(t, err)
	var archived, archivedRevision time.Time
	require.NoError(t, db.QueryRow("UPDATE accounts SET deleted_at=clock_timestamp() WHERE id=1 RETURNING deleted_at,updated_at").Scan(&archived, &archivedRevision))
	require.False(t, archived.IsZero())
	require.True(t, archivedRevision.After(stamp))
	var remaining int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM account_iq_check_results WHERE account_id=1").Scan(&remaining))
	require.Zero(t, remaining, "the established soft-delete cleanup trigger must still execute")
	var iq string
	require.NoError(t, db.QueryRow("SELECT iq_check::text FROM accounts WHERE id=1").Scan(&iq))
	require.JSONEq(t, "{\"enabled\":false,\"interval_minutes\":15,\"status\":\"unknown\"}", iq)
	require.True(t, peerBefore.Equal(oneClickAccountRevision(t, db, 2)))
	modeAfter, err := store.GetSchedulingMode(context.Background())
	require.NoError(t, err)
	require.Equal(t, modeBefore, modeAfter, "timestamp ordering must not change the active scheduling algorithm")
	t.Log("ordinary field edit and real IQ soft-delete cleanup preserved; peer timestamp and scheduling mode unchanged")
}
