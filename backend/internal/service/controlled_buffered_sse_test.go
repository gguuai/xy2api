//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

type controlledBufferedLocalUpstream struct {
	HTTPUpstream
	t           *testing.T
	server      *httptest.Server
	observation scheduling.Observation
}

func (u *controlledBufferedLocalUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	buffered, _ := req.Context().Value(controlledBufferedResponseContextKey{}).(bool)
	require.True(u.t, buffered, "real gateway must transfer settlement ownership before dispatch")
	d, ok := req.Context().Value(controlledDispatchContextKey{}).(*controlledDispatch)
	require.True(u.t, ok)
	u.observation = d.healthObservation
	local := req.Clone(req.Context())
	target, err := url.Parse(u.server.URL)
	require.NoError(u.t, err)
	local.URL, local.Host = target, ""
	return u.server.Client().Do(local)
}

func controlledBufferedSSEImage(result string) string {
	return `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"image_generation_call","result":"` + result + "\"}]}}\n\n"
}

func TestControlledBufferedSSEForwardAdapters(t *testing.T) {
	for _, input := range []struct {
		name, route, body     string
		healthy, attributable bool
	}{
		{"oauth_terminal_image", "oauth", controlledBufferedSSEImage("AQID"), true, false},
		{"oauth_large_terminal_image", "oauth", controlledBufferedSSEImage(strings.Repeat("A", openAIFirstOutputStageMaxBytes+16384)), true, false},
		{"oauth_empty_terminal", "oauth", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n", false, true},
		{"oauth_text_terminal", "oauth", "data: {\"type\":\"response.completed\",\"response\":" + protocolResponsesBody + "}\n\n", false, false},
		{"oauth_malformed_terminal", "oauth", "data: {\"type\":\"response.completed\",\"response\":\n\n", false, true},
		{"oauth_server_error", "oauth", "data: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"message\":\"fixture\"}}\n\n", false, true},
		{"oauth_policy_rejection", "oauth", "data: {\"type\":\"error\",\"error\":{\"type\":\"image_generation_user_error\",\"code\":\"moderation_blocked\",\"message\":\"fixture\"}}\n\n", false, false},
		{"oauth_content_policy_rejection", "oauth", "data: {\"type\":\"error\",\"error\":{\"type\":\"image_generation_user_error\",\"code\":\"content_policy_violation\",\"message\":\"fixture\"}}\n\n", false, false},
		{"direct_policy_rejection", "direct", "{\"type\":\"error\",\"error\":{\"type\":\"image_generation_user_error\",\"code\":\"content_policy_violation\",\"message\":\"fixture\"}}", false, false},
		{"direct_wrong_protocol", "direct", alphaSearchResponsesSSE("unrelated text"), false, true},
		{"apikey_wrong_protocol", "apikey", alphaSearchResponsesSSE("unrelated text"), false, true},
		{"alpha_valid", "alpha", alphaSearchResponsesSSE("search result"), true, false},
		{"alpha_missing_payload", "alpha", "data: {\"type\":\"response.completed\"}\n\n", false, true},
		{"alpha_empty_results", "alpha", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n", true, false},
		{"alpha_wrong_schema", "alpha", "data: {\"ok\":true}\n\n", false, true},
	} {
		t.Run(input.name, func(t *testing.T) {
			s, db, policy, accounts := controlledIntegration(t, true)
			policy.Profiles[0].AttemptTimeoutMS = 10000
			policy.Profiles[0].TotalBudgetMS = 20000
			_, err := s.Store.PutPolicy(context.Background(), policy, 1)
			require.NoError(t, err)
			ctx, request := controlledIntegrationRequest(t, s)
			gateModel := openAIImagesResponsesMainModelValue()
			if input.route == "direct" || input.route == "apikey" {
				gateModel = "gpt-image-2"
			}
			if input.route == "alpha" {
				gateModel = "test-model"
			}
			frozen, err := s.Store.FreezeFailureAdmission(ctx, 1, gateModel)
			require.NoError(t, err)
			gateKey := frozen.ModelKey()
			_, err = db.Exec("INSERT INTO scheduling_failure_gates(gate_key,scope,reason,ready_after) VALUES($1,'account_model','fixture',NOW()-INTERVAL '1 second')", gateKey)
			require.NoError(t, err)
			account := controlledPick(t, s, ctx, request, accounts[:1])
			account.Credentials = map[string]any{"access_token": "fixture", "api_key": "fixture"}
			if input.route == "apikey" {
				account.Type = AccountTypeAPIKey
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if input.name == "direct_policy_rejection" {
					w.Header().Set("Content-Type", "application/json")
				}
				_, _ = fmt.Fprint(w, input.body)
			}))
			defer server.Close()
			upstream := &controlledBufferedLocalUpstream{t: t, server: server}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: &controlledHTTPUpstream{service: s, base: upstream}}
			model := "gpt-image-1"
			if input.route == "direct" || input.route == "apikey" {
				model = "gpt-image-2"
			}
			body := fmt.Sprintf(`{"model":%q,"prompt":"fixture","response_format":"b64_json"}`, model)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(body)).WithContext(ctx)
			c.Request.Header.Set("Content-Type", "application/json")
			var adapterErr error
			if input.route == "alpha" {
				_, adapterErr = svc.forwardAlphaSearchViaResponsesWebSearch(ctx, c, account, []byte(`{"model":"test-model","commands":{}}`), "fixture", "", "test-model", "test-model")
			} else {
				parsed, err := svc.ParseOpenAIImagesRequest(c, []byte(body))
				require.NoError(t, err)
				_, adapterErr = svc.ForwardImages(ctx, c, account, []byte(body), parsed, "")
			}
			observation := upstream.observation
			require.Equal(t, gateModel, observation.Model, "actual outbound model owns the health record")
			require.NotEmpty(t, observation.HealthIdentity)
			key := scheduling.HealthRedisKey(observation.AccountID, observation.Model, observation.Profile, observation.Reasoning, observation.ContextBucket, observation.Transport, observation.HealthIdentity)
			raw, err := s.redis.HGet(ctx, key, "snapshot").Bytes()
			require.NoError(t, err)
			var health scheduling.HealthSnapshot
			require.NoError(t, json.Unmarshal(raw, &health))
			var outcome, state string
			var gateRecovered bool
			require.NoError(t, db.QueryRow("SELECT outcome,state FROM scheduling_attempts WHERE request_id=$1", request.ID).Scan(&outcome, &state))
			require.NoError(t, db.QueryRow("SELECT ready_after IS NULL FROM scheduling_failure_gates WHERE gate_key=$1", gateKey).Scan(&gateRecovered))
			t.Logf("OBSERVED error=%v outcome=%s state=%s good_streak=%d gate_recovered=%v samples=%+v", adapterErr, outcome, state, health.GoodStreak, gateRecovered, health.Samples)
			require.Equal(t, "settled", state)
			if input.healthy {
				require.NoError(t, adapterErr)
				require.Equal(t, "completed", outcome)
				require.Equal(t, 1, health.GoodStreak)
				require.True(t, gateRecovered)
				require.Len(t, health.Samples, 1)
				require.True(t, health.Samples[0].Completed)
			} else {
				require.Error(t, adapterErr)
				require.NotEqual(t, "completed", outcome)
				require.Zero(t, health.GoodStreak)
				require.False(t, gateRecovered)
				if input.attributable {
					require.Len(t, health.Samples, 1)
					require.True(t, health.Samples[0].AttributableFailure)
				} else {
					require.Empty(t, health.Samples)
				}
			}
			if strings.HasSuffix(input.name, "policy_rejection") {
				request.mu.Lock()
				replaySafe := request.ReplaySafe
				request.mu.Unlock()
				require.False(t, replaySafe, "policy errors must prohibit cross-account replay")
				var imageErr *OpenAIImagesUpstreamError
				require.ErrorAs(t, adapterErr, &imageErr)
				require.Equal(t, http.StatusBadRequest, imageErr.StatusCode)
				require.Equal(t, "image_generation_user_error", imageErr.ErrorType)
			}
			for _, sample := range health.Samples {
				require.False(t, sample.HasSemanticOutput, "buffering must not invent first-output evidence")
			}
		})
	}
}

