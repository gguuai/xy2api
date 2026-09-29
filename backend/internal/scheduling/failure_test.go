package scheduling

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFailureClassificationScopesAndRetry(t *testing.T) {
	a := FailureAdmission{AccountID: 1, Model: "model-a", Credential: "credential", Domains: AccountFailureDomains{AccountID: 1, QuotaPoolID: "org-a", AvailabilityPoolID: "proxy-a"}}
	now := time.Now()
	tests := []struct {
		name                 string
		e                    FailureEvidence
		scope, effect, retry string
	}{
		{"invalid input", FailureEvidence{GlobalInput: true, ReplaySafe: true}, "request", "none", "stop"},
		{"credential", FailureEvidence{Trusted: true, Status: 401, Code: "invalid_api_key", ReplaySafe: true}, "credential", "auth_block", "next_eligible"},
		{"generic 403", FailureEvidence{Trusted: true, Status: 403, ReplaySafe: true}, "request", "none", "next_eligible"},
		{"model", FailureEvidence{Trusted: true, Status: 403, Code: "model_not_allowed", ReplaySafe: true}, "account_model", "cooldown", "next_eligible"},
		{"unknown 429", FailureEvidence{Trusted: true, Status: 429, ReplaySafe: true}, "account_model", "cooldown", "next_eligible"},
		{"different pool", FailureEvidence{Trusted: true, Status: 429, SharedKind: "quota_pool", SharedPool: "other", ReplaySafe: true}, "account_model", "cooldown", "next_eligible"},
		{"declared quota", FailureEvidence{Trusted: true, Status: 429, SharedKind: "quota_pool", SharedPool: "org-a", ReplaySafe: true}, "quota_pool", "cooldown", "next_eligible"},
		{"generic 503", FailureEvidence{Trusted: true, Status: 503, ReplaySafe: true}, "account_model", "observe_failure", "next_eligible"},
		{"trusted protocol failure", FailureEvidence{Trusted: true, Status: 200, ProtocolFailure: true, ReplaySafe: true}, "account_model", "observe_failure", "next_eligible"},
		{"untrusted protocol claim", FailureEvidence{Status: 200, ProtocolFailure: true, ReplaySafe: true}, "request", "none", "next_eligible"},
		{"protocol failure after output", FailureEvidence{Trusted: true, Status: 200, ProtocolFailure: true, ReplaySafe: true, Committed: true}, "account_model", "observe_failure", "stop"},
		{"owner", FailureEvidence{Trusted: true, Status: 503, ReplaySafe: true, OwnerPinned: true}, "account_model", "observe_failure", "stop"},
		{"committed", FailureEvidence{Trusted: true, Status: 503, ReplaySafe: true, Committed: true}, "account_model", "observe_failure", "stop"},
		{"cancel retains independent 429", FailureEvidence{Trusted: true, Status: 429, ReplaySafe: true, ClientCancelled: true}, "account_model", "cooldown", "stop"},
		{"local cancel", FailureEvidence{ClientCancelled: true, ReplaySafe: true}, "request", "none", "stop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := ClassifyFailure(tt.e, a, now)
			require.Equal(t, tt.scope, d.Scope)
			require.Equal(t, tt.effect, d.Effect)
			require.Equal(t, tt.retry, d.Retry)
		})
	}
	long := now.Add(48 * time.Hour)
	d := ClassifyFailure(FailureEvidence{Trusted: true, Status: 429, ReplaySafe: true, RetryAfter: long}, a, now)
	require.Equal(t, long, d.Until)
	require.True(t, ParseFailureRetryAfter("-1", now).IsZero())
	require.Equal(t, now.Add(30*time.Second), ParseFailureRetryAfter("30", now))
}
func TestFailureClassificationCannotExpandAttemptBudget(t *testing.T) {
	l := NewAttemptLedger(RetryPolicy{}, LatencyProfile{}, time.Now(), time.Time{})
	require.NoError(t, l.BeginAttempt(1, 0, time.Now(), true))
	require.ErrorIs(t, l.BeginAttempt(1, 0, time.Now(), true), ErrAttemptBudget)
	require.NoError(t, l.BeginAttempt(2, 0, time.Now(), true))
	require.ErrorIs(t, l.BeginAttempt(3, 0, time.Now(), true), ErrAttemptBudget)
	require.NoError(t, l.BeginAttempt(3, 1, time.Now(), true))
	require.ErrorIs(t, l.BeginAttempt(4, 1, time.Now(), true), ErrAttemptBudget)
}
func domainTicket(t *testing.T, s *PostgresStore, id int64) DispatchTicket {
	t.Helper()
	a, e := s.FreezeFailureAdmission(context.Background(), id, "model-a")
	require.NoError(t, e)
	r := testDispatch(id, "")
	r.Failure = &a
	ticket, e := s.BeginDispatch(context.Background(), r)
	require.NoError(t, e)
	return ticket
}
func TestFailureDomainsPostgresUnknownProbeRemainsAccountLocal(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	for _, id := range []int64{1, 3} {
		_, e := s.PutFailureDomains(ctx, AccountFailureDomains{AccountID: id, QuotaPoolID: "org-a"}, 9)
		require.NoError(t, e)
	}
	ticket := domainTicket(t, s, 1)
	d := ClassifyFailure(FailureEvidence{Status: 429, Trusted: true, SharedKind: "quota_pool", SharedPool: "org-a", ReplaySafe: true}, *ticket.Failure, time.Now())
	require.NoError(t, s.SettleAttempt(ctx, ticket.TicketID, "upstream_error", false))
	require.NoError(t, s.ApplyFailureFeedback(ctx, ticket.TicketID, d, false))
	peer, e := s.InspectFailureDomains(ctx, 3, "model-a")
	require.NoError(t, e)
	require.True(t, peer.Eligible, "retired shared configuration must not block another account")
	_, e = db.Exec("UPDATE scheduling_failure_gates SET ready_after=NOW()-INTERVAL '1 second' WHERE gate_key=$1", d.Key)
	require.NoError(t, e)
	probe := domainTicket(t, s, 1)
	require.Contains(t, probe.Failure.ProbeVersions, d.Key)
	// Redis can be absent/flushed or its lease expired: persisted PG occupancy wins.
	a, e := s.FreezeFailureAdmission(ctx, 1, "model-a")
	require.NoError(t, e)
	r := testDispatch(1, "")
	r.Failure = &a
	_, e = s.BeginDispatch(ctx, r)
	require.ErrorIs(t, e, ErrFailureDomainBlocked)
	require.NoError(t, s.MarkAttemptUnknown(ctx, probe.TicketID))
	_, e = s.BeginDispatch(ctx, r)
	require.ErrorIs(t, e, ErrFailureDomainBlocked)
	require.NoError(t, s.SettleAttempt(ctx, probe.TicketID, "completed", false))
	require.NoError(t, s.ApplyFailureFeedback(ctx, probe.TicketID, FailureDecision{Effect: "none"}, true))
	_, e = s.BeginDispatch(ctx, r)
	require.NoError(t, e)
}
func TestFailureDomainsPostgresStaleCredentialAndMembership(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	ticket := domainTicket(t, s, 1)
	_, e := db.Exec("UPDATE accounts SET credentials='{\"api_key\":\"replacement-fixture\"}'::jsonb WHERE id=1")
	require.NoError(t, e)
	d := ClassifyFailure(FailureEvidence{Trusted: true, Status: 401, Code: "invalid_api_key", ReplaySafe: true}, *ticket.Failure, time.Now())
	require.NoError(t, s.ApplyFailureFeedback(ctx, ticket.TicketID, d, false))
	state, e := s.InspectFailureDomains(ctx, 1, "model-a")
	require.NoError(t, e)
	require.True(t, state.Eligible)
	var stale bool
	require.NoError(t, db.QueryRow("SELECT (evidence->>'stale_identity')::boolean FROM scheduling_failure_audit WHERE ticket_id=$1", ticket.TicketID).Scan(&stale))
	require.True(t, stale)
	_, e = s.PutFailureDomains(ctx, AccountFailureDomains{AccountID: 3, QuotaPoolID: "old-org"}, 9)
	require.NoError(t, e)
	old := domainTicket(t, s, 3)
	_, e = s.PutFailureDomains(ctx, AccountFailureDomains{AccountID: 3, Version: 1, QuotaPoolID: "new-org"}, 9)
	require.NoError(t, e)
	shared := ClassifyFailure(FailureEvidence{Trusted: true, Status: 429, SharedKind: "quota_pool", SharedPool: "old-org", ReplaySafe: true}, *old.Failure, time.Now())
	require.NoError(t, s.ApplyFailureFeedback(ctx, old.TicketID, shared, false))
	state, e = s.InspectFailureDomains(ctx, 3, "model-a")
	require.NoError(t, e)
	require.False(t, state.Eligible, "retired shared membership edits cannot erase an account-local cooldown")
	require.Equal(t, "account_model", shared.Scope)
}
func TestFailureDomainsPostgresConcurrentRecovery(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	for _, id := range []int64{1, 3} {
		_, e := s.PutFailureDomains(ctx, AccountFailureDomains{AccountID: id, QuotaPoolID: "pool"}, 9)
		require.NoError(t, e)
	}
	t1 := domainTicket(t, s, 1)
	d := ClassifyFailure(FailureEvidence{Trusted: true, Status: 429, SharedKind: "quota_pool", SharedPool: "pool", ReplaySafe: true}, *t1.Failure, time.Now())
	require.NoError(t, s.SettleAttempt(ctx, t1.TicketID, "upstream_error", false))
	require.NoError(t, s.ApplyFailureFeedback(ctx, t1.TicketID, d, false))
	_, e := db.Exec("UPDATE scheduling_failure_gates SET ready_after=NOW()-INTERVAL '1 second' WHERE gate_key=$1", d.Key)
	require.NoError(t, e)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []int64{1, 1} {
		a, e := s.FreezeFailureAdmission(ctx, id, "model-a")
		require.NoError(t, e)
		wg.Add(1)
		go func(a FailureAdmission) {
			defer wg.Done()
			<-start
			r := testDispatch(a.AccountID, "")
			r.Failure = &a
			_, e := s.BeginDispatch(ctx, r)
			results <- e
		}(a)
	}
	close(start)
	wg.Wait()
	close(results)
	won, blocked := 0, 0
	for err := range results {
		if err == nil {
			won++
		} else if errors.Is(err, ErrFailureDomainBlocked) {
			blocked++
		} else {
			t.Fatal(err)
		}
	}
	require.Equal(t, 1, won)
	require.Equal(t, 1, blocked)
}
func TestFailureDomainsUnknownResolutionRequiresEvidenceAndCAS(t *testing.T) {
	s, _ := isolatedControlStore(t)
	ctx := context.Background()
	ticket := domainTicket(t, s, 1)
	require.NoError(t, s.MarkAttemptUnknown(ctx, ticket.TicketID))
	r := UnknownResolution{ExpectedVersion: 0, Source: "timer_expired", Reference: "provider-event-123", ObservedAt: time.Now(), Outcome: "completed"}
	_, e := s.ResolveUnknownWithEvidence(ctx, ticket.TicketID, r, 9)
	require.ErrorIs(t, e, ErrInvalidControl)
	r.Source = "provider_receipt"
	v, e := s.ResolveUnknownWithEvidence(ctx, ticket.TicketID, r, 9)
	require.NoError(t, e)
	require.Equal(t, "settled", v.State)
	require.EqualValues(t, 1, v.Version)
	_, e = s.ResolveUnknownWithEvidence(ctx, ticket.TicketID, r, 9)
	require.NoError(t, e)
	r.Reference = "different-evidence"
	_, e = s.ResolveUnknownWithEvidence(ctx, ticket.TicketID, r, 9)
	require.ErrorIs(t, e, ErrVersionConflict)
}
