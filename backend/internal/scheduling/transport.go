package scheduling

import "strings"

// CanonicalTransport preserves protocol-specific health buckets. Generic http
// never reads Responses, Chat, Messages, Gemini or WebSocket samples.
func CanonicalTransport(value string) string {
	switch value = strings.ToLower(strings.TrimSpace(value)); value {
	case "anthropic":
		return "messages"
	case "chat_completions":
		return "chat"
	case "response":
		return "responses"
	case "websocket":
		return "ws"
	default:
		return value
	}
}
