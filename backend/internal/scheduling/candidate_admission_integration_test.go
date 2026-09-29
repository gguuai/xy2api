package scheduling

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestCandidateAdmissionBatchMatchesIndividualReads(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	_, err := db.Exec("INSERT INTO accounts(id) SELECT generate_series(4,15); UPDATE accounts SET schedulable=FALSE WHERE id IN (3,11); UPDATE accounts SET status='disabled' WHERE id=15; UPDATE accounts SET credentials=jsonb_build_object('account_id','parent-one') WHERE id=1; UPDATE accounts SET parent_account_id=5 WHERE id=6; INSERT INTO scheduling_controls(scope,subject_id,state,mode,epoch) VALUES ('logical_account',4,'PAUSED','request_drain',1),('credential_family',5,'PAUSED','request_drain',1),('logical_account',7,'DRAINING','session_drain',3),('credential_family',7,'DRAIN_UNCERTAIN','session_drain',4),('logical_account',8,'DRAINING','session_drain',1),('logical_account',9,'DRAINING','session_drain',1),('logical_account',10,'DRAINING','session_drain',1),('logical_account',11,'RUNNING','request_drain',1),('logical_account',15,'RUNNING','request_drain',1); INSERT INTO scheduling_session_grants(scope,subject_id,epoch,session_id,remaining_turns,expires_at) VALUES ('logical_account',7,3,'owner',2,NOW()+INTERVAL '1 hour'),('credential_family',7,4,'owner',2,NOW()+INTERVAL '1 hour'),('logical_account',8,1,'owner',2,NOW()-INTERVAL '1 hour'),('logical_account',9,1,'owner',0,NOW()+INTERVAL '1 hour'),('logical_account',10,0,'owner',2,NOW()+INTERVAL '1 hour'); INSERT INTO scheduling_account_failure_domains(account_id,version,quota_pool_id) VALUES(12,1,'shared-q'),(13,1,'shared-q')")
	require.NoError(t, err)
	a12, err := s.FreezeFailureAdmission(ctx, 12, "actual-model")
	require.NoError(t, err)
	a14, err := s.FreezeFailureAdmission(ctx, 14, "actual-model")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO scheduling_failure_gates(gate_key,scope,reason,version,blocked,ready_after) VALUES($1,'quota_pool','rate_limit',1,FALSE,NOW()+INTERVAL '1 hour'),($2,'account_model','capability',1,TRUE,NULL)", a12.SharedKey("quota_pool", "shared-q", ""), a14.ModelKey())
	require.NoError(t, err)
	inputs := make([]CandidateAdmissionInput, 0, 15)
	for id := int64(1); id <= 15; id++ {
		inputs = append(inputs, CandidateAdmissionInput{AccountID: id, Model: "actual-model"})
	}
	var before int
	require.NoError(t, db.QueryRow("SELECT SUM(remaining_turns) FROM scheduling_session_grants").Scan(&before))
	for _, session := range []string{"", "owner", "other-owner"} {
		batch, err := s.ReadCandidateAdmissions(ctx, inputs, session)
		require.NoError(t, err)
		require.Len(t, batch, len(inputs))
		require.False(t, batch[11].ControlAllowed, "generic disable is a hard gate even with RUNNING logical control")
		require.False(t, batch[15].ControlAllowed, "inactive status is a hard gate even with RUNNING logical control")
		for _, input := range inputs {
			allowed, err := s.CanAdmitControl(ctx, input.AccountID, session)
			require.NoError(t, err)
			require.Equal(t, allowed, batch[input.AccountID].ControlAllowed, "control account=%d session=%s", input.AccountID, session)
			if !allowed {
				continue
			}
			state, err := s.InspectFailureDomains(ctx, input.AccountID, input.Model)
			require.NoError(t, err)
			frozen, err := s.FreezeFailureAdmission(ctx, input.AccountID, input.Model)
			require.NoError(t, err)
			require.Equal(t, state.Eligible, batch[input.AccountID].FailureEligible, "gate account=%d", input.AccountID)
			require.Equal(t, state.Domains, batch[input.AccountID].Domains)
			require.Equal(t, frozen.HealthIdentity, batch[input.AccountID].HealthIdentity)
		}
		require.Equal(t, batch[1].HealthIdentity, batch[2].HealthIdentity, "shadow identity follows owner")
	}
	var after int
	require.NoError(t, db.QueryRow("SELECT SUM(remaining_turns) FROM scheduling_session_grants").Scan(&after))
	require.Equal(t, before, after, "preflight cannot spend a continuation grant")
	t.Log("batch and individual preflight agree for family/account pause, grants, owner identity, shared and model gates; no grant consumed")
}

