package scheduling

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestFailureTerminalIntentSurvivesCrashBeforeSettlement(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	_, err := s.PutFailureDomains(ctx, AccountFailureDomains{AccountID: 1, QuotaPoolID: "org-a"}, 9)
	require.NoError(t, err)
	ticket := domainTicket(t, s, 1)
	decision := ClassifyFailure(FailureEvidence{Trusted: true, Status: 429, Code: "insufficient_quota", SharedKind: "quota_pool", SharedPool: "org-a", ReplaySafe: true}, *ticket.Failure, time.Now())
	require.NoError(t, s.RecordTerminalFailureIntent(ctx, ticket.TicketID, "upstream_error", "remote_terminal", false, decision, false))
	// Re-create the service store: no in-memory Finish callback survives.
	restarted := NewPostgresStore(db)
	n, err := restarted.ReconcileTerminalIntents(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	n, err = restarted.ReconcileFailureIntents(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	state, err := restarted.InspectFailureDomains(ctx, 1, "model-a")
	require.NoError(t, err)
	require.False(t, state.Eligible)
	n, err = restarted.ReconcileFailureIntents(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	var gates, audits int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM scheduling_failure_gates WHERE blocked").Scan(&gates))
	require.Equal(t, 1, gates)
	require.NoError(t, db.QueryRow("SELECT count(*) FROM scheduling_failure_audit WHERE ticket_id=$1 AND action='failure_feedback'", ticket.TicketID).Scan(&audits))
	require.Equal(t, 1, audits)
}

func TestFailureIdentityGuardFencesConcurrentCredentialReplacement(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	frozen, err := s.FreezeFailureAdmission(ctx, 1, "model-a")
	require.NoError(t, err)
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, e := s.WithCurrentFailureIdentity(ctx, &frozen, func() error { close(entered); <-release; return nil })
		finished <- e
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("guard did not acquire identity fence")
	}
	deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err = db.ExecContext(deadline, `UPDATE accounts SET credentials='{"api_key":"new"}' WHERE id=1`)
	cancel()
	require.Error(t, err, "replacement must wait until terminal observation completes")
	close(release)
	require.NoError(t, <-finished)
	_, err = db.ExecContext(ctx, `UPDATE accounts SET credentials='{"api_key":"new"}' WHERE id=1`)
	require.NoError(t, err)
	called := false
	current, err := s.WithCurrentFailureIdentity(ctx, &frozen, func() error { called = true; return nil })
	require.NoError(t, err)
	require.False(t, current)
	require.False(t, called)
}
