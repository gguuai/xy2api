//go:build unit

package service

import (
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
	"time"
)

func TestControlledFailureEvidenceRequiresCompleteTrustedScope(t *testing.T) {
	a := scheduling.FailureAdmission{AccountID: 1, Model: "model-a", Domains: scheduling.AccountFailureDomains{QuotaPoolID: "org-a", AvailabilityPoolID: "svc-a"}}
	for _, tt := range []struct {
		name, raw, scope, retry string
		status                  int
	}{
		{"generic400", `{"error":{"type":"invalid_request_error","message":"selected model capacity"}}`, "request", "next_eligible", 400},
		{"context differs by provider", `{"error":{"code":"context_length_exceeded"}}`, "request", "next_eligible", 400},
		{"explicit parameter rejection", `{"error":{"code":"invalid_parameter","param":"temperature"}}`, "request", "stop", 400},
		{"truncated scope", `{"error":{"code":"insufficient_quota","organization_id":"org-a"}`, "account_model", "next_eligible", 429},
		{"matching quota", `{"error":{"code":"insufficient_quota","organization_id":"org-a"}}`, "quota_pool", "next_eligible", 429},
		{"other organization", `{"error":{"code":"insufficient_quota","organization_id":"org-b"}}`, "account_model", "next_eligible", 429},
		{"same url proves nothing", `{"error":{"code":"service_unavailable","base_url":"https://same.example"}}`, "account_model", "next_eligible", 503},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evidence := FailureEvidenceFromResponse(tt.status, http.Header{}, []byte(tt.raw), a, time.Now())
			require.False(t, evidence.GlobalInput)
			decision := scheduling.ClassifyFailure(evidence, a, time.Now())
			require.Equal(t, tt.scope, decision.Scope)
			require.Equal(t, tt.retry, decision.Retry)
		})
	}
	e := FailureEvidenceFromResponse(429, http.Header{}, []byte(`{"error":{"code":"insufficient_quota","organization_id":"org-a","model":"model-a"}}`), a, time.Now())
	require.False(t, e.SharedModel, "model field alone does not prove model-scoped quota")
	e = FailureEvidenceFromResponse(429, http.Header{}, []byte(`{"error":{"code":"insufficient_quota","organization_id":"org-a","model":"model-a","scope":"model"}}`), a, time.Now())
	require.True(t, e.SharedModel)
}