func TestCandidateAdmissionBatchIsFreshAndKeepsFinalFence(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	inputs := []CandidateAdmissionInput{{AccountID: 1, Model: "actual-model"}}
	first, err := s.ReadCandidateAdmissions(ctx, inputs, "")
	require.NoError(t, err)
	require.True(t, first[1].ControlAllowed)
	require.True(t, first[1].FailureEligible)
	_, err = db.ExecContext(ctx, "UPDATE accounts SET schedulable=FALSE WHERE id=1")
	require.NoError(t, err)
	paused, err := s.ReadCandidateAdmissions(ctx, inputs, "")
	require.NoError(t, err)
	require.False(t, paused[1].ControlAllowed)
	_, err = s.BeginDispatch(ctx, testDispatch(1, ""))
	require.ErrorIs(t, err, ErrControlBlocked, "a stale allowed preflight is not permission to send")
	_, err = db.ExecContext(ctx, "UPDATE accounts SET schedulable=TRUE WHERE id=1")
	require.NoError(t, err)
	_, err = db.Exec("UPDATE accounts SET credentials=jsonb_build_object('account_id','rotated-principal') WHERE id=1")
	require.NoError(t, err)
	rotated, err := s.ReadCandidateAdmissions(ctx, inputs, "")
	require.NoError(t, err)
	require.NotEqual(t, first[1].HealthIdentity, rotated[1].HealthIdentity)
	t.Log("each call re-reads controls and credential owner; final dispatch still rejects a post-snapshot pause")
}

func TestCandidateAdmissionBatchUsesBoundedUnknownProbe(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	a, err := s.FreezeFailureAdmission(ctx, 1, "actual-model")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO scheduling_failure_gates(gate_key,scope,reason,version,ready_after) VALUES($1,'account_model','recovery',1,NOW()-INTERVAL '1 second')", a.ModelKey())
	require.NoError(t, err)
	request := testDispatch(1, "")
	request.Failure = &a
	ticket, err := s.BeginDispatch(ctx, request)
	require.NoError(t, err)
	require.NoError(t, s.MarkAttemptUnknown(ctx, ticket.TicketID))
	inputs := []CandidateAdmissionInput{{AccountID: 1, Model: a.Model}}
	held, err := s.ReadCandidateAdmissions(ctx, inputs, "")
	require.NoError(t, err)
	require.False(t, held[1].FailureEligible)
	_, err = db.Exec("UPDATE scheduling_attempts SET unknown_hold_until=NOW()-INTERVAL '1 second' WHERE ticket_id=$1", ticket.TicketID)
	require.NoError(t, err)
	expired, err := s.ReadCandidateAdmissions(ctx, inputs, "")
	require.NoError(t, err)
	require.True(t, expired[1].FailureEligible)
	requireUnknownAuditPreserved(t, db, ticket.TicketID)
}

func TestCandidateAdmissionBatchRejectsMissingAndSkipsPausedInvalidIdentity(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	_, err := s.ReadCandidateAdmissions(ctx, []CandidateAdmissionInput{{AccountID: 999, Model: "m"}}, "")
	require.ErrorIs(t, err, ErrControlNotFound)
	_, err = db.Exec("UPDATE accounts SET schedulable=FALSE,credentials='[]'::jsonb WHERE id=3")
	require.NoError(t, err)
	paused, err := s.ReadCandidateAdmissions(ctx, []CandidateAdmissionInput{{AccountID: 3, Model: "m"}}, "")
	require.NoError(t, err)
	require.False(t, paused[3].ControlAllowed, "blocked accounts do not need credential decoding")
	empty, err := s.ReadCandidateAdmissions(ctx, nil, "")
	require.NoError(t, err)
	require.Empty(t, empty)
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = s.ReadCandidateAdmissions(cancelCtx, []CandidateAdmissionInput{{AccountID: 1, Model: "m"}}, "")
	require.ErrorIs(t, err, context.Canceled)
}

// Instrument actual database/sql driver calls, not an inferred count from the
// implementation. Both paths use the same real PostgreSQL schema and connection.
type admissionCountingConnector struct {
	driver.Connector
	queries *atomic.Int64
}
type admissionCountingConn struct {
	driver.Conn
	queries *atomic.Int64
}

func (c admissionCountingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return admissionCountingConn{Conn: conn, queries: c.queries}, nil
}
func (c admissionCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.queries.Add(1)
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return q.QueryContext(ctx, query, args)
}

func countedAdmissionStore(t testing.TB, db *sql.DB) (*PostgresStore, *atomic.Int64) {
	t.Helper()
	var schema string
	require.NoError(t, db.QueryRow("SELECT current_schema()").Scan(&schema))
	u, err := url.Parse(os.Getenv("SCHEDULING_TEST_POSTGRES_DSN"))
	require.NoError(t, err)
	values := u.Query()
	values.Set("options", "-csearch_path="+schema)
	u.RawQuery = values.Encode()
	connector, err := pq.NewConnector(u.String())
	require.NoError(t, err)
	queries := &atomic.Int64{}
	counted := sql.OpenDB(admissionCountingConnector{Connector: connector, queries: queries})
	counted.SetMaxOpenConns(1)
	require.NoError(t, counted.Ping())
	t.Cleanup(func() { require.NoError(t, counted.Close()) })
	return NewPostgresStore(counted), queries
}

