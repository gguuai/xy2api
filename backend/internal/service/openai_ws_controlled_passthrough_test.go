package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

type controlledPassthroughTestAttempt struct {
	ctx                                 context.Context
	cancel                              context.CancelFunc
	mu                                  sync.Mutex
	sent, observed, committed, finished int
	markErr                             error
	terminal                            bool
	outcome                             string
	onFinish                            func()
}

func newControlledPassthroughTestAttempt() *controlledPassthroughTestAttempt {
	ctx, cancel := context.WithCancel(context.Background())
	return &controlledPassthroughTestAttempt{ctx: ctx, cancel: cancel}
}
func (d *controlledPassthroughTestAttempt) Context() context.Context { return d.ctx }
func (d *controlledPassthroughTestAttempt) MarkSent() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.markErr != nil {
		return d.markErr
	}
	d.sent++
	return nil
}
func (d *controlledPassthroughTestAttempt) ObserveFrame([]byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.observed++
}
func (d *controlledPassthroughTestAttempt) CommitOutput([]byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.committed++
}
func (d *controlledPassthroughTestAttempt) Finish(outcome string, terminal bool, _ error) {
	d.mu.Lock()
	d.finished++
	d.terminal = terminal
	d.outcome = outcome
	d.mu.Unlock()
	if d.onFinish != nil {
		d.onFinish()
	}
	d.cancel()
}
func (d *controlledPassthroughTestAttempt) counts() (int, int, int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sent, d.observed, d.committed, d.finished
}

func TestControlledPassthroughPauseRefusesNextDispatchWithoutRewritingOwner(t *testing.T) {
	raw := newStagedPassthroughConn()
	first := newControlledPassthroughTestAttempt()
	paused := false
	var newTurnFlags []bool
	wrapper := newOpenAIWSControlledPassthroughFrameConn(context.Background(), raw, func(body []byte, newTurn bool) (context.Context, controlledPassthroughAttempt, error) {
		newTurnFlags = append(newTurnFlags, newTurn)
		if paused {
			return nil, nil, scheduling.ErrNoCandidate
		}
		return context.Background(), first, nil
	})
	defer wrapper.Close()
	initial := []byte(`{"type":"response.create","model":"my-model"}`)
	require.NoError(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, initial))
	require.Equal(t, initial, <-raw.writes)
	paused = true
	raw.Send(`{"type":"response.output_text.delta","delta":"finish original"}`)
	_, payload, err := wrapper.ReadFrame(context.Background())
	require.NoError(t, err)
	wrapper.commitOutput(payload)
	wrapper.finishTurn("response.completed", true, nil)
	require.NoError(t, wrapper.ctx.Err(), "normal terminal cancellation must not end the session")
	next := []byte(`{"type":"response.create","previous_response_id":"opaque-owner","input":[]}`)
	require.ErrorIs(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, next), scheduling.ErrNoCandidate)
	require.Equal(t, []bool{false, true}, newTurnFlags)
	require.Empty(t, raw.writes, "paused next turn must never reach upstream")
	sent, observed, committed, finished := first.counts()
	require.Equal(t, 1, sent)
	require.Equal(t, 1, observed)
	require.Equal(t, 1, committed)
	require.Equal(t, 1, finished)
	require.Contains(t, string(next), "opaque-owner", "admission must not strip a protocol owner")
}

func TestControlledPassthroughCountsOnlyActualCreatesAndRejectsOverlap(t *testing.T) {
	raw := newStagedPassthroughConn()
	attempt := newControlledPassthroughTestAttempt()
	calls := 0
	wrapper := newOpenAIWSControlledPassthroughFrameConn(context.Background(), raw, func([]byte, bool) (context.Context, controlledPassthroughAttempt, error) {
		calls++
		return context.Background(), attempt, nil
	})
	defer wrapper.Close()
	require.NoError(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"session.update","session":{"model":"m"}}`)))
	require.Zero(t, calls)
	<-raw.writes
	body := []byte(`{"type":" response.create ","model":"m"}`)
	require.NoError(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, body))
	<-raw.writes
	require.Equal(t, 1, calls)
	require.ErrorContains(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, body), "overlapping")
	require.Equal(t, 1, calls)
	require.Empty(t, raw.writes)
}

func TestControlledPassthroughNotSentBudgetRejectionNeverWrites(t *testing.T) {
	raw := newStagedPassthroughConn()
	attempt := newControlledPassthroughTestAttempt()
	attempt.markErr = scheduling.ErrAttemptBudget
	wrapper := newOpenAIWSControlledPassthroughFrameConn(context.Background(), raw, func([]byte, bool) (context.Context, controlledPassthroughAttempt, error) {
		return context.Background(), attempt, nil
	})
	defer wrapper.Close()
	require.ErrorIs(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"response.create"}`)), scheduling.ErrAttemptBudget)
	sent, _, _, finished := attempt.counts()
	require.Zero(t, sent)
	require.Equal(t, 1, finished)
	require.Empty(t, raw.writes)
	require.Equal(t, "not_sent", attempt.outcome)
	require.True(t, attempt.terminal)
}

