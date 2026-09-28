//go:build unit

package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/redis/go-redis/v9" //nolint:depguard // isolated integration fixture uses real Redis.
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Each child owns a separate service, HTTP client, PG pool and Redis client.
func TestControlledGatewayProcessWorker(t *testing.T) {
	dsn := os.Getenv("CONTROLLED_PROCESS_DSN")
	if dsn == "" {
		t.Skip("parent-only subprocess entrypoint")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("SCHEDULING_TEST_REDIS_ADDR"), DB: 15})
	defer func() { _ = rdb.Close() }()
	accounts := []*Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 10, Status: StatusActive, Schedulable: true, GroupIDs: []int64{7}}, {ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 10, Status: StatusActive, Schedulable: true, GroupIDs: []int64{7}}}
	repo := &controlledIntegrationRepo{db: db, accounts: accounts}
	service := NewControlledSchedulingService(db, rdb, repo, nil)
	parent, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	ctx := NewControlledRequestContext(parent, "responses")
	group := int64(7)
	r, on, err := service.loadPolicy(ctx, &group, "test-model", "")
	require.NoError(t, err)
	require.True(t, on)
	defer r.Close()
	id, err := strconv.ParseInt(os.Getenv("CONTROLLED_PROCESS_ACCOUNT"), 10, 64)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, "POST", os.Getenv("CONTROLLED_PROCESS_URL"), strings.NewReader("{\"model\":\"test-model\",\"stream\":true}"))
	require.NoError(t, err)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := service.roundTrip(req, id, 10, client.Do)
	if os.Getenv("CONTROLLED_PROCESS_BLOCKED") == "1" {
		var failover *UpstreamFailoverError
		require.True(t, errors.Is(err, scheduling.ErrFailureDomainBlocked) || (errors.As(err, &failover) && failover.PreDispatchSelectionInvalidated), "unexpected error: %v", err)
		require.Zero(t, r.Ledger.Snapshot().Attempts)
		t.Logf("process=%d account=%d blocked=true actual_attempts=0", os.Getpid(), id)
		return
	}
	require.NoError(t, err)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Contains(t, string(raw), "response.completed")
	require.Equal(t, 1, r.Ledger.Snapshot().Attempts)
	t.Logf("process=%d account=%d completed=true actual_attempts=1", os.Getpid(), id)
}

func TestControlledCrossProcessDomainProbeSurvivesRedisLoss(t *testing.T) {
	s, db, _, _ := controlledIntegration(t, false)
	ctx := context.Background()
	for _, id := range []int64{1, 3} {
		_, err := s.Store.PutFailureDomains(ctx, scheduling.AccountFailureDomains{AccountID: id, QuotaPoolID: "org-fixture"}, 7)
		require.NoError(t, err)
	}
	frozen, err := s.Store.FreezeFailureAdmission(ctx, 1, "test-model")
	require.NoError(t, err)
	seed, err := s.Store.BeginDispatch(ctx, scheduling.DispatchRequest{RequestID: scheduling.NewDispatchID(), AccountID: 1, NodeID: "seed", Failure: &frozen})
	require.NoError(t, err)
	decision := scheduling.ClassifyFailure(scheduling.FailureEvidence{Status: 429, Trusted: true, SharedKind: "quota_pool", SharedPool: "org-fixture", ReplaySafe: true}, *seed.Failure, time.Now())
	require.NoError(t, s.Store.SettleAttempt(ctx, seed.TicketID, "upstream_error", false))
	require.NoError(t, s.Store.ApplyFailureFeedback(ctx, seed.TicketID, decision, false))
	_, err = db.Exec("UPDATE scheduling_failure_gates SET ready_after=NOW()-INTERVAL '1 second' WHERE gate_key=$1", decision.Key)
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: {\"type\":\"response.completed\"}\n\n")
	}))
	defer server.Close()
	var schema string
	require.NoError(t, db.QueryRow("SELECT current_schema()").Scan(&schema))
	u, err := url.Parse(os.Getenv("SCHEDULING_TEST_POSTGRES_DSN"))
	require.NoError(t, err)
	q := u.Query()
	q.Set("options", "-csearch_path="+schema)
	u.RawQuery = q.Encode()
	executable, err := os.Executable()
	require.NoError(t, err)
	makeChild := func(id int64, blocked bool) *exec.Cmd {
		cmd := exec.Command(executable, "-test.run=^TestControlledGatewayProcessWorker$", "-test.v")
		expect := "0"
		if blocked {
			expect = "1"
		}
		cmd.Env = append(os.Environ(), "CONTROLLED_PROCESS_DSN="+u.String(), "CONTROLLED_PROCESS_ACCOUNT="+strconv.FormatInt(id, 10), "CONTROLLED_PROCESS_URL="+server.URL, "CONTROLLED_PROCESS_BLOCKED="+expect)
		return cmd
	}
	type childResult struct {
		out []byte
		err error
	}
	done := make(chan childResult, 1)
	go func() { out, err := makeChild(1, false).CombinedOutput(); done <- childResult{out, err} }()
	select {
	case <-entered:
	case result := <-done:
		t.Fatalf("first process exited early: %v %s", result.err, result.out)
	case <-time.After(10 * time.Second):
		t.Fatal("first process never dispatched")
	}
	require.NoError(t, s.redis.FlushDB(ctx).Err())
	out, err := makeChild(3, true).CombinedOutput()
	require.NoError(t, err, string(out))
	require.EqualValues(t, 1, calls.Load(), "Redis loss must not launch a second remote probe")
	t.Log(string(out))
	releaseOnce.Do(func() { close(release) })
	result := <-done
	require.NoError(t, result.err, string(result.out))
	t.Log(string(result.out))
	out, err = makeChild(3, false).CombinedOutput()
	require.NoError(t, err, string(out))
	require.EqualValues(t, 2, calls.Load())
	t.Log(string(out))
	var active int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts WHERE state<>'settled'").Scan(&active))
	require.Zero(t, active)
}
