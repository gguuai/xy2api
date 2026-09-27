package scheduling

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCanonicalTransportAliasesPreserveProtocolBuckets(t *testing.T) {
	for input, want := range map[string]string{"anthropic": "messages", "chat_completions": "chat", "response": "responses", "websocket": "ws", "http": "http", "responses": "responses", "chat": "chat", "messages": "messages", "gemini": "gemini", "ws": "ws", "": ""} {
		require.Equal(t, want, CanonicalTransport(input))
	}
	p := Policy{Model: "m", Profiles: []LatencyProfile{{Name: "messages", Transport: "anthropic"}, {Name: "responses", Transport: "responses"}, {Name: "generic", Transport: "http"}}}
	for input, name := range map[string]string{"messages": "messages", "anthropic": "messages", "response": "responses", "responses": "responses", "http": "generic"} {
		got, ok := ResolveProfileForTransport(p, "", -1, input)
		require.True(t, ok)
		require.Equal(t, name, got.Name)
		require.Equal(t, CanonicalTransport(input), got.Transport)
	}
	_, ok := ResolveProfileForTransport(p, "", -1, "chat")
	require.False(t, ok, "no cross-protocol fallback without explicit wildcard")
}