func TestControlledPassthroughObservationDoesNotCommitClientOutput(t *testing.T) {
	raw := newStagedPassthroughConn()
	attempt := newControlledPassthroughTestAttempt()
	wrapper := newOpenAIWSControlledPassthroughFrameConn(context.Background(), raw, func([]byte, bool) (context.Context, controlledPassthroughAttempt, error) {
		return context.Background(), attempt, nil
	})
	defer wrapper.Close()
	require.NoError(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"response.create"}`)))
	<-raw.writes
	raw.Send(`{"type":"response.output_text.delta","delta":"hello"}`)
	_, payload, err := wrapper.ReadFrame(context.Background())
	require.NoError(t, err)
	_, observed, committed, _ := attempt.counts()
	require.Equal(t, 1, observed)
	require.Zero(t, committed)
	wrapper.commitOutput(payload)
	_, _, committed, _ = attempt.counts()
	require.Equal(t, 1, committed)
	wrapper.finishTurn("response.completed", true, nil)
	wrapper.finishTurn("duplicate_callback", true, nil)
	_, _, _, finished := attempt.counts()
	require.Equal(t, 1, finished)
}

func TestControlledPassthroughForceStopCancelsReadStartedBetweenTurns(t *testing.T) {
	raw := newStagedPassthroughConn()
	attempt := newControlledPassthroughTestAttempt()
	wrapper := newOpenAIWSControlledPassthroughFrameConn(context.Background(), raw, func([]byte, bool) (context.Context, controlledPassthroughAttempt, error) {
		return context.Background(), attempt, nil
	})
	defer wrapper.Close()
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() { close(started); _, _, err := wrapper.ReadFrame(context.Background()); result <- err }()
	<-started
	require.NoError(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"response.create"}`)))
	<-raw.writes
	attempt.cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("new turn cancellation did not reach the already waiting upstream read")
	}
}

func TestControlledPassthroughFirstTerminalKeepsNextTurnLive(t *testing.T) {
	raw := newStagedPassthroughConn()
	attempts := []*controlledPassthroughTestAttempt{newControlledPassthroughTestAttempt(), newControlledPassthroughTestAttempt()}
	calls := 0
	wrapper := newOpenAIWSControlledPassthroughFrameConn(context.Background(), raw, func([]byte, bool) (context.Context, controlledPassthroughAttempt, error) {
		d := attempts[calls]
		calls++
		return NewControlledRequestContext(context.Background(), "ws"), d, nil
	})
	defer wrapper.Close()
	body := []byte(`{"type":"response.create","previous_response_id":"owner"}`)
	require.NoError(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, body))
	<-raw.writes
	wrapper.finishTurn("response.completed", true, nil)
	require.NoError(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, body))
	require.Equal(t, body, <-raw.writes)
	require.NoError(t, wrapper.ctx.Err())
	require.NoError(t, attempts[1].Context().Err())
	require.Equal(t, 2, calls)
}

