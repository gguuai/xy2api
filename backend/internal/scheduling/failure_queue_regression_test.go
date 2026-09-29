package scheduling

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Only existing APIs are used, so the same correctness test runs on the
// pre-fix candidate as well as the repaired candidate.
func TestFailureFeedbackDeletedIdentityDoesNotStarveLiveGate(t *testing.T) {
	for _, logicalID := range []int64{1, 2} {
		t.Run(map[int64]string{1: "deleted_account", 2: "deleted_credential_parent"}[logicalID], func(t *testing.T) {
			s, db := isolatedControlStore(t)
			ctx := context.Background()
			for i := 0; i < 100; i++ {
				ticket := domainTicket(t, s, logicalID)
				require.NoError(t, s.SettleAttempt(ctx, ticket.TicketID, "completed", false))
				require.NoError(t, s.RecordFailureIntent(ctx, ticket.TicketID, FailureDecision{Effect: "none"}, true))
			}
			survivor := domainTicket(t, s, 3)
			decision := ClassifyFailure(FailureEvidence{Trusted: true, Status: 429, ReplaySafe: true}, *survivor.Failure, time.Now())
			require.NoError(t, s.SettleAttempt(ctx, survivor.TicketID, "upstream_error", false))
			require.NoError(t, s.RecordFailureIntent(ctx, survivor.TicketID, decision, false))
			_, err := db.ExecContext(ctx, "UPDATE accounts SET deleted_at=NOW() WHERE id=1")
			require.NoError(t, err)
			for cycle := 0; cycle < 3; cycle++ {
				n, e := s.ReconcileFailureIntents(ctx)
				t.Logf("cycle=%d reconciled=%d error=%v", cycle, n, e)
			}
			state, err := s.InspectFailureDomains(ctx, 3, "model-a")
			require.NoError(t, err)
			require.False(t, state.Eligible, "a deleted identity must not starve a later live account's actual 429 gate")
			var before int64
			require.NoError(t, db.QueryRow("SELECT version FROM scheduling_failure_gates WHERE gate_key=$1", decision.Key).Scan(&before))
			require.NoError(t, s.ApplyFailureFeedback(ctx, survivor.TicketID, decision, false))
			n, err := s.ReconcileFailureIntents(ctx)
			require.NoError(t, err)
			require.Zero(t, n)
			var after int64
			require.NoError(t, db.QueryRow("SELECT version FROM scheduling_failure_gates WHERE gate_key=$1", decision.Key).Scan(&after))
			require.Equal(t, before, after, "replay must not extend cooldown or increment the gate twice")
			var audited int
			require.NoError(t, db.QueryRow("SELECT count(*) FROM scheduling_failure_audit WHERE action='failure_feedback'").Scan(&audited))
			require.Equal(t, 101, audited, "deleted identities must finish with an idempotent audit")
		})
	}
}
