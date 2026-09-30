//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestNonstreamReadErrorSafety(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"unexpected_eof", io.ErrUnexpectedEOF, true},
		{"reset", fmt.Errorf("read: %w", syscall.ECONNRESET), true},
		{"closed", net.ErrClosed, true},
		{"attempt_timeout", context.DeadlineExceeded, true},
		{"cancelled", context.Canceled, false},
		{"too_large", ErrUpstreamResponseBodyTooLarge, false},
		{"unknown", errors.New("invalid payload"), false},
		{"success", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nonstreamReadError(context.Background(), &http.Response{StatusCode: 200}, tc.err)
			var failover *UpstreamFailoverError
			require.Equal(t, tc.retry, errors.As(got, &failover))
			if tc.err != nil {
				require.ErrorIs(t, got, tc.err)
			} else {
				require.NoError(t, got)
			}
		})
	}
	for _, name := range []string{"cancelled", "deadline", "owner", "unsafe", "semantic", "committed", "budget", "request_error", "nil_response"} {
		t.Run(name, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := NewControlledRequestContext(parent, "responses")
			r := controlledRequest(ctx)
			r.Policy = scheduling.NormalizePolicy(scheduling.Policy{})
			r.Policy.Retry.MaxAttempts = 1
			r.Ledger = scheduling.NewAttemptLedger(r.Policy.Retry, scheduling.LatencyProfile{}, time.Now(), time.Time{})
			resp := &http.Response{StatusCode: 200}
			switch name {
			case "cancelled":
				cancel()
			case "deadline":
				r.Ledger = scheduling.NewAttemptLedger(r.Policy.Retry, scheduling.LatencyProfile{}, time.Now(), time.Now().Add(-time.Second))
			case "owner":
				r.owner = true
			case "unsafe":
				r.ReplaySafe = false
			case "semantic":
				r.semanticAt = time.Now()
			case "committed":
				r.Ledger.MarkSemanticCommit()
			case "budget":
				require.NoError(t, r.Ledger.BeginAttempt(1, 0, time.Now(), true))
			case "request_error":
				resp.StatusCode = 400
			case "nil_response":
				resp = nil
			}
			before := r.Ledger.Snapshot()
			require.Same(t, io.ErrUnexpectedEOF, nonstreamReadError(ctx, resp, io.ErrUnexpectedEOF))
			require.Equal(t, before, r.Ledger.Snapshot(), "classification must not mutate the budget")
		})
	}
}

func TestControlledProtocolFailureEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, payload      string
		provider, excluded bool
	}{
		{"responses", "{\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\"}}}", true, false},
		{"plain_json_error", `{"error":{"code":"server_error"}}`, true, false},
		{"native_response_failed", `{"object":"response","status":"failed","output":[],"error":{"code":"server_error"}}`, true, false},
		{"native_response_incomplete", `{"object":"response","status":"incomplete","error":{"code":"server_error"}}`, false, true},
		{"native_response_cancelled", `{"object":"response","status":"cancelled","error":{"code":"server_error"}}`, false, true},
		{"native_response_canceled", `{"object":"response","status":"canceled","error":{"code":"server_error"}}`, false, true},
		{"anthropic", "{\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}", true, false},
		{"null_code", "{\"type\":\"error\",\"error\":{\"code\":null,\"type\":\"server_error\"}}", true, false},
		{"invalid_request", "{\"type\":\"error\",\"error\":{\"code\":\"server_error\",\"type\":\"invalid_request_error\"}}", false, true},
		{"incomplete", "{\"type\":\"response.incomplete\",\"response\":{\"error\":{\"code\":\"server_error\"}}}", false, true},
		{"cancelled", "{\"type\":\"response.cancelled\",\"response\":{\"error\":{\"code\":\"server_error\"}}}", false, true},
		{"malformed", "{\"type\":\"error\",\"error\":{\"code\":\"server_error\"}", false, false},
		{"wrong_type", "{\"type\":\"error\",\"error\":{\"code\":5,\"type\":\"server_error\"}}", false, false},
		{"unknown", "{\"type\":\"error\",\"error\":{\"code\":\"unrecognized\"}}", false, false},
		{"shared_claim_stays_local", "{\"type\":\"error\",\"error\":{\"code\":\"service_unavailable\",\"service_id\":\"shared\",\"scope\":\"service\"}}", true, false},
	} {
		for _, buffered := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/buffered_%t", tc.name, buffered), func(t *testing.T) {
				d := &controlledDispatch{status: 200, request: &ControlledRequest{Policy: scheduling.Policy{Enabled: true}, ReplaySafe: true}, semanticReady: make(chan struct{})}
				if buffered {
					observeControlledBufferedFailure(&http.Response{Body: &controlledResponseBody{dispatch: d, success: true, buffered: true}}, []byte(tc.payload))
				} else {
					d.ObserveFrame([]byte(tc.payload))
				}
				require.Equal(t, tc.provider, d.failureEvidence.ProtocolFailure)
				require.Equal(t, tc.excluded, d.excluded)
				require.Empty(t, d.failureEvidence.SharedKind)
				require.Empty(t, d.failureEvidence.SharedPool)
			})
		}
	}
}

