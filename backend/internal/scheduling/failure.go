package scheduling

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// FailureDecision never spends/refunds an attempt or overrides owner/commit.
type FailureDecision struct {
	Class  string    `json:"class"`
	Reason string    `json:"reason"`
	Retry  string    `json:"retry"`
	Effect string    `json:"effect"`
	Scope  string    `json:"scope"`
	Key    string    `json:"key,omitempty"`
	Until  time.Time `json:"until,omitempty"`
	Hard   bool      `json:"hard,omitempty"`
}
type FailureEvidence struct {
	Status               int
	Code                 string
	Trusted              bool
	GlobalInput          bool
	Capability           bool
	ClientCancelled      bool
	Committed            bool
	ReplaySafe           bool
	OwnerPinned          bool
	NotSent              bool
	FirstSemanticTimeout bool
	// These require authenticated structured evidence, never HTTP status or URL.
	SharedKind  string
	SharedPool  string
	SharedModel bool
	RetryAfter  time.Time
}
type AccountFailureDomains struct {
	AccountID          int64  `json:"account_id"`
	Version            int64  `json:"version"`
	QuotaPoolID        string `json:"quota_pool_id"`
	AvailabilityPoolID string `json:"availability_pool_id"`
}

func (c AccountFailureDomains) Validate() error {
	if c.AccountID <= 0 || c.Version < 0 {
		return ErrInvalidControl
	}
	for _, id := range []string{c.QuotaPoolID, c.AvailabilityPoolID} {
		if len(id) > 128 || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\r\n\t") {
			return ErrInvalidControl
		}
	}
	return nil
}

// Separate credential/membership fences are not health generations or pause epochs.
type FailureAdmission struct {
	AccountID         int64                 `json:"account_id"`
	CredentialOwnerID int64                 `json:"credential_owner_id"`
	Model             string                `json:"model"`
	Credential        string                `json:"credential"`
	HealthIdentity    string                `json:"health_identity"`
	Domains           AccountFailureDomains `json:"domains"`
	Keys              []string              `json:"keys"`
	ProbeVersions     map[string]int64      `json:"probe_versions,omitempty"`
}

