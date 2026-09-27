package iqcheck

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Error text is inspected only inside an error envelope (or a non-200 body).
// It never enters diagnostics and is never interpreted as a candy answer.
func classifyUpstreamError(raw []byte, d *Diagnostic, depth int) string {
	if depth > 8 || len(raw) > 8192 {
		return "upstream_error"
	}
	var message string
	if json.Unmarshal(raw, &message) == nil {
		return classifyErrorMessage(message)
	}
	var env map[string]json.RawMessage
	if validateEvent(raw) != nil || json.Unmarshal(raw, &env) != nil {
		return "upstream_error"
	}
	for _, key := range []string{"error", "response"} {
		if nested := env[key]; len(nested) > 0 && !bytes.Equal(nested, []byte("null")) {
			if reason := classifyUpstreamError(nested, d, depth+1); reason != "upstream_error" {
				return reason
			}
		}
	}
	codes := map[string]string{
		"insufficient_quota": "quota_exhausted", "insufficient_balance": "quota_exhausted",
		"balance_not_enough": "quota_exhausted", "quota_exhausted": "quota_exhausted",
		"billing_hard_limit_reached": "quota_exhausted", "billing_not_active": "quota_exhausted",
		"credit_balance_too_low": "quota_exhausted", "payment_required": "quota_exhausted",
		"invalid_api_key": "authentication_unavailable", "invalid_token": "authentication_unavailable",
		"authentication_error": "authentication_unavailable", "token_expired": "authentication_unavailable",
		"permission_denied": "permission_denied", "access_denied": "permission_denied",
		"policy_violation": "policy_denied", "model_not_found": "unsupported_model",
		"unsupported_model": "unsupported_model", "unsupported_parameter": "unsupported_parameter",
		"invalid_parameter": "unsupported_parameter", "rate_limit_exceeded": "rate_limited",
		"rate_limit_error": "rate_limited", "server_error": "upstream_unavailable",
		"overloaded_error": "upstream_unavailable", "service_unavailable": "upstream_unavailable",
		"upstream_timeout": "timeout", "request_timeout": "timeout",
	}
	for _, field := range []string{"code", "type"} {
		var code string
		if json.Unmarshal(env[field], &code) == nil {
			code = strings.ToLower(strings.TrimSpace(code))
			if reason, ok := codes[code]; ok {
				if d != nil {
					if field == "code" {
						d.ErrorCode = code
					} else {
						d.ErrorType = code
					}
				}
				return reason
			}
		}
	}
	for _, field := range []string{"message", "msg", "error_description"} {
		if json.Unmarshal(env[field], &message) == nil {
			if reason := classifyErrorMessage(message); reason != "upstream_error" {
				return reason
			}
		}
	}
	return "upstream_error"
}

func classifyErrorMessage(message string) string {
	message = strings.ToLower(message)
	groups := []struct {
		reason  string
		phrases []string
	}{
		{"quota_exhausted", []string{"insufficient balance", "insufficient credit", "credit balance is too low", "insufficient_quota", "exceeded your current quota", "billing hard limit", "balance not enough", "余额不足", "额度已用尽", "额度不足", "配额已耗尽", "欠费"}},
		{"authentication_unavailable", []string{"invalid api key", "incorrect api key", "invalid_api_key", "invalid access token", "token has expired", "api key has expired", "无效的令牌", "令牌已过期", "密钥无效"}},
		{"rate_limited", []string{"rate limit exceeded", "too many requests", "rate_limit_exceeded", "请求过于频繁", "请求频率超限"}},
		{"timeout", []string{"upstream timed out", "upstream timeout", "request timed out", "gateway timeout", "上游超时", "请求超时"}},
		{"unsupported_model", []string{"model not found", "model does not exist", "不支持该模型", "模型不存在"}},
		{"upstream_unavailable", []string{"no available channel", "no available upstream", "service unavailable", "无可用渠道", "无可用上游"}},
	}
	for _, group := range groups {
		for _, phrase := range group.phrases {
			if strings.Contains(message, phrase) {
				return group.reason
			}
		}
	}
	return "upstream_error"
}

// Only structured error wrappers qualify; assistant text must go through Grade.
func isErrorEnvelope(raw []byte) bool {
	var env struct {
		Success *bool           `json:"success"`
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Msg     string          `json:"msg"`
		Status  string          `json:"status"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return false
	}
	return env.Success != nil && !*env.Success || env.Status == "error" ||
		(len(env.Code) > 0 && (env.Message != "" || env.Msg != "") && string(env.Code) != "0" && string(env.Code) != "200" && string(env.Code) != "null")
}

func FailureCategory(reason string) string {
	switch reason {
	case "correct_answer", "wrong_answer":
		return "assessment"
	case "quota_exhausted", "http_402":
		return "quota"
	case "authentication_unavailable", "http_401", "permission_denied", "policy_denied", "http_403":
		return "access"
	case "rate_limited", "http_429":
		return "rate_limit"
	case "timeout", "http_408", "http_504":
		return "timeout"
	case "request_failed", "response_read_failed", "upstream_unavailable", "upstream_error":
		return "upstream"
	case "unsupported_model", "unsupported_parameter", "unsupported_account_type", "invalid_endpoint", "http_400", "http_404":
		return "configuration"
	case "refusal", "ambiguous_answer", "unparseable_answer", "invalid_answer_structure", "empty_response":
		return "answer"
	case "interrupted", "request_cancelled", "cancelled_by_account_change", "waiting_state", "background_budget":
		return "execution"
	}
	if strings.HasPrefix(reason, "http_") {
		return "upstream"
	}
	return "protocol"
}