func TestControlledPassthroughUnconfirmedCloseDoesNotPretendTerminal(t *testing.T) {
	raw := newStagedPassthroughConn()
	attempt := newControlledPassthroughTestAttempt()
	wrapper := newOpenAIWSControlledPassthroughFrameConn(context.Background(), raw, func([]byte, bool) (context.Context, controlledPassthroughAttempt, error) {
		return context.Background(), attempt, nil
	})
	require.NoError(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"response.create"}`)))
	<-raw.writes
	require.NoError(t, wrapper.Close())
	_, _, _, finished := attempt.counts()
	require.Equal(t, 1, finished)
	require.False(t, attempt.terminal)
	require.Equal(t, "connection_closed", attempt.outcome)
}

func TestControlledPassthroughCancellationDistinguishesClippedBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &controlledDispatch{ctx: ctx, timeout: true, request: &ControlledRequest{Model: "m", Profile: scheduling.LatencyProfile{AttemptTimeoutMS: 12000}}}
	var timeout *openAIWSPassthroughFirstOutputTimeoutError
	require.ErrorAs(t, controlledPassthroughCancelCause(d), &timeout)
	require.Equal(t, 12*time.Second, timeout.deadline.timeout)
	d.clipped = true
	err := controlledPassthroughCancelCause(d)
	require.EqualError(t, err, "first_output_budget_exhausted")
	require.False(t, errors.As(err, &timeout))
}

func TestControlledPassthroughRejectedNextTurnReleasesItsRequest(t *testing.T) {
	for _, failPrepare := range []bool{false, true} {
		t.Run(map[bool]string{false: "mark_sent", true: "prepare"}[failPrepare], func(t *testing.T) {
			raw := newStagedPassthroughConn()
			first := newControlledPassthroughTestAttempt()
			rejected := newControlledPassthroughTestAttempt()
			rejected.markErr = scheduling.ErrAttemptBudget
			initialClosed, nextClosed := 0, 0
			wrapper := newOpenAIWSControlledPassthroughFrameConn(context.Background(), raw, func(_ []byte, next bool) (context.Context, controlledPassthroughAttempt, error) {
				ctx := NewControlledRequestContext(context.Background(), "ws")
				r := controlledRequest(ctx)
				if !next {
					r.finish = func() { initialClosed++ }
					return ctx, first, nil
				}
				r.finish = func() { nextClosed++ }
				if failPrepare {
					return ctx, nil, scheduling.ErrAttemptBudget
				}
				return ctx, rejected, nil
			})
			defer wrapper.Close()
			body := []byte(`{"type":"response.create"}`)
			require.NoError(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, body))
			<-raw.writes
			wrapper.finishTurn("response.completed", true, nil)
			require.ErrorIs(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, body), scheduling.ErrAttemptBudget)
			require.Zero(t, initialClosed, "initial handler owns the first ledger")
			require.Equal(t, 1, nextClosed, "a rejected new turn must release its own ledger")
			require.Empty(t, raw.writes)
		})
	}
}

func TestControlledPassthroughUsageWaitsForItsOwnSettledTicket(t *testing.T) {
	raw := newStagedPassthroughConn()
	contexts := []context.Context{NewControlledRequestContext(context.Background(), "ws"), NewControlledRequestContext(context.Background(), "ws")}
	ids := []string{"first-ticket", "next-ticket"}
	calls := 0
	wrapper := newOpenAIWSControlledPassthroughFrameConn(context.Background(), raw, func(_ []byte, _ bool) (context.Context, controlledPassthroughAttempt, error) {
		i := calls
		calls++
		d := newControlledPassthroughTestAttempt()
		d.onFinish = func() { controlledRequest(contexts[i]).history = []SchedulingAttemptTrace{{AttemptID: ids[i]}} }
		return contexts[i], d, nil
	})
	defer wrapper.Close()
	var billed []string
	for index := range contexts {
		require.NoError(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"response.create"}`)))
		<-raw.writes
		wrapper.afterTurnSettlement(func(ctx context.Context) { billed = append(billed, SchedulingAttemptIDFromContext(ctx)) })
		require.Len(t, billed, index, "usage must wait for dispatch settlement")
		wrapper.finishTurn("response.completed", true, nil)
		wrapper.finishTurn("duplicate", true, nil)
		require.Equal(t, ids[:index+1], billed)
	}
	// A bare terminal can be finalized by Relay after it closes the transport.
	require.NoError(t, wrapper.Close())
	lateID := ""
	wrapper.afterTurnSettlement(func(ctx context.Context) { lateID = SchedulingAttemptIDFromContext(ctx) })
	require.Equal(t, "next-ticket", lateID)
}
