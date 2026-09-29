//go:build unit

package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// These tests require disposable, real PostgreSQL and Redis. No upstream model,
// account credential or production datastore is used.
type controlledIntegrationRepo struct {
	AccountRepository
	db            *sql.DB
	accounts      []*Account
	shortTimeouts bool
}

func (r *controlledIntegrationRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	for _, a := range r.accounts {
		if a.ID == id {
			copy := *a
			err := r.db.QueryRowContext(ctx, "SELECT schedulable FROM accounts WHERE id=$1", id).Scan(&copy.Schedulable)
			return &copy, err
		}
	}
	return nil, nil
}
func (r *controlledIntegrationRepo) ListByGroup(ctx context.Context, group int64) ([]Account, error) {
	out := []Account{}
	for _, a := range r.accounts {
		v, e := r.GetByID(ctx, a.ID)
		if e != nil {
			return nil, e
		}
		out = append(out, *v)
	}
	return out, nil
}
func controlledIntegration(t *testing.T, profile bool) (*ControlledSchedulingService, *sql.DB, scheduling.Policy, []*Account) {
	t.Helper()
	dsn := os.Getenv("SCHEDULING_TEST_POSTGRES_DSN")
	addr := os.Getenv("SCHEDULING_TEST_REDIS_ADDR")
	if dsn == "" || addr == "" {
		t.Skip("disposable PostgreSQL and Redis not configured")
	}
	admin, e := sql.Open("postgres", dsn)
	require.NoError(t, e)
	require.NoError(t, admin.Ping())
	schema := "e2e_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, e = admin.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, e)
	u, e := url.Parse(dsn)
	require.NoError(t, e)
	q := u.Query()
	q.Set("options", "-csearch_path="+schema)
	u.RawQuery = q.Encode()
	db, e := sql.Open("postgres", u.String())
	require.NoError(t, e)
	db.SetMaxOpenConns(12)
	t.Cleanup(func() { db.Close(); _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE"); admin.Close() })
	_, e = db.Exec("CREATE TABLE scheduler_outbox(id BIGSERIAL PRIMARY KEY,event_type TEXT NOT NULL,account_id BIGINT); CREATE TABLE accounts(id BIGINT PRIMARY KEY,parent_account_id BIGINT REFERENCES accounts(id),credentials JSONB NOT NULL DEFAULT '{}'::jsonb,extra JSONB NOT NULL DEFAULT '{}'::jsonb,priority INTEGER NOT NULL DEFAULT 0,platform TEXT NOT NULL DEFAULT 'openai',type TEXT NOT NULL DEFAULT 'oauth',schedulable BOOLEAN NOT NULL DEFAULT TRUE,status TEXT NOT NULL DEFAULT 'active',deleted_at TIMESTAMPTZ,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()); INSERT INTO accounts(id) VALUES(1),(3); INSERT INTO accounts(id,parent_account_id) VALUES(2,1); UPDATE accounts SET priority=1 WHERE id=3; CREATE TABLE groups(id BIGINT PRIMARY KEY,deleted_at TIMESTAMPTZ); INSERT INTO groups(id) VALUES(7); CREATE TABLE account_groups(account_id BIGINT REFERENCES accounts(id),group_id BIGINT REFERENCES groups(id),priority INTEGER NOT NULL DEFAULT 50,PRIMARY KEY(account_id,group_id)); INSERT INTO account_groups(account_id,group_id) VALUES(1,7),(2,7),(3,7)")
	require.NoError(t, e)
	migration, e := os.ReadFile("../../migrations/257_explicit_account_scheduling.sql")
	require.NoError(t, e)
	_, e = db.Exec(string(migration))
	require.NoError(t, e)
	for _, name := range []string{"259_scheduling_failure_domains.sql", "260_scheduling_profile_health_revision.sql", "261_scheduling_failure_feedback_queue.sql", "262_group_account_scheduling.sql", "263_dual_scheduling_mode.sql", "264_scheduling_bounded_unknown_recovery.sql"} {
		migration, e = os.ReadFile("../../migrations/" + name)
		require.NoError(t, e)
		_, e = db.Exec(string(migration))
		require.NoError(t, e)
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	require.NoError(t, rdb.FlushDB(context.Background()).Err())
	t.Cleanup(func() { rdb.Close() })
	family := int64(1)
	accounts := []*Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Priority: 0, Concurrency: 10, Status: StatusActive, Schedulable: true, GroupIDs: []int64{7}}, {ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &family, Priority: 0, Concurrency: 10, Status: StatusActive, Schedulable: true, GroupIDs: []int64{7}}, {ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Priority: 1, Concurrency: 10, Status: StatusActive, Schedulable: true, GroupIDs: []int64{7}}}
	repo := &controlledIntegrationRepo{db: db, accounts: accounts, shortTimeouts: profile}
	service := NewControlledSchedulingService(db, rdb, repo, nil)
	group := scheduling.DefaultGroupPolicy(7)
	zero, one := 0, 1
	group.Accounts = []scheduling.AccountRule{{AccountID: 1, Priority: &zero, Weight: 7}, {AccountID: 2, Priority: &zero, Weight: 3}, {AccountID: 3, Priority: &one, Weight: 1}}
	rec, e := service.Store.PutGroupPolicy(context.Background(), group, 0)
	require.NoError(t, e)
	p, _ := accountPoolPolicy(rec.Policy, "test-model")
	return service, db, p, accounts
}
func controlledIntegrationRequest(t *testing.T, s *ControlledSchedulingService) (context.Context, *ControlledRequest) {
	t.Helper()
	ctx := NewControlledRequestContext(context.Background(), "responses")
	group := int64(7)
	r, on, e := s.loadPolicy(ctx, &group, "test-model", "")
	require.NoError(t, e)
	require.True(t, on)
	// Fast transport tests override only this in-memory request ledger. Persisted
	// group settings retain production defaults and never contain latency profiles.
	if repo, ok := s.accounts.(*controlledIntegrationRepo); ok && repo.shortTimeouts {
		r.Profile = scheduling.LatencyProfile{Name: scheduling.AccountPoolProfileName, AttemptTimeoutMS: 150, TotalBudgetMS: 1000, MinAttemptWindowMS: 50}
		r.Ledger = scheduling.NewAttemptLedger(r.Policy.Retry, r.Profile, r.Started, r.ClientDeadline)
	}
	t.Cleanup(r.Close)
	return ctx, r
}
func controlledPick(t *testing.T, s *ControlledSchedulingService, ctx context.Context, r *ControlledRequest, a []*Account) *Account {
	t.Helper()
	picked, e := s.selectAccount(ctx, r, a, func(a *Account) (bool, string) { return a.Schedulable, "test" }, nil, false)
	require.NoError(t, e)
	return picked.Account
}
func controlledHTTP(t *testing.T, s *ControlledSchedulingService, ctx context.Context, account int64, handler http.HandlerFunc) (*http.Response, error) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	req, e := http.NewRequestWithContext(ctx, "POST", server.URL, strings.NewReader("{\"stream\":true}"))
	require.NoError(t, e)
	return s.roundTrip(req, account, 10, server.Client().Do)
}
func controlledConsume(t *testing.T, response *http.Response) {
	t.Helper()
	_, e := io.Copy(io.Discard, response.Body)
	require.NoError(t, e)
	require.NoError(t, response.Body.Close())
}

