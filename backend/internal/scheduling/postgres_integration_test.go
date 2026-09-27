package scheduling

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func isolatedControlStore(t *testing.T) (*PostgresStore, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("SCHEDULING_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL DSN not provided")
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	require.NoError(t, admin.Ping())
	schema := "scheduling_" + NewDispatchID()
	_, err = admin.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("options", "-csearch_path="+schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	db.SetMaxOpenConns(12)
	t.Cleanup(func() { db.Close(); _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE"); admin.Close() })
	_, err = db.Exec("CREATE TABLE scheduler_outbox(id BIGSERIAL PRIMARY KEY,event_type TEXT NOT NULL,account_id BIGINT); CREATE TABLE accounts(id BIGINT PRIMARY KEY,parent_account_id BIGINT REFERENCES accounts(id),schedulable BOOLEAN NOT NULL DEFAULT TRUE,status TEXT NOT NULL DEFAULT 'active',deleted_at TIMESTAMPTZ,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()); INSERT INTO accounts(id) VALUES(1),(3); INSERT INTO accounts(id,parent_account_id) VALUES(2,1)")
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/257_explicit_account_scheduling.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	return NewPostgresStore(db), db
}
func testDispatch(id int64, session string) DispatchRequest {
	return DispatchRequest{TicketID: NewDispatchID(), RequestID: NewDispatchID(), AccountID: id, SessionID: session, NodeID: "isolated-test-node", LeaseDuration: time.Minute}
}
func TestPostgresControlIntegration(t *testing.T) {
	t.Run("pause_and_settlement", func(t *testing.T) {
		s, db := isolatedControlStore(t)
		ctx := context.Background()
		ticket, err := s.BeginDispatch(ctx, testDispatch(1, ""))
		require.NoError(t, err)
		c, err := s.Control(ctx, ControlCommand{AccountID: 1, Action: "pause"})
		require.NoError(t, err)
		require.Equal(t, ControlDraining, c.State)
		require.EqualValues(t, 1, c.ActiveAttempts)
		_, err = s.BeginDispatch(ctx, testDispatch(1, ""))
		require.ErrorIs(t, err, ErrControlBlocked)
		require.NoError(t, s.SettleAttempt(ctx, ticket.TicketID, "completed", true))
		require.NoError(t, s.SettleAttempt(ctx, ticket.TicketID, "cancelled", false))
		c, err = s.GetControl(ctx, 1, ScopeAccount)
		require.NoError(t, err)
		require.Equal(t, ControlPaused, c.State)
		require.EqualValues(t, 1, c.PendingSettlements)
		var outcome string
		require.NoError(t, db.QueryRow("SELECT outcome FROM scheduling_attempts WHERE ticket_id=$1", ticket.TicketID).Scan(&outcome))
		require.Equal(t, "completed", outcome)
		require.NoError(t, s.AcknowledgeUsage(ctx, ticket.TicketID))
		c, err = s.Control(ctx, ControlCommand{AccountID: 1, Action: "resume"})
		require.NoError(t, err)
		require.Equal(t, ControlRunning, c.State)
	})
	t.Run("pause_dispatch_race", func(t *testing.T) {
		s, _ := isolatedControlStore(t)
		ctx := context.Background()
		start := make(chan struct{})
		var wg sync.WaitGroup
		var admitted atomic.Int64
		errs := make(chan error, 40)
		for i := 0; i < 30; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := s.BeginDispatch(ctx, testDispatch(1, ""))
				if err == nil {
					admitted.Add(1)
				} else if !errors.Is(err, ErrControlBlocked) {
					errs <- err
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.Control(ctx, ControlCommand{AccountID: 1, Action: "pause"})
			if err != nil {
				errs <- err
			}
		}()
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
		c, err := s.GetControl(ctx, 1, ScopeAccount)
		require.NoError(t, err)
		require.Equal(t, admitted.Load(), c.ActiveAttempts)
		for i := 0; i < 5; i++ {
			_, err = s.BeginDispatch(ctx, testDispatch(1, ""))
			require.ErrorIs(t, err, ErrControlBlocked)
		}
	})
	t.Run("family_and_logical_scope", func(t *testing.T) {
		s, _ := isolatedControlStore(t)
		ctx := context.Background()
		_, err := s.Control(ctx, ControlCommand{AccountID: 1, Action: "pause"})
		require.NoError(t, err)
		_, err = s.BeginDispatch(ctx, testDispatch(2, ""))
		require.NoError(t, err)
		_, err = s.Control(ctx, ControlCommand{AccountID: 2, Scope: ScopeFamily, Action: "pause"})
		require.NoError(t, err)
		_, err = s.BeginDispatch(ctx, testDispatch(2, ""))
		require.ErrorIs(t, err, ErrControlBlocked)
		_, err = s.Control(ctx, ControlCommand{AccountID: 2, Scope: ScopeFamily, Action: "resume"})
		require.NoError(t, err)
		_, err = s.BeginDispatch(ctx, testDispatch(1, ""))
		require.ErrorIs(t, err, ErrControlBlocked)
		_, err = s.BeginDispatch(ctx, testDispatch(2, ""))
		require.NoError(t, err)
		wrong := testDispatch(2, "")
		wrong.FamilyID = 3
		_, err = s.BeginDispatch(ctx, wrong)
		require.ErrorIs(t, err, ErrInvalidControl)
	})
	t.Run("bounded_session_drain", func(t *testing.T) {
		s, _ := isolatedControlStore(t)
		ctx := context.Background()
		ticket, err := s.BeginDispatch(ctx, testDispatch(1, "old-owner"))
		require.NoError(t, err)
		require.NoError(t, s.SettleAttempt(ctx, ticket.TicketID, "completed", false))
		c, err := s.Control(ctx, ControlCommand{AccountID: 1, Action: "session_drain", SessionDurationSeconds: 60, SessionMaxTurns: 1})
		require.NoError(t, err)
		require.EqualValues(t, 1, c.AllowedSessions)
		_, err = s.BeginDispatch(ctx, testDispatch(1, "new-owner"))
		require.ErrorIs(t, err, ErrControlBlocked)
		next, err := s.BeginDispatch(ctx, testDispatch(1, "old-owner"))
		require.NoError(t, err)
		_, err = s.BeginDispatch(ctx, testDispatch(1, "old-owner"))
		require.ErrorIs(t, err, ErrControlBlocked)
		require.NoError(t, s.SettleAttempt(ctx, next.TicketID, "completed", false))
		c, err = s.GetControl(ctx, 1, ScopeAccount)
		require.NoError(t, err)
		require.Equal(t, ControlPaused, c.State)
	})
	t.Run("lease_expiry_and_force_stop", func(t *testing.T) {
		s, _ := isolatedControlStore(t)
		ctx := context.Background()
		r := testDispatch(1, "")
		r.LeaseDuration = 5 * time.Millisecond
		ticket, err := s.BeginDispatch(ctx, r)
		require.NoError(t, err)
		time.Sleep(20 * time.Millisecond)
		n, err := s.ReconcileExpired(ctx)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		c, err := s.Control(ctx, ControlCommand{AccountID: 1, Action: "pause"})
		require.NoError(t, err)
		require.Equal(t, ControlUncertain, c.State)
		require.EqualValues(t, 1, c.UnknownAttempts)
		s.SetCancelHook(func(context.Context, ControlSnapshot) error { return errors.New("notification unavailable") })
		c, err = s.Control(ctx, ControlCommand{AccountID: 1, Action: "force_stop"})
		require.NoError(t, err)
		require.NotEmpty(t, c.NotificationWarning)
		require.Equal(t, ControlUncertain, c.State)
		require.NoError(t, s.ResolveUnknown(ctx, ticket.TicketID, "observed_terminal", false))
		c, err = s.GetControl(ctx, 1, ScopeAccount)
		require.NoError(t, err)
		require.Equal(t, ControlPaused, c.State)
	})
	t.Run("epoch_conflict_and_old_settlement", func(t *testing.T) {
		s, _ := isolatedControlStore(t)
		ctx := context.Background()
		old, err := s.BeginDispatch(ctx, testDispatch(1, ""))
		require.NoError(t, err)
		zero := int64(0)
		_, err = s.Control(ctx, ControlCommand{AccountID: 1, Action: "pause", ExpectedEpoch: &zero})
		require.NoError(t, err)
		_, err = s.Control(ctx, ControlCommand{AccountID: 1, Action: "resume", ExpectedEpoch: &zero})
		require.ErrorIs(t, err, ErrVersionConflict)
		c, err := s.Control(ctx, ControlCommand{AccountID: 1, Action: "resume"})
		require.NoError(t, err)
		epoch := c.Epoch
		require.NoError(t, s.SettleAttempt(ctx, old.TicketID, "completed", false))
		c, err = s.GetControl(ctx, 1, ScopeAccount)
		require.NoError(t, err)
		require.Equal(t, ControlRunning, c.State)
		require.Equal(t, epoch, c.Epoch)
	})
	t.Run("ticket_reuse_rollback", func(t *testing.T) {
		s, db := isolatedControlStore(t)
		ctx := context.Background()
		r := testDispatch(1, "owner")
		_, err := s.BeginDispatch(ctx, r)
		require.NoError(t, err)
		_, err = s.BeginDispatch(ctx, r)
		require.ErrorIs(t, err, ErrAttemptIdentity)
		var n int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts").Scan(&n))
		require.Equal(t, 1, n)
	})
	t.Run("policy_compare_and_swap", func(t *testing.T) {
		s, _ := isolatedControlStore(t)
		ctx := context.Background()
		p := NormalizePolicy(Policy{GroupID: 0, Model: "test", Enabled: false})
		rec, err := s.PutPolicy(ctx, p, 0)
		require.NoError(t, err)
		require.EqualValues(t, 1, rec.Version)
		_, err = s.PutPolicy(ctx, p, 0)
		require.ErrorIs(t, err, ErrVersionConflict)
		var success atomic.Int64
		var wg sync.WaitGroup
		errs := make(chan error, 10)
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, e := s.PutPolicy(ctx, p, 1)
				if e == nil {
					success.Add(1)
				} else if !errors.Is(e, ErrVersionConflict) {
					errs <- e
				}
			}()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			require.NoError(t, e)
		}
		require.EqualValues(t, 1, success.Load())
		rec, err = s.GetPolicy(ctx, 0, "test")
		require.NoError(t, err)
		require.EqualValues(t, 2, rec.Version)
		require.EqualValues(t, 2, rec.Policy.Version)
	})
	t.Run("hard_capacity_keeps_unknown", func(t *testing.T) {
		s, _ := isolatedControlStore(t)
		ctx := context.Background()
		r := testDispatch(1, "")
		r.HardConcurrency = 1
		ticket, err := s.BeginDispatch(ctx, r)
		require.NoError(t, err)
		require.NoError(t, s.MarkAttemptUnknown(ctx, ticket.TicketID))
		_, err = s.BeginDispatch(ctx, r)
		require.ErrorIs(t, err, ErrAttemptIdentity)
		r.TicketID = NewDispatchID()
		_, err = s.BeginDispatch(ctx, r)
		require.ErrorIs(t, err, ErrCapacity)
		counts, err := s.AccountActiveCounts(ctx, []int64{1, 2})
		require.NoError(t, err)
		require.Equal(t, 1, counts[1])
		require.Zero(t, counts[2])
		require.NoError(t, s.ResolveUnknown(ctx, ticket.TicketID, "observed_terminal", false))
		_, err = s.BeginDispatch(ctx, r)
		require.NoError(t, err)
	})
	t.Run("session_candidate_is_read_only", func(t *testing.T) {
		s, _ := isolatedControlStore(t)
		ctx := context.Background()
		ticket, err := s.BeginDispatch(ctx, testDispatch(1, "owner"))
		require.NoError(t, err)
		require.NoError(t, s.SettleAttempt(ctx, ticket.TicketID, "completed", false))
		_, err = s.Control(ctx, ControlCommand{AccountID: 1, Action: "session_drain", SessionDurationSeconds: 60, SessionMaxTurns: 1})
		require.NoError(t, err)
		for i := 0; i < 3; i++ {
			ok, e := s.CanContinueSession(ctx, 1, "owner")
			require.NoError(t, e)
			require.True(t, ok)
		}
		_, err = s.BeginDispatch(ctx, testDispatch(1, "owner"))
		require.NoError(t, err)
		ok, err := s.CanContinueSession(ctx, 1, "owner")
		require.NoError(t, err)
		require.False(t, ok)
	})

	t.Run("force_stop_survives_resume_and_is_scope_fenced", func(t *testing.T) {
		s, _ := isolatedControlStore(t)
		ctx := context.Background()
		first, err := s.BeginDispatch(ctx, testDispatch(1, ""))
		require.NoError(t, err)
		child, err := s.BeginDispatch(ctx, testDispatch(2, ""))
		require.NoError(t, err)
		outside, err := s.BeginDispatch(ctx, testDispatch(3, ""))
		require.NoError(t, err)
		_, err = s.Control(ctx, ControlCommand{AccountID: 1, Action: "force_stop"})
		require.NoError(t, err)
		_, err = s.Control(ctx, ControlCommand{AccountID: 1, Action: "resume"})
		require.NoError(t, err)
		newer, err := s.BeginDispatch(ctx, testDispatch(1, ""))
		require.NoError(t, err)
		for _, check := range []struct {
			id   string
			want bool
		}{{first.TicketID, true}, {child.TicketID, false}, {outside.TicketID, false}, {newer.TicketID, false}} {
			got, e := s.AttemptCancellationRequested(ctx, check.id)
			require.NoError(t, e)
			require.Equal(t, check.want, got)
		}
		_, err = s.Control(ctx, ControlCommand{AccountID: 2, Scope: ScopeFamily, Action: "force_stop"})
		require.NoError(t, err)
		_, err = s.Control(ctx, ControlCommand{AccountID: 2, Scope: ScopeFamily, Action: "resume"})
		require.NoError(t, err)
		got, err := s.AttemptCancellationRequested(ctx, child.TicketID)
		require.NoError(t, err)
		require.True(t, got)
		got, err = s.AttemptCancellationRequested(ctx, outside.TicketID)
		require.NoError(t, err)
		require.False(t, got)
		fresh, err := s.BeginDispatch(ctx, testDispatch(2, ""))
		require.NoError(t, err)
		got, err = s.AttemptCancellationRequested(ctx, fresh.TicketID)
		require.NoError(t, err)
		require.False(t, got)
		_, err = s.AttemptCancellationRequested(ctx, "missing")
		require.ErrorIs(t, err, ErrAttemptIdentity)
	})
	t.Run("force_stop_settlement_lock_order", func(t *testing.T) {
		s, _ := isolatedControlStore(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for i := 0; i < 12; i++ {
			ticket, err := s.BeginDispatch(ctx, testDispatch(1, ""))
			require.NoError(t, err)
			start := make(chan struct{})
			errs := make(chan error, 2)
			go func() { <-start; _, e := s.Control(ctx, ControlCommand{AccountID: 1, Action: "force_stop"}); errs <- e }()
			go func() { <-start; errs <- s.SettleAttempt(ctx, ticket.TicketID, "completed", false) }()
			close(start)
			require.NoError(t, <-errs)
			require.NoError(t, <-errs)
			_, err = s.Control(ctx, ControlCommand{AccountID: 1, Action: "resume"})
			require.NoError(t, err)
		}
	})
	t.Run("request_trace_redaction_and_unobserved_values", func(t *testing.T) {
		s, db := isolatedControlStore(t)
		ctx := context.Background()
		req := testDispatch(1, "")
		req.RequestID = "trace-request"
		first, err := s.BeginDispatch(ctx, req)
		require.NoError(t, err)
		req = testDispatch(2, "")
		req.RequestID = "trace-request"
		second, err := s.BeginDispatch(ctx, req)
		require.NoError(t, err)
		require.NoError(t, s.RecordAttemptMetrics(ctx, first.TicketID, map[string]any{"priority": -1, "reason": "same_tier_retry", "metric_version": "2", "first_event_ms": int64(0), "first_semantic_ms": nil, "prompt": "MUST_NOT_PERSIST", "access_token": "MUST_NOT_PERSIST", "nested": map[string]any{"secret": "MUST_NOT_PERSIST"}}))
		require.NoError(t, s.RecordAttemptMetrics(ctx, first.TicketID, map[string]any{"remaining_budget_ms": int64(500)}))
		require.NoError(t, s.SettleAttempt(ctx, first.TicketID, "retryable_error", false))
		items, err := s.ListRequestAttempts(ctx, "trace-request")
		require.NoError(t, err)
		require.Len(t, items, 2)
		require.Equal(t, first.TicketID, items[0].TicketID)
		require.Equal(t, second.TicketID, items[1].TicketID)
		require.NotNil(t, items[0].SettledAt)
		require.Nil(t, items[1].SettledAt)
		require.JSONEq(t, `{"priority":-1,"reason":"same_tier_retry","metric_version":"2","first_event_ms":0,"remaining_budget_ms":500}`, string(items[0].Metrics))
		require.JSONEq(t, "{}", string(items[1].Metrics))
		require.ErrorIs(t, s.RecordAttemptMetrics(ctx, first.TicketID, map[string]any{"first_event_ms": -1}), ErrInvalidControl)
		require.ErrorIs(t, s.RecordAttemptMetrics(ctx, "missing", map[string]any{}), ErrAttemptIdentity)
		empty, err := s.ListRequestAttempts(ctx, "unknown-request")
		require.NoError(t, err)
		require.NotNil(t, empty)
		require.Empty(t, empty)
		var persisted string
		require.NoError(t, db.QueryRow("SELECT metrics::text FROM scheduling_attempts WHERE ticket_id=$1", first.TicketID).Scan(&persisted))
		require.NotContains(t, persisted, "MUST_NOT_PERSIST")
	})

	t.Run("actual_dispatch_stats_separate_kinds_and_scope", func(t *testing.T) {
		s, _ := isolatedControlStore(t)
		ctx := context.Background()
		now := time.Now().UTC()
		sends := []struct {
			account, group int64
			model, kind    string
			sent           bool
		}{{1, 7, "model-a", "ordinary_first", true}, {1, 7, "model-a", "ordinary_first", true}, {2, 7, "model-a", "ordinary_first", true}, {1, 7, "model-a", "retry", true}, {2, 7, "model-a", "probe", true}, {2, 7, "model-a", "pin", true}, {2, 7, "model-a", "owner", true}, {2, 7, "model-a", "fallback", true}, {1, 7, "model-a", "ordinary_first", false}, {1, 8, "model-a", "ordinary_first", true}, {1, 7, "model-b", "ordinary_first", true}}
		for _, send := range sends {
			ticket, err := s.BeginDispatch(ctx, testDispatch(send.account, ""))
			require.NoError(t, err)
			m := map[string]any{"group_id": send.group, "model": send.model, "dispatch_kind": send.kind, "attempt_number": 1}
			if send.sent {
				m["sent_at"] = now.Format(time.RFC3339Nano)
			}
			require.NoError(t, s.RecordAttemptMetrics(ctx, ticket.TicketID, m))
		}
		stats, err := s.DispatchStatistics(ctx, 7, "model-a", now.Add(-time.Minute), now.Add(time.Minute))
		require.NoError(t, err)
		require.EqualValues(t, 3, stats.OrdinaryFirstTotal)
		require.Len(t, stats.Accounts, 2)
		require.InDelta(t, 2.0/3, *stats.Accounts[0].OrdinaryFirstShare, 1e-9)
		require.EqualValues(t, 1, stats.Accounts[0].ByKind["retry"])
		require.EqualValues(t, 0, stats.Accounts[0].ByKind["owner"])
		require.InDelta(t, 1.0/3, *stats.Accounts[1].OrdinaryFirstShare, 1e-9)
		for _, k := range []string{"probe", "pin", "owner", "fallback"} {
			require.EqualValues(t, 1, stats.Accounts[1].ByKind[k])
		}
		empty, err := s.DispatchStatistics(ctx, 7, "model-a", now.Add(-2*time.Minute), now.Add(-time.Minute))
		require.NoError(t, err)
		require.Empty(t, empty.Accounts)
		ticket, err := s.BeginDispatch(ctx, testDispatch(3, ""))
		require.NoError(t, err)
		require.NoError(t, s.RecordAttemptMetrics(ctx, ticket.TicketID, map[string]any{"group_id": 9, "model": "m", "dispatch_kind": "owner", "sent_at": now.Format(time.RFC3339Nano)}))
		ownerOnly, err := s.DispatchStatistics(ctx, 9, "m", now.Add(-time.Minute), now.Add(time.Minute))
		require.NoError(t, err)
		require.Len(t, ownerOnly.Accounts, 1)
		require.Nil(t, ownerOnly.Accounts[0].OrdinaryFirstShare)
		require.ErrorIs(t, s.RecordAttemptMetrics(ctx, ticket.TicketID, map[string]any{"sent_at": "not-a-time"}), ErrInvalidControl)
		require.ErrorIs(t, s.RecordAttemptMetrics(ctx, ticket.TicketID, map[string]any{"dispatch_kind": "unclassified"}), ErrInvalidControl)
	})
	t.Run("usage_acknowledgement_survives_late_settlement", func(t *testing.T) {
		s, _ := isolatedControlStore(t)
		ctx := context.Background()
		ticket, err := s.BeginDispatch(ctx, testDispatch(1, ""))
		require.NoError(t, err)
		require.NoError(t, s.RecordAttemptMetrics(ctx, ticket.TicketID, map[string]any{"sent_at": time.Now().UTC().Format(time.RFC3339Nano)}))
		c, err := s.GetControl(ctx, 1, ScopeAccount)
		require.NoError(t, err)
		require.EqualValues(t, 1, c.PendingSettlements)
		require.NoError(t, s.MarkAttemptUnknown(ctx, ticket.TicketID))
		require.NoError(t, s.AcknowledgeUsage(ctx, ticket.TicketID))
		require.NoError(t, s.AcknowledgeUsage(ctx, ticket.TicketID))
		c, err = s.GetControl(ctx, 1, ScopeAccount)
		require.NoError(t, err)
		require.EqualValues(t, 1, c.UnknownAttempts)
		require.Zero(t, c.PendingSettlements)
		require.NoError(t, s.ResolveUnknown(ctx, ticket.TicketID, "observed_completed", true))
		c, err = s.GetControl(ctx, 1, ScopeAccount)
		require.NoError(t, err)
		require.Zero(t, c.PendingSettlements)
		require.Zero(t, c.UnknownAttempts)
	})

	t.Run("overlapping_family_and_account_force_stop", func(t *testing.T) {
		s, _ := isolatedControlStore(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tickets := make([]DispatchTicket, 0, 20)
		for i := 0; i < 20; i++ {
			ticket, err := s.BeginDispatch(ctx, testDispatch(int64(1+i%2), ""))
			require.NoError(t, err)
			tickets = append(tickets, ticket)
		}
		start := make(chan struct{})
		errs := make(chan error, 22)
		go func() {
			<-start
			_, err := s.Control(ctx, ControlCommand{AccountID: 2, Scope: ScopeFamily, Action: "force_stop"})
			errs <- err
		}()
		go func() {
			<-start
			_, err := s.Control(ctx, ControlCommand{AccountID: 2, Scope: ScopeAccount, Action: "force_stop"})
			errs <- err
		}()
		for _, ticket := range tickets {
			go func(ticket DispatchTicket) {
				<-start
				errs <- s.SettleAttempt(ctx, ticket.TicketID, "completed", false)
			}(ticket)
		}
		close(start)
		for i := 0; i < 22; i++ {
			require.NoError(t, <-errs)
		}
	})
	fmt.Println("isolated PostgreSQL control, attempt, scope, drain, lease, and policy CAS assertions completed")
}
