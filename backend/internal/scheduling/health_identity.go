package scheduling

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// StableHealthIdentity separates proven upstream principals/endpoints while
// keeping ordinary OAuth refresh and non-health configuration edits continuous.
// The account ID remains a separate key component. API keys without explicit
// principal metadata use a one-way key fingerprint because replacing such a key
// can replace the upstream account. Raw credentials are never returned.
func StableHealthIdentity(platform, kind string, credentials, extra map[string]any) string {
	first := func(keys ...string) string {
		for _, m := range []map[string]any{credentials, extra} {
			for _, k := range keys {
				if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			}
		}
		return ""
	}
	principal := map[string]string{
		"account":      first("chatgpt_account_id", "account_id"),
		"user":         first("chatgpt_user_id", "claude_user_id", "anthropic_user_id", "user_id", "sub"),
		"organization": first("organization_id", "org_id", "tenant_id"),
		"workspace":    first("workspace_id", "chatgpt_workspace_id"),
		"project":      first("project_id"),
		"email":        first("email"),
	}
	hasPrincipal := false
	for _, v := range principal {
		if v != "" {
			hasPrincipal = true
		}
	}
	endpoint := map[string]string{"base_url": strings.TrimRight(first("base_url"), "/"), "region": first("region", "aws_region")}
	if enabled, ok := extra["custom_base_url_enabled"].(bool); ok && enabled {
		endpoint["custom_base_url"] = strings.TrimRight(first("custom_base_url"), "/")
	}
	if urls, ok := credentials["api_base_urls"].(map[string]any); ok {
		for k, v := range urls {
			if str, ok := v.(string); ok {
				endpoint["protocol:"+k] = strings.TrimRight(strings.TrimSpace(str), "/")
			}
		}
	}
	if urls, ok := credentials["api_base_urls"].(map[string]string); ok {
		for k, v := range urls {
			endpoint["protocol:"+k] = strings.TrimRight(strings.TrimSpace(v), "/")
		}
	}
	keyIdentity := ""
	kind = strings.ToLower(strings.TrimSpace(kind))
	if !hasPrincipal && (kind == "apikey" || kind == "upstream") {
		if key := first("api_key"); key != "" {
			h := sha256.Sum256([]byte(key))
			keyIdentity = hex.EncodeToString(h[:])
		}
	}
	raw, _ := json.Marshal([]any{"stable_health_identity_v1", strings.ToLower(strings.TrimSpace(platform)), kind, principal, endpoint, keyIdentity})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
