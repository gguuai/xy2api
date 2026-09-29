//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

var reviewTransportSent = errors.New("diagnostic transport reached")

type reviewNoNetworkTransport struct{ calls int }

func (u *reviewNoNetworkTransport) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.calls++
	fmt.Println("DIAGNOSTIC_TRANSPORT_SEND_REACHED")
	return nil, reviewTransportSent
}
func (u *reviewNoNetworkTransport) DoWithTLS(r *http.Request, p string, a int64, c int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, a, c)
}

// Children cap stack growth so a recursive regression cannot exhaust the test
// runner. The terminal transport is a stub and never sends network traffic.
func TestReviewV021OpenAITransportNoRecursiveDispatch(t *testing.T) {
	if scenario := os.Getenv("XY2_RECURSION_DIAGNOSTIC_CASE"); scenario != "" {
		debug.SetMaxStack(128 << 10)
		debug.SetTraceback("single")
		transport := &reviewNoNetworkTransport{}
		gateway := &OpenAIGatewayService{controlledScheduling: &ControlledSchedulingService{}, httpUpstream: transport}
		ctx := context.Background()
		method := http.MethodPost
		switch scenario {
		case "get_without_dispatch":
			method = http.MethodGet
		case "post_without_request_context":
		case "sub2api_mode":
			ctx = NewControlledRequestContext(ctx, "openai")
			r := controlledRequest(ctx)
			r.Mode = scheduling.ModeSnapshot{Mode: scheduling.ModeSub2API}
			r.modeResolved = true
			r.policyLoaded = true
		case "without_scheduler_control":
			gateway.controlledScheduling = nil
		case "already_dispatched_control":
			ctx = context.WithValue(ctx, controlledDispatchContextKey{}, &controlledDispatch{})
		default:
			t.Fatalf("unknown diagnostic scenario %q", scenario)
		}
		req, err := http.NewRequestWithContext(ctx, method, "https://diagnostic.invalid/v1/responses", nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = gateway.doOpenAIUpstream(req, "", &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1})
		if !errors.Is(err, reviewTransportSent) || transport.calls != 1 {
			t.Fatalf("expected one transport invocation, calls=%d err=%v", transport.calls, err)
		}
		return
	}

	for _, scenario := range []string{"get_without_dispatch", "post_without_request_context", "sub2api_mode", "without_scheduler_control", "already_dispatched_control"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReviewV021OpenAITransportNoRecursiveDispatch$", "-test.v")
			cmd.Env = append(os.Environ(), "XY2_RECURSION_DIAGNOSTIC_CASE="+scenario)
			output, err := cmd.CombinedOutput()
			if dir := os.Getenv("XY2_RECURSION_DIAGNOSTIC_OUTPUT"); dir != "" {
				if writeErr := os.WriteFile(filepath.Join(dir, scenario+".log"), output, 0600); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			raw := string(output)
			t.Logf("fatal_stack_overflow=%t openai_frame=%t scheduler_frame=%t transport_reached=%t child_exit=%v",
				strings.Contains(raw, "fatal error: stack overflow"),
				strings.Contains(raw, "(*OpenAIGatewayService).doOpenAIUpstream"),
				strings.Contains(raw, "(*ControlledSchedulingService).roundTrip"),
				strings.Contains(raw, "DIAGNOSTIC_TRANSPORT_SEND_REACHED"), err)
			if err != nil {
				t.Errorf("request should invoke transport once without recursive dispatch; child failed (raw evidence: %s.log)", scenario)
			}
		})
	}
}
