package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/tidwall/gjson"
)

type controlledFailureAdmissionKey struct{}

// The final outbound request, after mapping, is the authority for model scope.
// No model/credential value is exposed in error messages or diagnostic bodies.
func (s *ControlledSchedulingService) prepareControlledFailureRequest(req *http.Request, accountID int64) (prepared *http.Request, prepareErr error) {
	if !ControlledSchedulingEnabled(req.Context()) {
		return req, nil
	}
	prepare, stopPreparation := controlledPreparationContext(req.Context(), controlledRequest(req.Context()))
	defer stopPreparation()
	defer func() { prepareErr = controlledPreparationError(req.Context(), prepare, prepareErr) }()
	model := ""
	if req.GetBody != nil {
		reader, err := req.GetBody()
		if err != nil {
			return req, err
		}
		outboundModel, _, valid := ReadControlledOutboundMetadata(reader)
		_ = reader.Close()
		if valid {
			model = outboundModel
		}
	}
	if model == "" {
		path := req.URL.Path
		if i := strings.Index(path, "/models/"); i >= 0 {
			model = strings.SplitN(path[i+8:], ":", 2)[0]
		}
	}
	if model == "" {
		r := controlledRequest(req.Context())
		r.mu.Lock()
		model = r.Model
		r.mu.Unlock()
		account, err := s.accounts.GetByID(prepare, accountID)
		if err != nil {
			return req, err
		}
		if account == nil {
			return req, scheduling.ErrNoCandidate
		}
		model = account.GetMappedModel(model)
		// Unknown mappings must not be expanded into shared-model gates by adapters.
	}
	a, err := s.Store.FreezeFailureAdmission(prepare, accountID, model)
	if err != nil {
		return req, err
	}
	return req.WithContext(context.WithValue(req.Context(), controlledFailureAdmissionKey{}, &a)), nil
}
func controlledFailureAdmission(ctx context.Context) *scheduling.FailureAdmission {
	a, _ := ctx.Value(controlledFailureAdmissionKey{}).(*scheduling.FailureAdmission)
	return a
}

func (s *ControlledSchedulingService) controlledFailureCandidate(ctx context.Context, a *Account, model string) (bool, []string, error) {
	if a == nil {
		return false, nil, nil
	}
	state, err := s.Store.InspectFailureDomains(ctx, a.ID, a.GetMappedModel(model))
	if err != nil {
		return false, nil, err
	}
	frozen := scheduling.FailureAdmission{AccountID: a.ID, Model: a.GetMappedModel(model), Domains: state.Domains}
	domains := controlledAccountFailureDomains(a, model)
	for _, v := range []struct{ k, p string }{{"quota_pool", state.Domains.QuotaPoolID}, {"availability_pool", state.Domains.AvailabilityPoolID}} {
		if v.p != "" {
			domains = append(domains, frozen.SharedKey(v.k, v.p, ""), frozen.SharedKey(v.k, v.p, frozen.Model))
		}
	}
	return state.Eligible, domains, nil
}

// FailureEvidenceFromResponse only accepts structured provider fields. A code
// with no matching declared pool identifier remains local, even on shared URLs.
func FailureEvidenceFromResponse(status int, header http.Header, raw []byte, a scheduling.FailureAdmission, now time.Time) scheduling.FailureEvidence {
	e := scheduling.FailureEvidence{Status: status, Trusted: true, ReplaySafe: true, RetryAfter: scheduling.ParseFailureRetryAfter(header.Get("Retry-After"), now)}
	// Truncated or malformed documents cannot authenticate code or scope.
	if !json.Valid(raw) {
		return e
	}
	code := gjson.GetBytes(raw, "error.code").String()
	if code == "" {
		code = gjson.GetBytes(raw, "error.type").String()
	}
	e.Code = code
	switch code {
	case "unsupported_parameter", "model_not_allowed", "model_not_found", "permission_denied_model":
		e.Capability = true
	}
	sharedScope := ""
	if status == 429 {
		switch code {
		case "organization_quota_exceeded", "organization_rate_limit_exceeded", "insufficient_quota", "quota_exhausted":
			id := gjson.GetBytes(raw, "error.organization_id").String()
			if id != "" && id == a.Domains.QuotaPoolID {
				e.SharedKind, e.SharedPool = "quota_pool", id
				sharedScope = "organization"
			}
		case "project_quota_exceeded", "project_rate_limit_exceeded":
			id := gjson.GetBytes(raw, "error.project_id").String()
			if id != "" && id == a.Domains.QuotaPoolID {
				e.SharedKind, e.SharedPool = "quota_pool", id
				sharedScope = "project"
			}
		}
	}
	if status >= 500 && code == "service_unavailable" {
		id := gjson.GetBytes(raw, "error.service_id").String()
		if id != "" && id == a.Domains.AvailabilityPoolID {
			e.SharedKind, e.SharedPool = "availability_pool", id
			sharedScope = "service"
		}
	}
	if scope := gjson.GetBytes(raw, "error.scope"); e.SharedKind != "" && scope.Exists() {
		validScope := scope.Type == gjson.String && scope.String() == sharedScope
		if scope.Type == gjson.String && scope.String() == "model" {
			model := gjson.GetBytes(raw, "error.model")
			e.SharedModel = model.Type == gjson.String && a.Model != "" && model.String() == a.Model
			validScope = e.SharedModel
		}
		// Missing/mismatched model identity and unknown explicit scopes cannot
		// turn narrower provider evidence into a whole-pool failure. The HTTP
		// status/code still supplies its independently sufficient local evidence.
		if !validScope {
			e.SharedKind, e.SharedPool = "", ""
		}
	}
	return e
}

