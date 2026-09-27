package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

type controlledMetadataSnapshot struct {
	protocol, reasoning, body string
	replay                    bool
}

func captureSchedulingMiddlewareMetadata(t *testing.T, path, body string) controlledMetadataSnapshot {
	t.Helper()
	var result controlledMetadataSnapshot
	router := gin.New()
	router.Use(ControlledSchedulingMiddleware())
	router.POST("/*path", func(c *gin.Context) {
		raw, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		r := controlledRequest(c.Request.Context())
		require.NotNil(t, r)
		r.mu.Lock()
		result = controlledMetadataSnapshot{protocol: r.Protocol, reasoning: r.Reasoning, replay: r.ReplaySafe, body: string(raw)}
		r.mu.Unlock()
		c.Status(http.StatusNoContent)
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.Equal(t, body, result.body, "capturing scheduling metadata must not change forwarded bytes")
	return result
}

func TestControlledRequestMetadataNativeProtocols(t *testing.T) {
	tests := []struct{ name, path, body, protocol, reasoning string }{
		{"gemini streaming path", "/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse", "{\"contents\":[],\"generationConfig\":{\"thinkingConfig\":{\"thinkingLevel\":\"HIGH\"}}}", "gemini", "high"},
		{"gemini ordinary path", "/v1beta/models/gemini-2.5-pro:generateContent", "{\"contents\":[],\"generationConfig\":{\"thinkingConfig\":{\"thinkingLevel\":\"low\"}}}", "gemini", "low"},
		{"gemini explicit budget", "/v1beta/models/m:streamGenerateContent", "{\"generationConfig\":{\"thinkingConfig\":{\"thinkingBudget\":24576}}}", "gemini", "budget:24576"},
		{"gemini zero budget", "/v1beta/models/m:streamGenerateContent", "{\"generationConfig\":{\"thinkingConfig\":{\"thinkingBudget\":0}}}", "gemini", "budget:0"},
		{"gemini dynamic budget", "/v1beta/models/m:streamGenerateContent", "{\"generationConfig\":{\"thinkingConfig\":{\"thinkingBudget\":-1}}}", "gemini", "budget:-1"},
		{"gemini snake aliases", "/v1beta/models/m:generateContent", "{\"generation_config\":{\"thinking_config\":{\"thinking_level\":\"MEDIUM\",\"thinking_budget\":1024}}}", "gemini", "medium|budget:1024"},
		{"gemini mixed aliases", "/v1beta/models/m:generateContent", "{\"generation_config\":{\"thinkingConfig\":{\"thinkingLevel\":\"minimal\"}}}", "gemini", "minimal"},
		{"gemini invalid numeric budget", "/v1beta/models/m:generateContent", "{\"generationConfig\":{\"thinkingConfig\":{\"thinkingBudget\":1.5}}}", "gemini", "unknown"},
		{"gemini budget text not inferred", "/v1beta/models/m:generateContent", "{\"generationConfig\":{\"thinkingConfig\":{\"thinkingBudget\":\"24576\"}}}", "gemini", "unknown"},
		{"gemini unspecified level", "/v1beta/models/m:generateContent", "{\"generationConfig\":{\"thinkingConfig\":{\"thinkingLevel\":\"THINKING_LEVEL_UNSPECIFIED\"}}}", "gemini", "unknown"},
		{"gemini absent configuration", "/v1beta/models/m:generateContent", "{\"contents\":[]}", "gemini", "default"},
		{"anthropic adaptive explicit effort", "/v1/messages", "{\"model\":\"claude-test\",\"thinking\":{\"type\":\"adaptive\"},\"output_config\":{\"effort\":\"HIGH\"}}", "messages", "high"},
		{"anthropic explicit budget", "/v1/messages", "{\"thinking\":{\"type\":\"enabled\",\"budget_tokens\":16384}}", "messages", "budget:16384"},
		{"anthropic effort and budget remain distinct", "/v1/messages", "{\"thinking\":{\"type\":\"enabled\",\"budget_tokens\":4096},\"output_config\":{\"effort\":\"max\"}}", "messages", "max|budget:4096"},
		{"anthropic disabled", "/v1/messages", "{\"thinking\":{\"type\":\"disabled\"}}", "messages", "none"},
		{"anthropic enabled no depth", "/v1/messages", "{\"thinking\":{\"type\":\"enabled\"}}", "messages", "unknown"},
		{"anthropic adaptive no depth", "/v1/messages", "{\"thinking\":{\"type\":\"adaptive\"}}", "messages", "unknown"},
		{"anthropic invalid type", "/v1/messages", "{\"thinking\":{\"type\":\"future_mode\"}}", "messages", "unknown"},
		{"anthropic absent configuration", "/v1/messages", "{\"messages\":[]}", "messages", "default"},
		{"openai responses effort", "/v1/responses", "{\"reasoning\":{\"effort\":\" XHIGH \"}}", "responses", "xhigh"},
		{"openai chat effort", "/v1/chat/completions", "{\"reasoning_effort\":\"LOW\"}", "chat", "low"},
		{"openai absent effort", "/v1/responses", "{\"input\":[]}", "responses", "default"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := captureSchedulingMiddlewareMetadata(t, tc.path, tc.body)
			require.Equal(t, tc.protocol, got.protocol)
			require.Equal(t, tc.reasoning, got.reasoning)
			require.True(t, got.replay)
			profile, ok := scheduling.ResolveProfileForTransport(scheduling.Policy{Profiles: []scheduling.LatencyProfile{{Name: "exact", Transport: tc.protocol, Reasoning: tc.reasoning}, {Name: "wildcard"}}}, got.reasoning, -1, got.protocol)
			require.True(t, ok)
			require.Equal(t, "exact", profile.Name)
		})
	}
}

func TestControlledRequestMetadataConstructorCanonicalAliases(t *testing.T) {
	for alias, want := range map[string]string{"anthropic": "messages", "chat_completions": "chat", "response": "responses", "websocket": "ws", "gemini": "gemini", "http": "http"} {
		require.Equal(t, want, controlledRequest(NewControlledRequestContext(context.Background(), alias)).Protocol, alias)
	}
}

func TestControlledRequestMetadataNativeToolsStayReplayUnsafe(t *testing.T) {
	for _, body := range []string{
		"{\"tools\":[{\"googleSearch\":{}}]}",
		"{\"tools\":[{\"codeExecution\":{}}]}",
		"{\"tools\":[{\"url_context\":{}}]}",
		"{\"tools\":[{\"functionDeclarations\":[{\"name\":\"client_tool\"}],\"googleSearch\":{}}]}",
		"{\"tools\":[{\"type\":\"function\",\"codeExecution\":{}}]}",
		"{\"tools\":[{\"type\":\"custom\",\"google_search\":{}}]}",
	} {
		got := captureSchedulingMiddlewareMetadata(t, "/v1beta/models/m:streamGenerateContent", body)
		require.False(t, got.replay, body)
	}
	got := captureSchedulingMiddlewareMetadata(t, "/v1/responses", "{\"tools\":[{\"type\":\"function\",\"name\":\"client_tool\"}]}")
	require.True(t, got.replay, "ordinary client-executed OpenAI function behavior is retained")
}