func TestControlledTerminalUsageCertainty(t *testing.T) {
	for _, scenario := range []string{"dial_cancel", "dial_timeout", "dial_admin", "sent_unknown", "semantic_unknown", "terminal_usage_late"} {
		t.Run(scenario, func(t *testing.T) {
			s, db, _, accounts := controlledIntegration(t, false)
			ctx, r := controlledIntegrationRequest(t, s)
			a := controlledPick(t, s, ctx, r, accounts[:1])
			d, err := s.beginDispatch(ctx, a.ID, 10)
			require.NoError(t, err)
			require.NoError(t, d.MarkSent())
			outcome, terminal, cause := "read_error", false, io.ErrUnexpectedEOF
			switch scenario {
			case "dial_cancel":
				cancelled, cancel := context.WithCancel(context.Background())
				cancel()
				r.clientContext = cancelled
				outcome, terminal = "not_sent", true
			case "dial_timeout":
				d.mu.Lock()
				d.timeout = true
				d.mu.Unlock()
				outcome, terminal = "not_sent", true
			case "dial_admin":
				d.adminCancelled.Store(true)
				outcome, terminal = "not_sent", true
			case "semantic_unknown":
				d.noteEvent(true, true, false)
				r.markSemantic(time.Now(), true)
			case "terminal_usage_late":
				outcome, terminal, cause = "completed", true, nil
			}
			d.Finish(outcome, terminal, cause)
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); d.Finish("duplicate", true, nil) }()
			}
			wg.Wait()
			var state, certainty string
			var pending bool
			require.NoError(t, db.QueryRow("SELECT state,COALESCE(metrics->>'terminal_certainty',''),usage_pending FROM scheduling_attempts WHERE ticket_id=$1", d.ticket.TicketID).Scan(&state, &certainty, &pending))
			if scenario == "terminal_usage_late" {
				require.Equal(t, "settled", state)
				require.True(t, pending)
				ackErrors := make(chan error, 8)
				for i := 0; i < 8; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						ackErrors <- s.Store.AcknowledgeUsage(context.Background(), d.ticket.TicketID)
					}()
				}
				wg.Wait()
				close(ackErrors)
				for ackErr := range ackErrors {
					require.NoError(t, ackErr)
				}
				require.NoError(t, db.QueryRow("SELECT usage_pending FROM scheduling_attempts WHERE ticket_id=$1", d.ticket.TicketID).Scan(&pending))
				require.False(t, pending)
			} else if !terminal {
				require.Equal(t, "unknown", state)
				require.Empty(t, certainty)
			} else {
				require.Equal(t, "settled", state)
				require.Equal(t, "proven_not_sent", certainty)
				require.False(t, pending)
			}
			require.Len(t, r.history, 1)
		})
	}
}