func seedAdmissionHundred(t testing.TB, db *sql.DB) []CandidateAdmissionInput {
	t.Helper()
	_, err := db.Exec("INSERT INTO accounts(id) SELECT generate_series(4,100); INSERT INTO scheduling_controls(scope,subject_id,state) SELECT scope,id,'RUNNING' FROM accounts CROSS JOIN (VALUES('logical_account'),('credential_family')) AS scopes(scope) ON CONFLICT DO NOTHING")
	require.NoError(t, err)
	inputs := make([]CandidateAdmissionInput, 100)
	for i := range inputs {
		inputs[i] = CandidateAdmissionInput{AccountID: int64(i + 1), Model: "m"}
	}
	return inputs
}

func readAdmissionIndividually(ctx context.Context, s *PostgresStore, inputs []CandidateAdmissionInput) error {
	for _, input := range inputs {
		allowed, err := s.CanAdmitControl(ctx, input.AccountID, "")
		if err != nil {
			return err
		}
		if !allowed {
			return errors.New("benchmark fixture unexpectedly blocked")
		}
		_, err = s.InspectFailureDomains(ctx, input.AccountID, input.Model)
		if err != nil {
			return err
		}
	}
	return nil
}

func TestCandidateAdmissionBatchConstantQueries100(t *testing.T) {
	_, db := isolatedControlStore(t)
	inputs := seedAdmissionHundred(t, db)
	s, queries := countedAdmissionStore(t, db)
	ctx := context.Background()
	start := time.Now()
	require.NoError(t, readAdmissionIndividually(ctx, s, inputs))
	individualElapsed, individualQueries := time.Since(start), queries.Swap(0)
	start = time.Now()
	result, err := s.ReadCandidateAdmissions(ctx, inputs, "")
	batchElapsed, batchQueries := time.Since(start), queries.Load()
	require.NoError(t, err)
	require.Len(t, result, 100)
	require.Greater(t, individualQueries, batchQueries)
	require.EqualValues(t, 2, batchQueries)
	t.Logf("100 accounts: individual_queries=%d batch_queries=%d individual_elapsed=%s batch_elapsed=%s (isolated local PG; no injected latency)", individualQueries, batchQueries, individualElapsed, batchElapsed)
}

func benchmarkAdmissionFixture(b *testing.B) *sql.DB {
	b.Helper()
	dsn := os.Getenv("SCHEDULING_TEST_POSTGRES_DSN")
	if dsn == "" {
		b.Skip("isolated PostgreSQL DSN not provided")
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(b, err)
	schema := "admission_bench_" + NewDispatchID()
	_, err = admin.Exec("CREATE SCHEMA " + schema)
	require.NoError(b, err)
	u, err := url.Parse(dsn)
	require.NoError(b, err)
	values := u.Query()
	values.Set("options", "-csearch_path="+schema)
	u.RawQuery = values.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(b, err)
	b.Cleanup(func() { _ = db.Close(); _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE"); _ = admin.Close() })
	_, err = db.Exec("CREATE TABLE scheduler_outbox(id BIGSERIAL PRIMARY KEY,event_type TEXT NOT NULL,account_id BIGINT); CREATE TABLE accounts(id BIGINT PRIMARY KEY,parent_account_id BIGINT REFERENCES accounts(id),credentials JSONB NOT NULL DEFAULT '{}'::jsonb,platform TEXT NOT NULL DEFAULT 'openai',type TEXT NOT NULL DEFAULT 'oauth',extra JSONB NOT NULL DEFAULT '{}'::jsonb,schedulable BOOLEAN NOT NULL DEFAULT TRUE,status TEXT NOT NULL DEFAULT 'active',deleted_at TIMESTAMPTZ,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()); INSERT INTO accounts(id) VALUES(1),(3); INSERT INTO accounts(id,parent_account_id) VALUES(2,1)")
	require.NoError(b, err)
	for _, name := range []string{"257_explicit_account_scheduling.sql", "259_scheduling_failure_domains.sql", "260_scheduling_profile_health_revision.sql", "261_scheduling_failure_feedback_queue.sql", "263_dual_scheduling_mode.sql", "264_scheduling_bounded_unknown_recovery.sql"} {
		migration, err := os.ReadFile("../../migrations/" + name)
		require.NoError(b, err)
		_, err = db.Exec(string(migration))
		require.NoError(b, err)
	}
	return db
}

func BenchmarkCandidateAdmission100(b *testing.B) {
	db := benchmarkAdmissionFixture(b)
	inputs := seedAdmissionHundred(b, db)
	s, queries := countedAdmissionStore(b, db)
	ctx := context.Background()
	for _, batch := range []bool{false, true} {
		b.Run(fmt.Sprintf("batch=%t", batch), func(b *testing.B) {
			queries.Store(0)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var err error
				if batch {
					_, err = s.ReadCandidateAdmissions(ctx, inputs, "")
				} else {
					err = readAdmissionIndividually(ctx, s, inputs)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(queries.Load())/float64(b.N), "queries/op")
		})
	}
}
