package scheduling

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"os"
	"sync"
	"testing"
)

func TestSchedulingModePostgresConcurrentCASAndIndependentControls(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	snapshot, err := s.GetSchedulingMode(ctx)
	require.NoError(t, err)
	require.Equal(t, ModeSnapshot{Mode: ModeControlled}, snapshot)
	for _, version := range []int64{0, 1} {
		ready := make(chan struct{})
		results := make(chan error, 8)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-ready
				mode := ModeControlled
				if i%2 == 0 {
					mode = ModeSub2API
				}
				_, e := NewPostgresStore(db).PutSchedulingMode(ctx, mode, version)
				results <- e
			}(i)
		}
		close(ready)
		wg.Wait()
		close(results)
		success, conflict := 0, 0
		for e := range results {
			if e == nil {
				success++
			} else if errors.Is(e, ErrVersionConflict) {
				conflict++
			} else {
				require.NoError(t, e)
			}
		}
		require.Equal(t, 1, success)
		require.Equal(t, 7, conflict)
		snapshot, err = s.GetSchedulingMode(ctx)
		require.NoError(t, err)
		require.Equal(t, version+1, snapshot.Version)
	}
	_, err = s.Control(ctx, ControlCommand{AccountID: 1, Action: "pause"})
	require.NoError(t, err)
	var enabled bool
	require.NoError(t, db.QueryRow("SELECT schedulable FROM accounts WHERE id=1").Scan(&enabled))
	require.True(t, enabled, "controlled pause must leave Sub2API eligibility unchanged")
	allowed, err := s.CanAdmitControl(ctx, 1, "")
	require.NoError(t, err)
	require.True(t, allowed, "archived controls cannot override the single account switch")
	_, err = db.Exec("UPDATE accounts SET schedulable=FALSE WHERE id=1")
	require.NoError(t, err)
	_, err = s.Control(ctx, ControlCommand{AccountID: 1, Action: "resume"})
	require.NoError(t, err)
	require.NoError(t, db.QueryRow("SELECT schedulable FROM accounts WHERE id=1").Scan(&enabled))
	require.False(t, enabled, "controlled resume cannot enable a generic-disabled account")
	allowed, err = s.CanAdmitControl(ctx, 1, "")
	require.NoError(t, err)
	require.False(t, allowed)
	_, err = s.BeginDispatch(ctx, testDispatch(1, ""))
	require.ErrorIs(t, err, ErrControlBlocked)
}
func TestSchedulingModeMigrationPreservesAmbiguousLegacyDisable(t *testing.T) {
	_, db := isolatedControlStore(t)
	_, err := db.Exec("UPDATE accounts SET schedulable=FALSE WHERE id=1; INSERT INTO scheduling_controls(scope,subject_id,state,updated_at) SELECT 'logical_account',id,'PAUSED',updated_at FROM accounts WHERE id=1 ON CONFLICT(scope,subject_id) DO UPDATE SET state='PAUSED',updated_at=EXCLUDED.updated_at")
	require.NoError(t, err)
	raw, err := os.ReadFile("../../migrations/263_dual_scheduling_mode.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(raw))
	require.NoError(t, err)
	var enabled bool
	require.NoError(t, db.QueryRow("SELECT schedulable FROM accounts WHERE id=1").Scan(&enabled))
	require.False(t, enabled)
}
func TestSchedulingModeGenericDisableOverridesSessionGrant(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	ticket, err := s.BeginDispatch(ctx, testDispatch(1, "keep-owner"))
	require.NoError(t, err)
	require.NoError(t, s.SettleAttempt(ctx, ticket.TicketID, "completed", false))
	_, err = s.Control(ctx, ControlCommand{AccountID: 1, Action: "session_drain", SessionDurationSeconds: 300, SessionMaxTurns: 3})
	require.NoError(t, err)
	allowed, err := s.CanAdmitControl(ctx, 1, "keep-owner")
	require.NoError(t, err)
	require.True(t, allowed)
	ticket, err = s.BeginDispatch(ctx, testDispatch(1, "keep-owner"))
	require.NoError(t, err)
	require.NoError(t, s.SettleAttempt(ctx, ticket.TicketID, "completed", false))
	_, err = db.Exec("UPDATE accounts SET schedulable=FALSE WHERE id=1")
	require.NoError(t, err)
	allowed, err = s.CanAdmitControl(ctx, 1, "keep-owner")
	require.NoError(t, err)
	require.False(t, allowed)
	_, err = s.BeginDispatch(ctx, testDispatch(1, "keep-owner"))
	require.ErrorIs(t, err, ErrControlBlocked)
	var remaining int
	require.NoError(t, db.QueryRow("SELECT remaining_turns FROM scheduling_session_grants WHERE subject_id=1 AND session_id='keep-owner'").Scan(&remaining))
	require.Equal(t, 3, remaining, "retired grants cannot be spent or override generic disable")
}