func TestControlledRealStoresAndHTTP(t *testing.T) {
	t.Run("strict_priority_shared_7_to_3", func(t *testing.T) {
		s, _, _, accounts := controlledIntegration(t, false)
		counts := map[int64]int{}
		other := &ControlledSchedulingService{Store: s.Store, Runtime: s.Runtime, redis: s.redis, accounts: s.accounts, node: "second-instance"}
		for i := 0; i < 20; i++ {
			node := s
			if i%2 != 0 {
				node = other
			}
			ctx, r := controlledIntegrationRequest(t, node)
			a := controlledPick(t, node, ctx, r, accounts)
			counts[a.ID]++
			resp, e := controlledHTTP(t, node, ctx, a.ID, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, "{\"ok\":true}")
			})
			require.NoError(t, e)
			controlledConsume(t, resp)
			r.Close()
		}
		require.Equal(t, map[int64]int{1: 14, 2: 6}, counts)
	})
	t.Run("three_actual_attempts_same_tier_first", func(t *testing.T) {
		s, _, _, accounts := controlledIntegration(t, false)
		ctx, r := controlledIntegrationRequest(t, s)
		var calls atomic.Int32
		ids := []int64{}
		for i := 0; i < 3; i++ {
			a := controlledPick(t, s, ctx, r, accounts)
			ids = append(ids, a.ID)
			resp, e := controlledHTTP(t, s, ctx, a.ID, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(503)
				fmt.Fprint(w, "unavailable")
			})
			require.NoError(t, e)
			controlledConsume(t, resp)
		}
		require.Equal(t, []int64{1, 2, 3}, ids)
		require.EqualValues(t, 3, calls.Load())
		require.Equal(t, 3, r.Ledger.Snapshot().Attempts)
		_, e := s.selectAccount(ctx, r, accounts, func(*Account) (bool, string) { return true, "" }, nil, false)
		require.Error(t, e)
	})
	t.Run("pause_wins_before_gate_no_call_or_budget", func(t *testing.T) {
		s, db, _, accounts := controlledIntegration(t, false)
		ctx, r := controlledIntegrationRequest(t, s)
		a := controlledPick(t, s, ctx, r, accounts)
		_, e := db.ExecContext(ctx, "UPDATE accounts SET schedulable=FALSE WHERE id=$1", a.ID)
		require.NoError(t, e)
		req, _ := http.NewRequestWithContext(ctx, "POST", "http://unused", nil)
		calls := 0
		_, e = s.roundTrip(req, a.ID, 10, func(*http.Request) (*http.Response, error) { calls++; return nil, nil })
		var invalidated *UpstreamFailoverError
		require.ErrorAs(t, e, &invalidated)
		require.True(t, invalidated.PreDispatchSelectionInvalidated)
		require.Zero(t, calls)
		require.Zero(t, r.Ledger.Snapshot().Attempts)
	})
	t.Run("gate_wins_pause_drains_and_settles_once", func(t *testing.T) {
		s, db, _, accounts := controlledIntegration(t, false)
		ctx, r := controlledIntegrationRequest(t, s)
		a := controlledPick(t, s, ctx, r, accounts)
		d, e := s.beginDispatch(ctx, a.ID, 10)
		require.NoError(t, e)
		require.NoError(t, d.MarkSent())
		_, e = db.ExecContext(ctx, "UPDATE accounts SET schedulable=FALSE WHERE id=$1", a.ID)
		require.NoError(t, e)
		require.NoError(t, d.Context().Err())
		d.ObserveFrame([]byte("{\"type\":\"response.completed\"}"))
		d.Finish("completed", true, nil)
		d.Finish("duplicate", false, errors.New("duplicate"))
		var outcome string
		require.NoError(t, db.QueryRow("SELECT outcome FROM scheduling_attempts WHERE ticket_id=$1", d.ticket.TicketID).Scan(&outcome))
		require.Equal(t, "completed", outcome)
	})
	t.Run("force_then_resume_still_cancels_old_ticket_on_other_node", func(t *testing.T) {
		s, _, _, accounts := controlledIntegration(t, false)
		ctx, r := controlledIntegrationRequest(t, s)
		a := controlledPick(t, s, ctx, r, accounts)
		d, e := s.beginDispatch(ctx, a.ID, 10)
		require.NoError(t, e)
		require.NoError(t, d.MarkSent())
		s.Store.SetCancelHook(nil)
		_, e = s.Store.Control(ctx, scheduling.ControlCommand{AccountID: a.ID, Action: "force_stop"})
		require.NoError(t, e)
		_, e = s.Store.Control(ctx, scheduling.ControlCommand{AccountID: a.ID, Action: "resume"})
		require.NoError(t, e)
		select {
		case <-d.Context().Done():
		case <-time.After(3 * time.Second):
			t.Fatal("force cancellation lost after resume")
		}
		d.Finish("cancelled", false, d.Context().Err())
	})
	t.Run("headers_timeout_counts_and_keeps_unknown_capacity", func(t *testing.T) {
		s, db, _, accounts := controlledIntegration(t, true)
		ctx, r := controlledIntegrationRequest(t, s)
		a := controlledPick(t, s, ctx, r, accounts)
		start := time.Now()
		_, e := controlledHTTP(t, s, ctx, a.ID, func(w http.ResponseWriter, request *http.Request) {
			_, _ = io.Copy(io.Discard, request.Body)
			select {
			case <-request.Context().Done():
			case <-time.After(2 * time.Second):
			}
		})
		require.ErrorIs(t, e, context.DeadlineExceeded)
		require.Less(t, time.Since(start), time.Second)
		require.True(t, r.Ledger.Snapshot().TimeoutSeen)
		var state string
		require.NoError(t, db.QueryRow("SELECT state FROM scheduling_attempts WHERE request_id=$1", r.ID).Scan(&state))
		require.Equal(t, "unknown", state)
		a2 := controlledPick(t, s, ctx, r, accounts)
		require.NotEqual(t, a.ID, a2.ID)
		resp, e := controlledHTTP(t, s, ctx, a2.ID, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) })
		require.NoError(t, e)
		controlledConsume(t, resp)
		a3 := controlledPick(t, s, ctx, r, accounts)
		require.NotEqual(t, a.ID, a3.ID)
		require.NotEqual(t, a2.ID, a3.ID)
		resp, e = controlledHTTP(t, s, ctx, a3.ID, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "{\"ok\":true}")
		})
		require.NoError(t, e)
		controlledConsume(t, resp)
		require.Equal(t, 3, r.Ledger.Snapshot().Attempts)
	})
	t.Run("semantic_reasoning_stops_first_output_timer", func(t *testing.T) {
		s, _, _, accounts := controlledIntegration(t, true)
		ctx, r := controlledIntegrationRequest(t, s)
		a := controlledPick(t, s, ctx, r, accounts)
		resp, e := controlledHTTP(t, s, ctx, a.ID, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.created\"}\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(15 * time.Millisecond)
			fmt.Fprint(w, "data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"thinking\"}\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(200 * time.Millisecond)
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\ndata: {\"type\":\"response.completed\"}\n\n")
		})
		require.NoError(t, e)
		controlledConsume(t, resp)
		require.Len(t, r.history, 1)
		require.NotNil(t, r.history[0].FirstSemanticMS)
		require.NotNil(t, r.history[0].FirstAnswerMS)
		require.Less(t, *r.history[0].FirstSemanticMS, *r.history[0].FirstAnswerMS)
		require.Equal(t, "completed", r.history[0].Outcome)
	})
	t.Run("heartbeat_only_timeout_never_commits_or_gets_ttft", func(t *testing.T) {
		s, _, _, accounts := controlledIntegration(t, true)
		ctx, r := controlledIntegrationRequest(t, s)
		a := controlledPick(t, s, ctx, r, accounts)
		_, e := controlledHTTP(t, s, ctx, a.ID, func(w http.ResponseWriter, q *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": ping\n\ndata: {\"type\":\"response.created\"}\n\n")
			w.(http.Flusher).Flush()
			_, _ = io.Copy(io.Discard, q.Body)
			select {
			case <-q.Context().Done():
			case <-time.After(2 * time.Second):
			}
		})
		require.ErrorIs(t, e, context.DeadlineExceeded)
		require.False(t, r.Ledger.Snapshot().Committed)
		require.Nil(t, r.history[0].FirstSemanticMS)
	})
	t.Run("truncated_semantic_stream_is_unknown_not_success", func(t *testing.T) {
		s, db, _, accounts := controlledIntegration(t, true)
		ctx, r := controlledIntegrationRequest(t, s)
		a := controlledPick(t, s, ctx, r, accounts)
		resp, e := controlledHTTP(t, s, ctx, a.ID, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
		})
		require.NoError(t, e)
		_, e = io.ReadAll(resp.Body)
		require.ErrorIs(t, e, io.ErrUnexpectedEOF)
		_ = resp.Body.Close()
		require.Equal(t, "stream_truncated", r.history[0].Outcome)
		var state string
		require.NoError(t, db.QueryRow("SELECT state FROM scheduling_attempts WHERE request_id=$1", r.ID).Scan(&state))
		require.Equal(t, "unknown", state)
	})

	t.Run("unknown_429_keeps_shadow_and_same_site_models_independent", func(t *testing.T) {
		s, db, _, accounts := controlledIntegration(t, false)
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.Header().Set("Retry-After", "60")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			_, _ = fmt.Fprint(w, `{"error":{"type":"rate_limit_error"}}`)
		}))
		defer server.Close()
		// All accounts target the identical site. Accounts 1 and 2 additionally share
		// actual credentials through the proven shadow relation; neither proves quota.
		for _, a := range accounts {
			a.Credentials = map[string]any{"base_url": server.URL}
		}
		_, e := db.Exec("UPDATE accounts SET credentials=jsonb_build_object('base_url',$1::text)", server.URL)
		require.NoError(t, e)
		ctx, r := controlledIntegrationRequest(t, s)
		for _, expected := range []int64{1, 2, 3} {
			a := controlledPick(t, s, ctx, r, accounts)
			require.Equal(t, expected, a.ID)
			req, e := http.NewRequestWithContext(ctx, "POST", server.URL, strings.NewReader(`{"model":"test-model","stream":true}`))
			require.NoError(t, e)
			resp, e := s.roundTrip(req, a.ID, 10, server.Client().Do)
			require.NoError(t, e)
			controlledConsume(t, resp)
			require.False(t, r.Ledger.Snapshot().BlockedFailureDomains["1"])
			if expected == 1 {
				local, e := s.Store.InspectFailureDomains(ctx, 1, "test-model")
				require.NoError(t, e)
				require.False(t, local.Eligible)
				otherModel, e := s.Store.InspectFailureDomains(ctx, 1, "other-model")
				require.NoError(t, e)
				require.True(t, otherModel.Eligible)
				shadow, e := s.Store.InspectFailureDomains(ctx, 2, "test-model")
				require.NoError(t, e)
				require.True(t, shadow.Eligible)
				sameSite, e := s.Store.InspectFailureDomains(ctx, 3, "test-model")
				require.NoError(t, e)
				require.True(t, sameSite.Eligible)
			}
		}
		_, e = s.selectAccount(ctx, r, accounts, func(*Account) (bool, string) { return true, "test" }, nil, false)
		require.Error(t, e)
		require.EqualValues(t, 3, calls.Load())
		require.Equal(t, 3, r.Ledger.Snapshot().Attempts)
		var sent int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts WHERE request_id=$1 AND metrics ? 'sent_at'", r.ID).Scan(&sent))
		require.Equal(t, 3, sent)
	})
	t.Run("retired_declared_429_keeps_sibling_eligible", func(t *testing.T) {
		s, db, _, accounts := controlledIntegration(t, false)
		ctx, r := controlledIntegrationRequest(t, s)
		for _, id := range []int64{1, 2} {
			_, e := s.Store.PutFailureDomains(ctx, scheduling.AccountFailureDomains{AccountID: id, QuotaPoolID: "org-fixture"}, 7)
			require.NoError(t, e)
		}
		a := controlledPick(t, s, ctx, r, accounts)
		require.EqualValues(t, 1, a.ID)
		resp, e := controlledHTTP(t, s, ctx, a.ID, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "60")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			_, _ = fmt.Fprint(w, `{"error":{"code":"organization_rate_limit_exceeded","organization_id":"org-fixture"}}`)
		})
		require.NoError(t, e)
		controlledConsume(t, resp)
		frozen, e := s.Store.FreezeFailureAdmission(ctx, 1, "test-model")
		require.NoError(t, e)
		sharedKey := frozen.SharedKey("quota_pool", "org-fixture", "")
		require.False(t, r.Ledger.Snapshot().BlockedFailureDomains[sharedKey])
		local, e := s.Store.InspectFailureDomains(ctx, 1, "test-model")
		require.NoError(t, e)
		require.False(t, local.Eligible, "account-local cooldown remains authoritative without a shared ledger block")
		var localKeys []string
		for _, gate := range local.Gates {
			localKeys = append(localKeys, gate.Key)
		}
		require.Contains(t, localKeys, frozen.ModelKey())
		require.False(t, r.Ledger.Snapshot().BlockedFailureDomains["1"])
		sibling, e := s.Store.InspectFailureDomains(ctx, 2, "test-model")
		require.NoError(t, e)
		require.True(t, sibling.Eligible)
		next := controlledPick(t, s, ctx, r, accounts)
		require.EqualValues(t, 2, next.ID)
		resp, e = controlledHTTP(t, s, ctx, next.ID, func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, `{"output":"done"}`) })
		require.NoError(t, e)
		controlledConsume(t, resp)
		require.Equal(t, 2, r.Ledger.Snapshot().Attempts)
		require.ErrorIs(t, r.Ledger.CanAttempt(1, 0, time.Now(), true), scheduling.ErrAttemptBudget)
		var siblingSends int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts WHERE request_id=$1 AND account_id=2 AND metrics ? 'sent_at'", r.ID).Scan(&siblingSends))
		require.Equal(t, 1, siblingSends)
	})
	t.Run("downstream_commit_and_nonstream_timing", func(t *testing.T) {
		s, _, _, accounts := controlledIntegration(t, false)
		ctx, r := controlledIntegrationRequest(t, s)
		a := controlledPick(t, s, ctx, r, accounts)
		resp, e := controlledHTTP(t, s, ctx, a.ID, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "{\"output\":\"done\"}") })
		require.NoError(t, e)
		controlledConsume(t, resp)
		require.Nil(t, r.history[0].FirstSemanticMS)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		writer := &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
		_, e = writer.Write([]byte("{\"output\":\"done\"}"))
		require.NoError(t, e)
		require.True(t, r.Ledger.Snapshot().Committed)
		require.ErrorIs(t, r.Ledger.CanAttempt(2, 0, time.Now(), true), scheduling.ErrCommitted)
	})
	t.Run("explain_does_not_mutate_stores", func(t *testing.T) {
		s, db, _, _ := controlledIntegration(t, false)
		// Explain requires an explicit read-only capacity source and the same
		// provider eligibility callback that production wiring supplies.
		s.concurrency = NewConcurrencyService(explainLoads{})
		s.SetExplainEligibility(allowExplain, nil)
		before, e := s.redis.Keys(context.Background(), "*").Result()
		require.NoError(t, e)
		_, e = s.Explain(context.Background(), []byte("{\"group_id\":7,\"model\":\"test-model\",\"protocol\":\"responses\"}"))
		require.NoError(t, e)
		after, e := s.redis.Keys(context.Background(), "*").Result()
		require.NoError(t, e)
		require.ElementsMatch(t, before, after)
		var tickets int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts").Scan(&tickets))
		require.Zero(t, tickets)
	})
}

