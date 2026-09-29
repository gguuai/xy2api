//go:build unit

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

// Direct continuation owners have no SWRR selection receipt. A recovery probe
// obtained while admitting their actual model must still be released if a later
// ledger/control check prevents the first physical dispatch.
func TestAccountPoolOwnerUnsentAdmissionReleasesRecoveryProbe(t *testing.T) {
	for _, rejection := range []string{"postgres_control_gate", "committed_request_ledger"} {
		t.Run(rejection, func(t *testing.T) {
			s, db, _, _ := controlledIntegration(t, false)
			ctx, r := controlledIntegrationRequest(t, s)
			r.mu.Lock()
			r.owner, r.ownerAccountID = true, 1
			r.mu.Unlock()
			actual, err := s.Store.FreezeFailureAdmission(ctx, 1, r.Policy.Model)
			require.NoError(t, err)
			health := scheduling.HealthSnapshot{Generation: "owner-recovery", State: scheduling.HealthHalfOpen, StageRevision: 4}
			raw, err := json.Marshal(health)
			require.NoError(t, err)
			key := scheduling.HealthRedisKey(1, actual.Model, r.Profile, r.Reasoning, controlledBucket(r), r.Protocol, actual.HealthIdentity)
			require.NoError(t, s.redis.HSet(ctx, key, "snapshot", raw).Err())
			wantErr := scheduling.ErrControlBlocked
			if rejection == "postgres_control_gate" {
				_, err = db.ExecContext(ctx, "UPDATE accounts SET schedulable=FALSE WHERE id=1")
				require.NoError(t, err)
			} else {
				r.Ledger.MarkSemanticCommit()
				wantErr = scheduling.ErrCommitted
			}
			d, err := s.beginDispatch(ctx, 1, 10)
			require.ErrorIs(t, err, wantErr)
			require.Nil(t, d)
			r.mu.Lock()
			decision, pending := r.Decision, r.decisionPending
			r.mu.Unlock()
			require.NotEmpty(t, decision.ProbeToken, "test must reach actual-model probe acquisition before the later guard rejects it")
			require.Empty(t, decision.ReservationID, "direct owner should not borrow an ordinary weighted reservation")
			require.False(t, pending)
			require.Zero(t, r.Ledger.Snapshot().Attempts)
			r.Close()
			parts := strings.Split(decision.ProbeToken, "|")
			require.Len(t, parts, 3)
			require.Eventually(t, func() bool {
				n, err := s.redis.Exists(context.Background(), parts[0], parts[1]).Result()
				return err == nil && n == 0
			}, time.Second, 5*time.Millisecond)
			var tickets, sent int
			require.NoError(t, db.QueryRow("SELECT COUNT(*),COUNT(*) FILTER (WHERE metrics ? 'sent_at') FROM scheduling_attempts WHERE request_id=$1", r.ID).Scan(&tickets, &sent))
			require.Zero(t, tickets)
			require.Zero(t, sent)
			t.Logf("OWNER_UNSENT rejection=%s actual_model_probe_acquired=true probe_released=true ledger_attempts=0 tickets=%d sent=%d", rejection, tickets, sent)
		})
	}
}