func TestControlledBufferedSSEEOFWaitsForAdapter(t *testing.T) {
	s, db, _, accounts := controlledIntegration(t, true)
	ctx, request := controlledIntegrationRequest(t, s)
	account := controlledPick(t, s, ctx, request, accounts[:1])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, controlledBufferedSSEImage("AQID"))
	}))
	defer server.Close()
	req, err := http.NewRequestWithContext(withControlledBufferedResponse(ctx), "POST", server.URL, strings.NewReader(`{"stream":true}`))
	require.NoError(t, err)
	resp, err := s.roundTrip(req, account.ID, 10, server.Client().Do)
	require.NoError(t, err)
	b, ok := resp.Body.(*controlledResponseBody)
	require.True(t, ok, "buffered SSE must bypass prefetch wrapper")
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	b.dispatch.mu.Lock()
	terminal, semantic := b.dispatch.transportTerminal, !b.dispatch.semantic.IsZero()
	b.dispatch.mu.Unlock()
	require.True(t, terminal)
	require.False(t, semantic)
	var state string
	require.NoError(t, db.QueryRow("SELECT state FROM scheduling_attempts WHERE request_id=$1", request.ID).Scan(&state))
	require.NotEqual(t, "settled", state, "EOF alone must not grant health")
	err = validateControlledNonstreamResponse(resp, body, "responses")
	require.NoError(t, err)
	finishControlledNonstreamResponse(resp, &err)
	finishControlledNonstreamResponse(resp, &err)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, resp.Body.Close())
	identity, err := s.controlledHealthIdentity(ctx, account)
	require.NoError(t, err)
	key := scheduling.HealthRedisKey(account.ID, "test-model", request.Profile, request.Reasoning, controlledBucket(request), request.Protocol, identity)
	raw, err := s.redis.HGet(ctx, key, "snapshot").Bytes()
	require.NoError(t, err)
	var health scheduling.HealthSnapshot
	require.NoError(t, json.Unmarshal(raw, &health))
	require.Equal(t, 1, health.GoodStreak)
	require.Len(t, health.Samples, 1, "finish and close must settle exactly once")
	require.True(t, health.Samples[0].Completed)
	require.False(t, health.Samples[0].HasSemanticOutput)
}