func TestControlledPrepareFailuresNeverCountActualDispatch(t *testing.T) {
	t.Run("postgres_record_failure", func(t *testing.T) {
		s, db, _, accounts := controlledIntegration(t, false)
		ctx, r := controlledIntegrationRequest(t, s)
		a := controlledPick(t, s, ctx, r, accounts)
		_, err := db.Exec("CREATE FUNCTION reject_send_record() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN IF NEW.metrics ? ''sent_at'' AND NOT (OLD.metrics ? ''sent_at'') THEN RAISE EXCEPTION ''injected send record failure''; END IF; RETURN NEW; END'; CREATE TRIGGER reject_send_record BEFORE UPDATE ON scheduling_attempts FOR EACH ROW EXECUTE FUNCTION reject_send_record()")
		require.NoError(t, err)
		req, err := http.NewRequestWithContext(ctx, "POST", "http://unused", strings.NewReader("{}"))
		require.NoError(t, err)
		calls := 0
		_, err = s.roundTrip(req, a.ID, 10, func(*http.Request) (*http.Response, error) { calls++; return nil, nil })
		require.ErrorIs(t, err, scheduling.ErrSharedState)
		require.Zero(t, calls)
		require.Zero(t, r.Ledger.Snapshot().Attempts)
		var state, outcome string
		require.NoError(t, db.QueryRow("SELECT state,outcome FROM scheduling_attempts WHERE request_id=$1", r.ID).Scan(&state, &outcome))
		require.Equal(t, "settled", state)
		require.Equal(t, "not_sent", outcome)
		stats, err := s.Store.DispatchStatistics(ctx, 7, "test-model", time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
		require.NoError(t, err)
		require.Zero(t, stats.OrdinaryFirstTotal)
		require.Empty(t, stats.Accounts)
	})
	t.Run("redis_failure_after_durable_prepare", func(t *testing.T) {
		s, db, _, accounts := controlledIntegration(t, false)
		ctx, r := controlledIntegrationRequest(t, s)
		a := controlledPick(t, s, ctx, r, accounts)
		d, err := s.beginDispatch(ctx, a.ID, 10)
		require.NoError(t, err)
		require.Empty(t, d.budget.ID, "new group settings must not hide a second shared retry limit")
		creditKey := "fixture:legacy_retry_credit"
		require.NoError(t, s.redis.Set(ctx, creditKey, 10, time.Minute).Err())
		// Real Redis raises WRONGTYPE when the second commit reaches HSET.
		receiptKey := d.decision.PoolKey + ":receipts"
		require.NoError(t, s.redis.Set(ctx, receiptKey, "injected wrong type", time.Minute).Err())
		err = d.MarkSent()
		require.ErrorIs(t, err, scheduling.ErrSharedState)
		require.False(t, d.sent)
		require.Zero(t, r.Ledger.Snapshot().Attempts)
		credit, err := s.redis.Get(ctx, creditKey).Int()
		require.NoError(t, err)
		require.Equal(t, 10, credit, "account-pool dispatch never consumes or replenishes old retry credit")
		require.NoError(t, s.redis.Del(ctx, receiptKey).Err())
		d.Finish("not_sent", true, scheduling.ErrSharedState)
		d.Finish("duplicate", true, nil)
		credit, err = s.redis.Get(ctx, creditKey).Int()
		require.NoError(t, err)
		require.Equal(t, 10, credit)
		var outcome string
		var recorded, usagePending bool
		require.NoError(t, db.QueryRow("SELECT outcome,metrics ? 'sent_at',usage_pending FROM scheduling_attempts WHERE ticket_id=$1", d.ticket.TicketID).Scan(&outcome, &recorded, &usagePending))
		require.True(t, recorded, "the aborted prepare marker remains auditable")
		require.Equal(t, "not_sent", outcome)
		require.False(t, usagePending)
		stats, err := s.Store.DispatchStatistics(ctx, 7, "test-model", time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
		require.NoError(t, err)
		require.Zero(t, stats.OrdinaryFirstTotal)
		require.Empty(t, stats.Accounts, "an aborted prepare is not business traffic")
		nextCtx, nextRequest := controlledIntegrationRequest(t, s)
		next := controlledPick(t, s, nextCtx, nextRequest, accounts)
		require.Equal(t, a.ID, next.ID, "failed prepare must refund its SWRR allocation")
	})
}

func TestControlledSemanticClassification(t *testing.T) {
	cases := []struct {
		name, payload              string
		semantic, answer, terminal bool
	}{
		{"role", "{\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}", false, false, false},
		{"empty_text", "{\"type\":\"response.output_text.delta\",\"delta\":\"\"}", false, false, false},
		{"reasoning", "{\"type\":\"response.reasoning_text.delta\",\"delta\":\"r\"}", true, false, false},
		{"tool_without_arguments", "{\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"name\":\"f\"}}", true, false, false},
		{"block_stop_not_response_terminal", "{\"type\":\"content_block_stop\"}", false, false, false},
		{"text", "{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}", true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, a, end, _ := classifySemanticEvent([]byte(tc.payload))
			require.Equal(t, tc.semantic, s)
			require.Equal(t, tc.answer, a)
			require.Equal(t, tc.terminal, end)
		})
	}
}
