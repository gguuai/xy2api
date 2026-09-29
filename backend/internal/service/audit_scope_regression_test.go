//go:build unit

package service

import (
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
	"time"
)

func TestAuditControlledExplicitModelScopeNeverWidens(t *testing.T) {
	a := scheduling.FailureAdmission{AccountID: 1, Model: "model-a", HealthIdentity: "fixture-identity", Domains: scheduling.AccountFailureDomains{QuotaPoolID: "org-a", AvailabilityPoolID: "svc-a"}}
	cases := []struct {
		name, raw string
		status    int
		shared    bool
	}{
		{"quota_model_mismatch", `{"error":{"code":"insufficient_quota","organization_id":"org-a","scope":"model","model":"model-b"}}`, 429, false},
		{"quota_model_missing", `{"error":{"code":"insufficient_quota","organization_id":"org-a","scope":"model"}}`, 429, false},
		{"availability_model_mismatch", `{"error":{"code":"service_unavailable","service_id":"svc-a","scope":"model","model":"model-b"}}`, 503, false},
		{"quota_model_matches", `{"error":{"code":"insufficient_quota","organization_id":"org-a","scope":"model","model":"model-a"}}`, 429, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			e := FailureEvidenceFromResponse(tc.status, http.Header{}, []byte(tc.raw), a, now)
			d := scheduling.ClassifyFailure(e, a, now)
			t.Logf("OBSERVED scope=%s shared_model=%v hard=%v key=%s whole_pool_key=%s", d.Scope, e.SharedModel, d.Hard, d.Key, a.SharedKey(e.SharedKind, e.SharedPool, ""))
			if tc.shared {
				require.True(t, e.SharedModel)
				require.Equal(t, a.SharedKey("quota_pool", "org-a", "model-a"), d.Key)
			} else {
				require.Empty(t, e.SharedKind, "unproven model identity cannot authorize any shared gate")
				require.Equal(t, "account_model", d.Scope)
				require.False(t, d.Hard)
			}
		})
	}
}

func TestControlledFailureEvidenceExplicitPoolScope(t *testing.T) {
	a := scheduling.FailureAdmission{AccountID: 1, Model: "model-a", HealthIdentity: "fixture-identity", Domains: scheduling.AccountFailureDomains{QuotaPoolID: "org-a", AvailabilityPoolID: "svc-a"}}
	for _, tc := range []struct {
		name, raw, wantKind string
		status              int
	}{
		{"implicit_org", `{"error":{"code":"insufficient_quota","organization_id":"org-a"}}`, "quota_pool", 429},
		{"explicit_org", `{"error":{"code":"insufficient_quota","organization_id":"org-a","scope":"organization"}}`, "quota_pool", 429},
		{"explicit_project", `{"error":{"code":"project_quota_exceeded","project_id":"org-a","scope":"project"}}`, "quota_pool", 429},
		{"explicit_service", `{"error":{"code":"service_unavailable","service_id":"svc-a","scope":"service"}}`, "availability_pool", 503},
		{"unknown_scope", `{"error":{"code":"insufficient_quota","organization_id":"org-a","scope":"user"}}`, "", 429},
		{"wrong_pool_scope", `{"error":{"code":"insufficient_quota","organization_id":"org-a","scope":"project"}}`, "", 429},
		{"malformed_scope", `{"error":{"code":"insufficient_quota","organization_id":"org-a","scope":{"value":"model"}}}`, "", 429},
		{"null_scope", `{"error":{"code":"insufficient_quota","organization_id":"org-a","scope":null}}`, "", 429},
		{"empty_scope", `{"error":{"code":"insufficient_quota","organization_id":"org-a","scope":""}}`, "", 429},
		{"numeric_model", `{"error":{"code":"insufficient_quota","organization_id":"org-a","scope":"model","model":7}}`, "", 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			e := FailureEvidenceFromResponse(tc.status, http.Header{}, []byte(tc.raw), a, now)
			require.Equal(t, tc.wantKind, e.SharedKind)
			d := scheduling.ClassifyFailure(e, a, now)
			if tc.wantKind == "" {
				require.Equal(t, "account_model", d.Scope)
				require.False(t, d.Hard)
				require.Equal(t, a.ModelKey(), d.Key)
			} else {
				require.Equal(t, a.SharedKey(e.SharedKind, e.SharedPool, ""), d.Key)
			}
		})
	}
}
