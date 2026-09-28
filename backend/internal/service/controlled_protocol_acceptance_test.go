//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

const protocolMessageBody = `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
const protocolResponsesBody = `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`
const protocolChatBody = `{"id":"chat_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
const protocolGeminiBody = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`

type protocolAdapterCase struct {
	name, body string
	run        func(context.Context, *http.Response, *gin.Context, *Account) error
}

func protocolAdapters() []protocolAdapterCase {
	claude := &GatewayService{cfg: &config.Config{}, rateLimitService: &RateLimitService{}}
	openai := &OpenAIGatewayService{cfg: &config.Config{}, rateLimitService: &RateLimitService{}}
	gemini := &GeminiMessagesCompatService{cfg: &config.Config{}}
	return []protocolAdapterCase{
		{"anthropic", protocolMessageBody, func(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
			_, e := claude.handleNonStreamingResponse(ctx, r, c, a, "test-model", "test-model")
			return e
		}},
		{"anthropic_passthrough", protocolMessageBody, func(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
			_, e := claude.handleNonStreamingResponseAnthropicAPIKeyPassthrough(ctx, r, c, a)
			return e
		}},
		{"bedrock", protocolMessageBody, func(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
			_, e := claude.handleBedrockNonStreamingResponse(ctx, r, c, a)
			return e
		}},
		{"native_anthropic", protocolMessageBody, func(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
			_, e := openai.handleNativeAnthropicBufferedResponse(ctx, r, c, a, "test-model", "test-model", "test-model", nil, time.Now())
			return e
		}},
		{"responses", protocolResponsesBody, func(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
			_, e := openai.handleNonStreamingResponse(ctx, r, c, a, "test-model", "test-model")
			return e
		}},
		{"responses_passthrough", protocolResponsesBody, func(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
			_, e := openai.handleNonStreamingResponsePassthrough(ctx, r, c, a, "test-model", "test-model")
			return e
		}},
		{"raw_chat", protocolChatBody, func(_ context.Context, r *http.Response, c *gin.Context, a *Account) error {
			_, e := openai.bufferRawChatCompletions(c, r, a, "test-model", "test-model", "test-model", nil, nil, time.Now())
			return e
		}},
		{"images", `{"created":1700000000,"data":[{"b64_json":"AQID"}]}`, func(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
			_, _, _, e := openai.handleOpenAIImagesNonStreamingResponse(ctx, r, c, a, &OpenAIImagesRequest{})
			return e
		}},
		{"images_direct", `{"data":[{"b64_json":"AQID"}]}`, func(_ context.Context, r *http.Response, c *gin.Context, _ *Account) error {
			_, _, _, e := openai.handleCodexDirectImagesNonStreamingResponse(r, c, &OpenAIImagesRequest{})
			return e
		}},
		{"images_oauth", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"image_generation_call\",\"result\":\"AQID\"}]}}\n\n", func(_ context.Context, r *http.Response, c *gin.Context, _ *Account) error {
			_, _, _, e := openai.handleOpenAIImagesOAuthNonStreamingResponse(r, c, "b64_json", "test-model")
			return e
		}},
		{"embeddings", `{"data":[{"embedding":[0.1,0.2],"index":0}]}`, controlledEmbeddingsAdapter},
		{"embeddings_base64", `{"data":[{"embedding":"zczMPc3MTD4=","index":0}]}`, controlledEmbeddingsAdapter},
		{"alpha_search", `{"results":[]}`, func(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
			svc := controlledProtocolGateway(r)
			_, e := svc.ForwardAlphaSearch(ctx, c, controlledProtocolAPIKey(a, PlatformOpenAI), []byte(`{"model":"test-model","commands":{}}`))
			return e
		}},
		{"alpha_search_responses", alphaSearchResponsesSSE("search result"), func(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
			svc := controlledProtocolGateway(r)
			_, e := svc.forwardAlphaSearchViaResponsesWebSearch(ctx, c, controlledProtocolAPIKey(a, PlatformOpenAI), []byte(`{"model":"test-model","commands":{}}`), "fixture", "", "test-model", "test-model")
			return e
		}},
		{"grok_voice", `{"text":"recognized"}`, func(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
			svc := controlledProtocolGateway(r)
			_, e := svc.ForwardGrokVoice(ctx, c, controlledProtocolAPIKey(a, PlatformGrok), "stt", []byte(`{}`), "application/json")
			return e
		}},
		{"grok_voice_silence", `{"text":""}`, func(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
			svc := controlledProtocolGateway(r)
			_, e := svc.ForwardGrokVoice(ctx, c, controlledProtocolAPIKey(a, PlatformGrok), "stt", []byte(`{}`), "application/json")
			return e
		}},
		{"grok_custom_voice", `{"voice_id":"voice-1"}`, func(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
			svc := controlledProtocolGateway(r)
			_, e := svc.ForwardGrokVoice(ctx, c, controlledProtocolAPIKey(a, PlatformGrok), "custom-voices", []byte(`{}`), "application/json")
			return e
		}},
		{"grok_voice_timestamps", `{"audio":"AQID","audio_timestamps":{"graph_chars":[],"graph_times":[]}}`, func(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
			svc := controlledProtocolGateway(r)
			_, e := svc.ForwardGrokVoice(ctx, c, controlledProtocolAPIKey(a, PlatformGrok), "tts", []byte(`{"text":"hello","with_timestamps":true}`), "application/json")
			return e
		}},
		{"grok_media", `{"request_id":"video-1"}`, func(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
			svc := controlledProtocolGateway(r)
			_, e := svc.ForwardGrokMedia(ctx, c, controlledProtocolAPIKey(a, PlatformGrok), GrokMediaEndpointVideosGenerations, "", []byte(`{"model":"grok-imagine-video","prompt":"waves"}`), "application/json")
			return e
		}},
		{"seedance", `{"id":"task-1"}`, func(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
			svc := controlledProtocolGateway(r)
			account := controlledProtocolAPIKey(a, PlatformOpenAI)
			account.Credentials["base_url"] = "https://api.openai.com"
			account.Credentials["openai_capabilities"] = []string{"seedance"}
			_, e := svc.ForwardSeedance(ctx, c, account, SeedanceEndpointCreate, "", []byte(`{"model":"test-model","content":[{"type":"text","text":"waves"}]}`))
			return e
		}},
		{"converted_chat", protocolChatBody, func(_ context.Context, r *http.Response, c *gin.Context, _ *Account) error {
			_, _, e := openai.readCCUpstreamJSONResponse(c, r, writeChatCompletionsError)
			return e
		}},
		{"native_gemini", protocolGeminiBody, func(_ context.Context, r *http.Response, c *gin.Context, a *Account) error {
			_, e := gemini.handleNativeNonStreamingResponse(c, r, false, a, "")
			return e
		}},
		{"gemini_chat", protocolGeminiBody, func(_ context.Context, r *http.Response, c *gin.Context, _ *Account) error {
			_, e := gemini.handleChatCompletionsNonStreamingResponseFromGemini(c, r, "test-model", false)
			return e
		}},
		{"gemini_messages", `{"response":` + protocolGeminiBody + `}`, func(_ context.Context, r *http.Response, c *gin.Context, _ *Account) error {
			_, e := gemini.handleNonStreamingResponse(c, r, "test-model")
			return e
		}},
	}
}

// The scheduling response already comes from a real local HTTP dispatch; only
// the protocol adapter's second transport boundary is stubbed here. Keep its
// account projection separate so the frozen scheduling identity stays intact.
func controlledProtocolGateway(resp *http.Response) *OpenAIGatewayService {
	return &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: &httpUpstreamRecorder{resp: resp}}
}

func controlledProtocolAPIKey(a *Account, platform string) *Account {
	copy := *a
	copy.Platform, copy.Type = platform, AccountTypeAPIKey
	copy.Credentials = map[string]any{"api_key": "fixture"}
	return &copy
}

func controlledEmbeddingsAdapter(ctx context.Context, r *http.Response, c *gin.Context, a *Account) error {
	svc := controlledProtocolGateway(r)
	_, err := svc.ForwardEmbeddings(ctx, c, controlledProtocolAPIKey(a, PlatformOpenAI), []byte(`{"model":"test-model","input":"hello"}`), "")
	return err
}

func TestControlledProtocolNonstreamAcceptance(t *testing.T) {
	for _, adapter := range protocolAdapters() {
		for _, input := range []struct {
			name, body string
			healthy    bool
		}{
			{"valid", adapter.body, true},
			{"html", "<html>upstream failed</html>", false},
			{"error_object", `{"error":{"message":"provider failure"}}`, false},
			{"empty", "", false},
			{"null", "null", false},
			{"wrong_schema", `{"ok":true}`, false},
			{"empty_result_object", `{"data":[{}]}`, false},
			{"wrong_protocol_sse", alphaSearchResponsesSSE("unrelated text"), false},
		} {
			if input.name == "wrong_protocol_sse" && adapter.name != "images" && adapter.name != "images_direct" && !strings.HasPrefix(adapter.name, "embeddings") {
				continue
			}
			t.Run(adapter.name+"/"+input.name, func(t *testing.T) {
				s, db, _, accounts := controlledIntegration(t, true)
				ctx, r := controlledIntegrationRequest(t, s)
				frozen, err := s.Store.FreezeFailureAdmission(ctx, 1, "test-model")
				require.NoError(t, err)
				gateKey := frozen.ModelKey()
				_, err = db.Exec("INSERT INTO scheduling_failure_gates(gate_key,scope,reason,ready_after) VALUES($1,'account_model','fixture',NOW()-INTERVAL '1 second')", gateKey)
				require.NoError(t, err)
				a := controlledPick(t, s, ctx, r, accounts[:1])
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, input.body)
				}))
				defer server.Close()
				req, err := http.NewRequestWithContext(ctx, "POST", server.URL, strings.NewReader(`{"stream":false}`))
				require.NoError(t, err)
				resp, err := s.roundTrip(req, a.ID, 10, server.Client().Do)
				require.NoError(t, err)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/test", nil).WithContext(ctx)
				adapterErr := adapter.run(ctx, resp, c, a)
				require.NoError(t, resp.Body.Close())
				identity, err := s.controlledHealthIdentity(ctx, a)
				require.NoError(t, err)
				key := scheduling.HealthRedisKey(a.ID, "test-model", r.Profile, r.Reasoning, controlledBucket(r), r.Protocol, identity)
				raw, err := s.redis.HGet(ctx, key, "snapshot").Bytes()
				require.NoError(t, err)
				var health scheduling.HealthSnapshot
				require.NoError(t, json.Unmarshal(raw, &health))
				var outcome, state string
				var gateRecovered bool
				require.NoError(t, db.QueryRow("SELECT outcome,state FROM scheduling_attempts WHERE request_id=$1", r.ID).Scan(&outcome, &state))
				require.NoError(t, db.QueryRow("SELECT ready_after IS NULL FROM scheduling_failure_gates WHERE gate_key=$1", gateKey).Scan(&gateRecovered))
				t.Logf("OBSERVED adapter_error=%v outcome=%s state=%s good_streak=%d gate_recovered=%v", adapterErr, outcome, state, health.GoodStreak, gateRecovered)
				require.Equal(t, "settled", state, "EOF must not leak UNKNOWN capacity")
				if input.healthy {
					require.NoError(t, adapterErr)
					require.Equal(t, "completed", outcome)
					require.Equal(t, 1, health.GoodStreak)
					require.True(t, gateRecovered)
				} else {
					require.Error(t, adapterErr)
					require.NotEqual(t, "completed", outcome)
					require.Zero(t, health.GoodStreak)
					require.False(t, gateRecovered)
					for _, sample := range health.Samples {
						require.False(t, sample.Completed)
					}
					if adapter.name == "images" || adapter.name == "images_direct" || adapter.name == "images_oauth" {
						require.NotEmpty(t, health.Samples, "invalid image payload must count as provider failure")
						require.True(t, health.Samples[len(health.Samples)-1].AttributableFailure)
					}
				}
			})
		}
	}
}

func TestControlledImagesNonstreamNativeErrorAttribution(t *testing.T) {
	for _, adapter := range protocolAdapters() {
		if adapter.name != "images_direct" && adapter.name != "images_oauth" {
			continue
		}
		for _, upstream := range []struct {
			name, kind, code string
			status           int
		}{
			{"server_failure", "server_error", "upstream_error", http.StatusBadGateway},
			{"policy_rejection", "image_generation_user_error", "moderation_blocked", http.StatusBadRequest},
		} {
			t.Run(adapter.name+"/"+upstream.name, func(t *testing.T) {
				s, db, _, accounts := controlledIntegration(t, true)
				ctx, request := controlledIntegrationRequest(t, s)
				a := controlledPick(t, s, ctx, request, accounts[:1])
				body := fmt.Sprintf(`{"error":{"type":%q,"code":%q,"message":"fixture"}}`, upstream.kind, upstream.code)
				if adapter.name == "images_oauth" {
					body = "data: {\"type\":\"error\"," + body[1:] + "\n\n"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, body)
				}))
				defer server.Close()
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(`{"stream":false}`))
				require.NoError(t, err)
				resp, err := s.roundTrip(req, a.ID, 10, server.Client().Do)
				require.NoError(t, err)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
				adapterErr := adapter.run(ctx, resp, c, a)
				var native *OpenAIImagesUpstreamError
				require.ErrorAs(t, adapterErr, &native, "retain native error type for downstream retry policy")
				require.Equal(t, upstream.status, native.StatusCode)
				require.Equal(t, upstream.code, native.Code)
				require.NoError(t, resp.Body.Close())
				identity, err := s.controlledHealthIdentity(ctx, a)
				require.NoError(t, err)
				key := scheduling.HealthRedisKey(a.ID, "test-model", request.Profile, request.Reasoning, controlledBucket(request), request.Protocol, identity)
				raw, err := s.redis.HGet(ctx, key, "snapshot").Bytes()
				require.NoError(t, err)
				var health scheduling.HealthSnapshot
				require.NoError(t, json.Unmarshal(raw, &health))
				var outcome, state string
				require.NoError(t, db.QueryRow("SELECT outcome,state FROM scheduling_attempts WHERE request_id=$1", request.ID).Scan(&outcome, &state))
				require.Equal(t, "settled", state)
				require.Equal(t, "invalid_response", outcome)
				require.Zero(t, health.GoodStreak)
				if upstream.status >= http.StatusInternalServerError {
					require.Len(t, health.Samples, 1)
					require.True(t, health.Samples[0].AttributableFailure)
					require.False(t, health.Samples[0].Completed)
					require.False(t, health.Samples[0].HasSemanticOutput)
				} else {
					require.Empty(t, health.Samples, "request content policy must not penalize provider health")
				}
				t.Logf("OBSERVED native_status=%d outcome=%s state=%s samples=%+v", native.StatusCode, outcome, state, health.Samples)
			})
		}
	}
}

func TestControlledProtocolErrorBodyDeadline(t *testing.T) {
	for _, size := range []int{128, 64 * 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s, db, _, accounts := controlledIntegration(t, true)
			ctx, r := controlledIntegrationRequest(t, s)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			a := controlledPick(t, s, ctx, r, accounts[:1])
			wireCancelled := make(chan time.Time, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.WriteHeader(503)
				_, _ = fmt.Fprint(w, strings.Repeat("x", size))
				w.(http.Flusher).Flush()
				<-req.Context().Done()
				wireCancelled <- time.Now()
			}))
			defer server.Close()
			done := make(chan error, 1)
			go func() {
				req, _ := http.NewRequestWithContext(ctx, "POST", server.URL, strings.NewReader(`{"stream":true}`))
				resp, err := s.roundTrip(req, a.ID, 10, server.Client().Do)
				if err == nil {
					_, err = io.ReadAll(resp.Body)
					_ = resp.Body.Close()
				}
				done <- err
			}()
			// D bounds remote diagnostic work. Finish then persists the known
			// terminal under its separate five-second cleanup context; database
			// latency must not obscure whether the wire was cancelled on time.
			var cancelledAt time.Time
			select {
			case cancelledAt = <-wireCancelled:
				require.LessOrEqual(t, cancelledAt.Sub(r.Ledger.Snapshot().Deadline), 350*time.Millisecond)
			case <-time.After(time.Until(r.Ledger.Snapshot().Deadline) + 350*time.Millisecond):
				cancel()
				t.Fatal("upstream error body was not cancelled by D")
			}
			select {
			case err := <-done:
				require.ErrorContains(t, err, "upstream error body deadline exceeded")
			case <-time.After(5*time.Second + 350*time.Millisecond):
				cancel()
				t.Fatal("terminal settlement exceeded its bounded cleanup window")
			}
			require.False(t, r.Ledger.Snapshot().TimeoutSeen, "HTTP error-body wait must not become first-output timeout")
			var state, outcome string
			require.NoError(t, db.QueryRow("SELECT state,outcome FROM scheduling_attempts WHERE request_id=$1", r.ID).Scan(&state, &outcome))
			require.Equal(t, "settled", state, "a known HTTP rejection must release execution capacity even when diagnostics stall")
			require.Equal(t, "http_error", outcome)
			t.Logf("OBSERVED error_body_finished=true first_output_timeout=%v wire_past_D_ms=%d settled_past_D_ms=%d", r.Ledger.Snapshot().TimeoutSeen, cancelledAt.Sub(r.Ledger.Snapshot().Deadline).Milliseconds(), time.Since(r.Ledger.Snapshot().Deadline).Milliseconds())
		})
	}
}

func TestControlledProtocolLongStreamSurvivesD(t *testing.T) {
	s, _, _, accounts := controlledIntegration(t, true)
	ctx, r := controlledIntegrationRequest(t, s)
	a := controlledPick(t, s, ctx, r, accounts[:1])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(1100 * time.Millisecond)
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\"}\n\n")
	}))
	defer server.Close()
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL, strings.NewReader(`{"stream":true}`))
	resp, err := s.roundTrip(req, a.ID, 10, server.Client().Do)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "response.completed")
	require.NoError(t, resp.Body.Close())
	require.False(t, r.Ledger.Snapshot().TimeoutSeen)
	if time.Now().Before(r.Ledger.Snapshot().Deadline) {
		t.Fatal("test did not cross D")
	}
}