func failureKey(parts ...string) string {
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func CredentialFingerprint(raw []byte) string {
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		return ""
	}
	credentials := map[string]any{}
	for _, key := range []string{"api_key", "access_token", "refresh_token", "client_secret", "client_id", "token", "session_token", "session_key", "cookie", "cookies", "base_url", "organization_id", "project_id", "tenant_id"} {
		if value, ok := data[key]; ok {
			credentials[key] = value
		}
	}
	// Unknown provider schemas stay conservatively fenced by their full snapshot.
	if len(credentials) == 0 {
		credentials = data
	}
	canonical, _ := json.Marshal(credentials)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
func (a FailureAdmission) AccountKey() string {
	ownerID := a.CredentialOwnerID
	if ownerID <= 0 {
		ownerID = a.AccountID
	}
	return failureKey("deployment", "credential", strconv.FormatInt(ownerID, 10), a.Credential)
}
func (a FailureAdmission) ModelKey() string {
	identity := a.HealthIdentity
	if identity == "" {
		identity = a.Credential
	}
	return failureKey("deployment", "account_model", strconv.FormatInt(a.AccountID, 10), identity, a.Model)
}
func (a FailureAdmission) SharedKey(kind, pool, model string) string {
	return failureKey("deployment", kind, pool, model)
}
func (a *FailureAdmission) SetKeys() {
	a.Keys = []string{a.AccountKey(), a.ModelKey()}
	for _, v := range []struct{ k, p string }{{"quota_pool", a.Domains.QuotaPoolID}, {"availability_pool", a.Domains.AvailabilityPoolID}} {
		if v.p != "" {
			a.Keys = append(a.Keys, a.SharedKey(v.k, v.p, ""), a.SharedKey(v.k, v.p, a.Model))
		}
	}
}
func ParseFailureRetryAfter(raw string, now time.Time) time.Time {
	if n, e := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); e == nil && n > 0 && n <= 86400*365*10 {
		return now.Add(time.Duration(n) * time.Second)
	}
	if t, e := http.ParseTime(raw); e == nil && t.After(now) {
		return t
	}
	return time.Time{}
}
func ClassifyFailure(e FailureEvidence, a FailureAdmission, now time.Time) FailureDecision {
	d := FailureDecision{Class: "unclassified", Reason: "scope_unknown", Retry: "next_eligible", Effect: "none", Scope: "request"}
	code := strings.ToLower(strings.TrimSpace(e.Code))
	switch {
	case e.GlobalInput:
		d.Class, d.Reason, d.Retry = "invalid_request", "global_input", "stop"
	case e.Trusted && (e.Status == 400 || e.Status == 422) && code == "invalid_parameter":
		d.Class, d.Reason, d.Retry = "invalid_request", "request_validation_rejected", "stop"
	case e.Trusted && e.Status == 401 && (code == "invalid_api_key" || code == "invalid_token" || code == "token_revoked" || code == "authentication_error"):
		d.Class, d.Reason, d.Effect, d.Scope, d.Key, d.Hard = "authentication", "credential_rejected", "auth_block", "credential", a.AccountKey(), true
	case e.Trusted && (e.Capability || code == "model_not_allowed" || code == "model_not_found" || code == "permission_denied_model"):
		d.Class, d.Reason, d.Effect, d.Scope, d.Key = "model_capability", "model_unavailable", "cooldown", "account_model", a.ModelKey()
		d.Until = now.Add(time.Minute)
	case e.Trusted && e.Status == 429:
		d.Class, d.Reason, d.Effect, d.Scope, d.Key = "rate_limit", "scope_unknown", "cooldown", "account_model", a.ModelKey()
		d.Until = now.Add(30 * time.Second)
	case e.Trusted && e.Status >= 500:
		d.Class, d.Reason, d.Effect, d.Scope, d.Key = "upstream_failure", "local_upstream_failure", "observe_failure", "account_model", a.ModelKey()
	case e.NotSent:
		d.Class, d.Reason, d.Effect, d.Scope, d.Key = "transport", "proven_not_sent", "observe_failure", "account_model", a.ModelKey()
	case e.FirstSemanticTimeout:
		d.Class, d.Reason, d.Effect, d.Scope, d.Key = "first_semantic_timeout", "outcome_unknown", "observe_failure", "account_model", a.ModelKey()
	}
	shared := e.Trusted && ((e.SharedKind == "quota_pool" && e.SharedPool != "" && e.SharedPool == a.Domains.QuotaPoolID && e.Status == 429) || (e.SharedKind == "availability_pool" && e.SharedPool != "" && e.SharedPool == a.Domains.AvailabilityPoolID && (e.NotSent || e.Status >= 500)))
	if shared {
		model := ""
		if e.SharedModel {
			model = a.Model
		}
		d.Scope, d.Key, d.Reason = e.SharedKind, a.SharedKey(e.SharedKind, e.SharedPool, model), "declared_and_proven_shared_scope"
		d.Effect = "cooldown"
		d.Until = now.Add(30 * time.Second)
		if e.SharedKind == "quota_pool" && (code == "insufficient_quota" || code == "quota_exhausted") && e.RetryAfter.IsZero() {
			d.Hard = true
			d.Until = time.Time{}
		}
	}
	if d.Key != "" && !e.RetryAfter.IsZero() && e.RetryAfter.After(now) && !d.Hard {
		d.Effect = "cooldown"
		d.Until = e.RetryAfter
	}
	if e.ClientCancelled || e.Committed || e.OwnerPinned || !e.ReplaySafe {
		d.Retry = "stop"
	}
	if e.ClientCancelled && !e.Trusted {
		d.Class, d.Reason, d.Effect, d.Scope, d.Key = "client_cancelled", "local_cancel", "none", "request", ""
	}
	return d
}
