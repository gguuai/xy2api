package scheduling

import (
	"context"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

func TestOneClickAccountMigrationKeepsSingleAccountScope(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	_, err := db.Exec("UPDATE accounts SET schedulable=FALSE WHERE id=3; INSERT INTO scheduling_controls(scope,subject_id,state,mode,epoch) VALUES('logical_account',1,'PAUSED','request_drain',2),('credential_family',1,'DRAINING','session_drain',4); INSERT INTO scheduling_session_grants(scope,subject_id,epoch,session_id,remaining_turns,expires_at) VALUES('credential_family',1,4,'owner',10,NOW()+INTERVAL '1 hour')")
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/265_single_account_scheduling_switch.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	var a, b, c bool
	require.NoError(t, db.QueryRow("SELECT (SELECT schedulable FROM accounts WHERE id=1),(SELECT schedulable FROM accounts WHERE id=2),(SELECT schedulable FROM accounts WHERE id=3)").Scan(&a, &b, &c))
	require.False(t, a)
	require.True(t, b)
	require.False(t, c)
	_, err = db.Exec("UPDATE accounts SET schedulable=TRUE WHERE id=1")
	require.NoError(t, err)
	for _, id := range []int64{1, 2} {
		allowed, e := s.CanAdmitControl(ctx, id, "owner")
		require.NoError(t, e)
		require.True(t, allowed)
		ticket, e := s.BeginDispatch(ctx, testDispatch(id, "owner"))
		require.NoError(t, e)
		require.NoError(t, s.SettleAttempt(ctx, ticket.TicketID, "completed", false))
	}
	var controls, grants int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_controls WHERE state<>'RUNNING'").Scan(&controls))
	require.NoError(t, db.QueryRow("SELECT SUM(remaining_turns) FROM scheduling_session_grants").Scan(&grants))
	require.Positive(t, controls, "old state remains auditable but is not an admission gate")
	require.Equal(t, 10, grants, "retired grants cannot authorize or be consumed by new requests")
	t.Log("migration: only A paused; B unchanged, prior disabled unchanged; A enable admits despite old pause; old grants unchanged")
}

func TestOneClickAccountDisableDrainsWithoutCancellation(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	active, err := s.BeginDispatch(ctx, testDispatch(1, "first-turn"))
	require.NoError(t, err)
	_, err = db.Exec("UPDATE accounts SET schedulable=FALSE WHERE id=1")
	require.NoError(t, err)
	_, err = s.BeginDispatch(ctx, testDispatch(1, "first-turn"))
	require.ErrorIs(t, err, ErrControlBlocked)
	_, err = s.BeginDispatch(ctx, testDispatch(1, "second-turn"))
	require.ErrorIs(t, err, ErrControlBlocked)
	require.NoError(t, s.RenewAttempt(ctx, active.TicketID, time.Minute), "in-flight request may renew to complete naturally")
	cancelled, err := s.AttemptCancellationRequested(ctx, active.TicketID)
	require.NoError(t, err)
	require.False(t, cancelled)
	peer, err := s.BeginDispatch(ctx, testDispatch(3, "new-request"))
	require.NoError(t, err)
	require.NoError(t, s.SettleAttempt(ctx, active.TicketID, "completed", false))
	require.NoError(t, s.SettleAttempt(ctx, peer.TicketID, "completed", false))
	_, err = db.Exec("UPDATE accounts SET schedulable=TRUE WHERE id=1")
	require.NoError(t, err)
	next, err := s.BeginDispatch(ctx, testDispatch(1, "next"))
	require.NoError(t, err)
	require.NoError(t, s.SettleAttempt(ctx, next.TicketID, "completed", false))
	t.Log("off: new and next-turn blocked, active attempt renewed/completed, other account accepted; on: new request accepted")
}

func TestOneClickAccountIgnoresRetiredSharedPoolsPreservesLocalFailure(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	_, err := db.Exec("INSERT INTO scheduling_account_failure_domains(account_id,version,quota_pool_id,availability_pool_id) VALUES(1,1,'old-quota','old-service'),(2,1,'old-quota','old-service')")
	require.NoError(t, err)
	a, err := s.FreezeFailureAdmission(ctx, 1, "model-a")
	require.NoError(t, err)
	b, err := s.FreezeFailureAdmission(ctx, 2, "model-a")
	require.NoError(t, err)
	require.Empty(t, a.Domains.QuotaPoolID)
	require.Empty(t, a.Domains.AvailabilityPoolID)
	require.NotEqual(t, a.AccountKey(), b.AccountKey(), "shared credentials must not couple the account switches or local failure gates")
	_, err = db.Exec("INSERT INTO scheduling_failure_gates(gate_key,scope,reason,version,blocked) VALUES($1,'quota_pool','old',1,TRUE),($2,'availability_pool','old',1,TRUE)", a.SharedKey("quota_pool", "old-quota", ""), a.SharedKey("availability_pool", "old-service", ""))
	require.NoError(t, err)
	values, err := s.ReadCandidateAdmissions(ctx, []CandidateAdmissionInput{{AccountID: 1, Model: "model-a"}, {AccountID: 2, Model: "model-a"}}, "")
	require.NoError(t, err)
	require.True(t, values[1].FailureEligible)
	require.True(t, values[2].FailureEligible)
	ticket := domainTicket(t, s, 1)
	decision := ClassifyFailure(FailureEvidence{Status: 401, Code: "invalid_api_key", Trusted: true, ReplaySafe: true}, *ticket.Failure, time.Now())
	require.NoError(t, s.SettleAttempt(ctx, ticket.TicketID, "upstream_error", false))
	require.NoError(t, s.ApplyFailureFeedback(ctx, ticket.TicketID, decision, false))
	_, err = db.Exec("UPDATE accounts SET schedulable=FALSE WHERE id=1; UPDATE accounts SET schedulable=TRUE WHERE id=1")
	require.NoError(t, err)
	left, err := s.InspectFailureDomains(ctx, 1, "model-a")
	require.NoError(t, err)
	require.False(t, left.Eligible, "on cannot erase an actual credential error")
	right, err := s.InspectFailureDomains(ctx, 2, "model-a")
	require.NoError(t, err)
	require.True(t, right.Eligible, "a local error must not quarantine a related account")
	t.Log("old shared quota/service gates ignored; actual auth failure blocks only A and survives switch off/on")
}
