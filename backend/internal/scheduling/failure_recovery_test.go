package scheduling

import (
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestFailureRecoveryVerifiedCASAndUnknownProbe(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	config, err := s.PutFailureDomains(ctx, AccountFailureDomains{AccountID: 1, QuotaPoolID: "org-a"}, 9)
	require.NoError(t, err)
	ticket := domainTicket(t, s, 1)
	decision := ClassifyFailure(FailureEvidence{Trusted: true, Status: 429, Code: "insufficient_quota", SharedKind: "quota_pool", SharedPool: "org-a", ReplaySafe: true}, *ticket.Failure, time.Now())
	require.True(t, decision.Hard)
	require.NoError(t, s.SettleAttempt(ctx, ticket.TicketID, "upstream_error", false))
	require.NoError(t, s.ApplyFailureFeedback(ctx, ticket.TicketID, decision, false))
	state, err := s.InspectFailureDomains(ctx, 1, "model-a")
	require.NoError(t, err)
	require.False(t, state.Eligible)
	var gate FailureGate
	for _, g := range state.Gates {
		if g.Key == decision.Key {
			gate = g
		}
	}
	evidence := FailureRecoveryEvidence{GateKey: gate.Key, Model: "model-a", ExpectedVersion: gate.Version, DomainVersion: config.Version, Source: "provider_status", Reference: "quota-credit-confirmed-1", ObservedAt: time.Now()}
	bad := evidence
	bad.ExpectedVersion--
	_, err = s.PermitFailureRecovery(ctx, 1, bad, 9)
	require.ErrorIs(t, err, ErrVersionConflict)
	_, err = s.PermitFailureRecovery(ctx, 1, evidence, 9)
	require.NoError(t, err)
	probe := domainTicket(t, s, 1)
	require.Contains(t, probe.Failure.ProbeVersions, gate.Key)
	require.NoError(t, s.MarkAttemptUnknown(ctx, probe.TicketID))
	evidence.ExpectedVersion++
	evidence.ObservedAt = time.Now()
	_, err = s.PermitFailureRecovery(ctx, 1, evidence, 9)
	require.ErrorIs(t, err, ErrFailureDomainBlocked)
	var n int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM scheduling_failure_audit WHERE action='verified_domain_recovery'").Scan(&n))
	require.Equal(t, 1, n)
}

func TestFailureShadowUsesActualCredentialOwner(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	_, err := db.Exec(`UPDATE accounts SET platform='openai',type='oauth',credentials='{"access_token":"parent-a","chatgpt_account_id":"principal-a"}',extra='{"custom_base_url_enabled":true,"custom_base_url":"https://a.example"}' WHERE id=1`)
	require.NoError(t, err)
	parent, err := s.FreezeFailureAdmission(ctx, 1, "model-a")
	require.NoError(t, err)
	child, err := s.FreezeFailureAdmission(ctx, 2, "model-a")
	require.NoError(t, err)
	require.Equal(t, int64(1), child.CredentialOwnerID)
	require.Equal(t, parent.AccountKey(), child.AccountKey(), "a known credential shadow shares credential failure, not model quota")
	require.NotEqual(t, parent.ModelKey(), child.ModelKey())
	require.Equal(t, parent.Credential, child.Credential)
	require.Equal(t, parent.HealthIdentity, child.HealthIdentity)
	ticket := domainTicket(t, s, 2)
	_, err = db.Exec(`UPDATE accounts SET credentials=jsonb_set(credentials,'{access_token}','"parent-b"') WHERE id=1`)
	require.NoError(t, err)
	current, err := s.FailureIdentityCurrent(ctx, ticket.Failure)
	require.NoError(t, err)
	require.False(t, current)
	fresh, err := s.FreezeFailureAdmission(ctx, 2, "model-a")
	require.NoError(t, err)
	require.Equal(t, child.HealthIdentity, fresh.HealthIdentity, "ordinary OAuth refresh preserves health identity")
	decision := ClassifyFailure(FailureEvidence{Trusted: true, Status: 401, Code: "invalid_token", ReplaySafe: true}, *ticket.Failure, time.Now())
	require.NoError(t, s.SettleAttempt(ctx, ticket.TicketID, "upstream_error", false))
	require.NoError(t, s.ApplyFailureFeedback(ctx, ticket.TicketID, decision, false))
	state, err := s.InspectFailureDomains(ctx, 2, "model-a")
	require.NoError(t, err)
	require.True(t, state.Eligible)
	_, err = db.Exec(`UPDATE accounts SET extra='{"custom_base_url_enabled":true,"custom_base_url":"https://b.example"}' WHERE id=1`)
	require.NoError(t, err)
	current, err = s.FailureIdentityCurrent(ctx, &fresh)
	require.NoError(t, err)
	require.False(t, current, "endpoint/extra change fences old terminal evidence")
}

func TestFailureRedisTokenFencingAfterLeaseLoss(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer func() { _ = client.Close() }()
	store := RedisFailureDomains{Client: client}
	ctx := context.Background()
	a := DispatchTicket{TicketID: "first", Failure: &FailureAdmission{ProbeVersions: map[string]int64{"one": 1, "two": 2}}}
	b := a
	b.TicketID = "second"
	require.NoError(t, store.Acquire(ctx, a))
	require.ErrorIs(t, store.Acquire(ctx, b), ErrFailureDomainBlocked)
	mini.FastForward(3 * time.Minute)
	require.NoError(t, store.Acquire(ctx, b))
	require.NoError(t, store.Release(ctx, a))
	require.ErrorIs(t, store.Acquire(ctx, a), ErrFailureDomainBlocked, "late release must not delete successor lease")
	require.NoError(t, store.Release(ctx, b))
	require.NoError(t, store.Acquire(ctx, a))
}

func TestFailureOAuthRefreshPreservesQuotaButReplacesAuthGate(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	_, err := db.Exec(`UPDATE accounts SET platform='openai',type='oauth',credentials='{"access_token":"old","chatgpt_account_id":"principal-a"}' WHERE id=1`)
	require.NoError(t, err)
	quotaTicket := domainTicket(t, s, 1)
	authTicket := domainTicket(t, s, 1)
	quota := ClassifyFailure(FailureEvidence{Trusted: true, Status: 429, ReplaySafe: true}, *quotaTicket.Failure, time.Now())
	require.NoError(t, s.SettleAttempt(ctx, quotaTicket.TicketID, "upstream_error", false))
	require.NoError(t, s.ApplyFailureFeedback(ctx, quotaTicket.TicketID, quota, false))
	// Credential rejection may arrive independently from another frozen attempt.
	auth := ClassifyFailure(FailureEvidence{Trusted: true, Status: 401, Code: "invalid_token", ReplaySafe: true}, *quotaTicket.Failure, time.Now())
	require.NotEqual(t, auth.Key, quota.Key)
	require.NoError(t, s.SettleAttempt(ctx, authTicket.TicketID, "upstream_error", false))
	require.NoError(t, s.ApplyFailureFeedback(ctx, authTicket.TicketID, auth, false))
	_, err = db.Exec(`UPDATE accounts SET credentials=jsonb_set(credentials,'{access_token}','"fresh"') WHERE id=1`)
	require.NoError(t, err)
	fresh, err := s.FreezeFailureAdmission(ctx, 1, "model-a")
	require.NoError(t, err)
	require.Equal(t, quotaTicket.Failure.ModelKey(), fresh.ModelKey(), "token refresh cannot erase quota cooldown")
	require.NotEqual(t, quotaTicket.Failure.AccountKey(), fresh.AccountKey(), "new actual credential has its own authentication gate")
	state, err := s.InspectFailureDomains(ctx, 1, "model-a")
	require.NoError(t, err)
	require.False(t, state.Eligible)
	for _, gate := range state.Gates {
		require.NotEqual(t, auth.Key, gate.Key)
	}
	_, err = db.Exec("UPDATE scheduling_failure_gates SET ready_after=NULL WHERE gate_key=$1", quota.Key)
	require.NoError(t, err)
	state, err = s.InspectFailureDomains(ctx, 1, "model-a")
	require.NoError(t, err)
	require.True(t, state.Eligible)
}