// Called before exposing an error response. The bounded prefix is restored byte
// for byte; it is never stored in metrics, audit logs, or account configuration.
func (d *controlledDispatch) observeFailureResponse(response *http.Response) {
	if d == nil || d.ticket.Failure == nil || response == nil || response.StatusCode < 400 {
		return
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	readers := []io.Reader{bytes.NewReader(raw)}
	if readErr != nil {
		readers = append(readers, &failureReadError{err: readErr})
	}
	readers = append(readers, response.Body)
	response.Body = &failurePrefixBody{Reader: io.MultiReader(readers...), Closer: response.Body}
	e := FailureEvidenceFromResponse(response.StatusCode, response.Header, raw, *d.ticket.Failure, time.Now())
	d.mu.Lock()
	d.failureEvidence = e
	d.mu.Unlock()
	decision := d.classifySchedulingFailure(e)
	if decision.Retry == "stop" {
		d.request.mu.Lock()
		d.request.ReplaySafe = false
		d.request.mu.Unlock()
	}
}

type failureReadError struct{ err error }

// Called with d.mu held, only for a validated failure frame. Protocol evidence
// is local to this account/model: an SSE code alone cannot prove shared scope.
func (d *controlledDispatch) observeProtocolFailureLocked(v gjson.Result) {
	if d.status < http.StatusOK || d.status >= http.StatusMultipleChoices {
		return
	}
	kind, status := v.Get("type").String(), v.Get("response.status").String()
	if kind != "error" && kind != "response.failed" && status != "failed" {
		return
	}
	detail := v.Get("error")
	if kind == "response.failed" || status == "failed" {
		detail = v.Get("response.error")
	}
	if !detail.IsObject() {
		return
	}
	code := detail.Get("code")
	if !code.Exists() || code.Type == gjson.Null || code.String() == "" {
		code = detail.Get("type")
	}
	if code.Type != gjson.String {
		return
	}
	switch code.String() {
	case "server_error", "internal_error", "internal_server_error", "overloaded_error", "service_unavailable":
		d.failureEvidence = scheduling.FailureEvidence{Status: d.status, Code: code.String(), Trusted: true, ProtocolFailure: true, ReplaySafe: true}
	}
}

func (r *failureReadError) Read([]byte) (int, error) {
	err := r.err
	if err == nil {
		return 0, io.EOF
	}
	r.err = nil
	return 0, err
}

type failurePrefixBody struct {
	io.Reader
	io.Closer
}

// This helper deliberately runs even after client cancellation or budget
// exhaustion, so independent authenticated quota/credential evidence is retained.
func (d *controlledDispatch) classifyFailureDomains(outcome string, err error) scheduling.FailureDecision {
	if d == nil || d.ticket.Failure == nil {
		return scheduling.FailureDecision{Effect: "none"}
	}
	d.mu.Lock()
	e := d.failureEvidence
	timedOut := d.timeout
	transportAttempted := d.sent
	e.FirstSemanticTimeout = timedOut && !d.clipped && d.semanticObservable
	e.AttemptTimeout = timedOut && !d.clipped && !d.semanticObservable
	d.mu.Unlock()
	d.request.mu.Lock()
	e.ReplaySafe = d.request.ReplaySafe
	e.OwnerPinned = d.request.owner
	e.Committed = d.request.Ledger != nil && d.request.Ledger.Snapshot().Committed
	d.request.mu.Unlock()
	// A failed local preparation is not evidence against an upstream account.
	// A dial failure after MarkSent is an attempted transport and may cool down.
	e.NotSent = outcome == "not_sent" && transportAttempted
	e.ClientCancelled = (errors.Is(err, context.Canceled) && !timedOut) || (d.request.clientContext != nil && d.request.clientContext.Err() != nil)
	decision := d.classifySchedulingFailure(e)
	if decision.Key != "" && (decision.Scope == "quota_pool" || decision.Scope == "availability_pool") && d.request.Ledger != nil {
		d.request.Ledger.BlockFailureDomain(decision.Key)
	}

	return decision
}

// Only provider scopes already authenticated by existing account metadata are
// added. A shared project_id alone does not prove a project-wide quota failure.
func controlledAccountFailureDomains(account *Account, model string) []string {
	if account == nil || !account.IsGrokOAuth() {
		return nil
	}
	fingerprint := grokTeamFingerprint(accountGrokTeamID(account))
	model = canonicalOpenAIAccountSchedulingModel(account, model)
	if fingerprint == "" || model == "" {
		return nil
	}
	return []string{"grok_team_model:" + grokTeamModelRateLimitKey(fingerprint, model)}
}

func blockControlledGrokTeamModelLimit(ctx context.Context, account *Account, model string) {
	if !ControlledSchedulingEnabled(ctx) {
		return
	}
	r := controlledRequest(ctx)
	r.mu.Lock()
	ledger := r.Ledger
	r.mu.Unlock()
	if ledger == nil {
		return
	}
	for _, domain := range controlledAccountFailureDomains(account, model) {
		ledger.BlockFailureDomain(domain)
	}
}

// The account-pool selector uses one short failure circuit. Authentication,
// quota and model-specific evidence keep their existing authoritative scope.
func (d *controlledDispatch) classifySchedulingFailure(e scheduling.FailureEvidence) scheduling.FailureDecision {
	d.request.mu.Lock()
	accountPool := d.request.Policy.AccountPool
	d.request.mu.Unlock()
	if accountPool {
		return scheduling.ClassifyAccountPoolFailure(e, *d.ticket.Failure, time.Now())
	}
	return scheduling.ClassifyFailure(e, *d.ticket.Failure, time.Now())
}
