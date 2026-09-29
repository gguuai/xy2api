//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestOneClickAccountSwitchLetsActiveSSEComplete(t *testing.T) {
	s, db, _, accounts := controlledIntegration(t, false)
	ctx, r := controlledIntegrationRequest(t, s)
	selected := controlledPick(t, s, ctx, r, accounts)
	finish := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(finish) }) })
	var cancelled atomic.Bool
	resp, err := controlledHTTP(t, s, ctx, selected.ID, func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"first\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
		case <-request.Context().Done():
			cancelled.Store(true)
		}
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	t.Cleanup(func() { release.Do(func() { close(finish) }) })
	_, err = db.ExecContext(ctx, "UPDATE accounts SET schedulable=FALSE WHERE id=$1", selected.ID)
	require.NoError(t, err)
	nextCtx, nextRequest := controlledIntegrationRequest(t, s)
	next := controlledPick(t, s, nextCtx, nextRequest, accounts)
	require.NotEqual(t, selected.ID, next.ID, "independent requests must move to another account immediately")
	time.Sleep(300 * time.Millisecond)
	require.False(t, cancelled.Load(), "turning off does not cancel an accepted stream")
	release.Do(func() { close(finish) })
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.True(t, strings.Contains(string(body), "response.completed"))
	require.False(t, cancelled.Load())
	var outcome string
	require.Eventually(t, func() bool {
		return db.QueryRow("SELECT outcome FROM scheduling_attempts WHERE request_id=$1", r.ID).Scan(&outcome) == nil && outcome == "completed"
	}, time.Second, 10*time.Millisecond)
	t.Logf("SSE account=%d completed after off, independent request selected=%d, cancelled=false", selected.ID, next.ID)
}

// Exercise real dispatch admission through the production WS frame wrapper.
// The staged transport records frames actually written to the upstream boundary.
func TestOneClickAccountSwitchLetsActiveWSTurnFinishAndRejectsNextTurn(t *testing.T) {
	s, db, _, accounts := controlledIntegration(t, false)
	firstCtx, firstRequest := controlledIntegrationRequest(t, s)
	firstRequest.Protocol = "ws"
	firstRequest.SessionID = "one-click-ws-owner"
	selected := controlledPick(t, s, firstCtx, firstRequest, accounts)
	raw := newStagedPassthroughConn()
	var nextRequest *ControlledRequest
	var flags []bool
	wrapper := newOpenAIWSControlledPassthroughFrameConn(context.Background(), raw, func(_ []byte, newTurn bool) (context.Context, controlledPassthroughAttempt, error) {
		flags = append(flags, newTurn)
		turnCtx := firstCtx
		if newTurn {
			turnCtx, nextRequest = controlledIntegrationRequest(t, s)
			nextRequest.Protocol = "ws"
			nextRequest.SessionID = firstRequest.SessionID
			nextRequest.owner, nextRequest.ownerAccountID = true, selected.ID
		}
		dispatch, err := s.beginDispatch(turnCtx, selected.ID, 10)
		return turnCtx, dispatch, err
	})
	defer func() { require.NoError(t, wrapper.Close()) }()
	first := []byte("{\"type\":\"response.create\",\"model\":\"test-model\"}")
	require.NoError(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, first))
	require.Equal(t, first, <-raw.writes)
	_, err := db.Exec("UPDATE accounts SET schedulable=FALSE WHERE id=$1", selected.ID)
	require.NoError(t, err)
	raw.Send("{\"type\":\"response.output_text.delta\",\"delta\":\"finish accepted turn\"}")
	_, payload, err := wrapper.ReadFrame(context.Background())
	require.NoError(t, err)
	wrapper.commitOutput(payload)
	raw.Send("{\"type\":\"response.completed\",\"response\":{\"id\":\"owned-response\",\"status\":\"completed\"}}")
	_, payload, err = wrapper.ReadFrame(context.Background())
	require.NoError(t, err)
	require.Contains(t, string(payload), "response.completed")
	wrapper.commitOutput(payload)
	wrapper.finishTurn("response.completed", true, nil)
	require.NoError(t, wrapper.ctx.Err(), "off cannot cancel the accepted turn or its terminal frame")
	next := []byte("{\"type\":\"response.create\",\"previous_response_id\":\"owned-response\",\"input\":[]}")
	require.ErrorIs(t, wrapper.WriteFrame(context.Background(), coderws.MessageText, next), scheduling.ErrControlBlocked)
	require.Equal(t, []bool{false, true}, flags)
	require.Empty(t, raw.writes, "disabled owner must reject a new turn before any upstream write")
	require.Equal(t, selected.ID, nextRequest.ownerAccountID, "strong owner cannot move to a peer")
	require.Zero(t, nextRequest.Ledger.Snapshot().Attempts)
	var attempts int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM scheduling_attempts WHERE account_id=$1", selected.ID).Scan(&attempts))
	require.Equal(t, 1, attempts, "the rejected new turn cannot create a second ticket")
	var state, outcome string
	require.NoError(t, db.QueryRow("SELECT state,outcome FROM scheduling_attempts WHERE request_id=$1", firstRequest.ID).Scan(&state, &outcome))
	require.Equal(t, "settled", state)
	require.Equal(t, "response.completed", outcome)
	t.Logf("WS owner=%d first turn=response.completed after off; next turn=control_blocked; upstream creates=1", selected.ID)
}
