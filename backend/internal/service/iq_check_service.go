package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/liulixin-lex/xy2api/internal/domain"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai_compat"
)

var ErrIQCheckInvalid = infraerrors.BadRequest("IQ_CHECK_INVALID", "Invalid OpenAI IQ check settings")
var ErrIQCheckDisabled = infraerrors.BadRequest("IQ_CHECK_DISABLED", "IQ check is disabled")

type IQCheckClaim struct {
	RoundDeadline  *time.Time
	TimeoutSeconds int
	AccountID      int64
	Token          string
	Revision       string
	StartedAt      time.Time
	Profile        domain.IQProfile
	Protocol       string
}

type IQCheckAttempt struct {
	Number     int                 `json:"attempt_no"`
	StartedAt  time.Time           `json:"started_at"`
	FinishedAt *time.Time          `json:"finished_at"`
	Status     string              `json:"status"`
	Reason     string              `json:"reason"`
	LatencyMS  int64               `json:"latency_ms"`
	Diagnostic *iqcheck.Diagnostic `json:"diagnostic,omitempty"`
}
type IQCheckRecord struct {
	Attempts         []IQCheckAttempt    `json:"attempts"`
	Diagnostic       *iqcheck.Diagnostic `json:"diagnostic,omitempty"`
	ID               int64               `json:"id"`
	PromptVersion    string              `json:"prompt_version"`
	Model            string              `json:"model"`
	Effort           string              `json:"effort"`
	Status           string              `json:"status"`
	Answer           string              `json:"answer"`
	Reason           string              `json:"reason,omitempty"`
	StartedAt        time.Time           `json:"started_at"`
	FinishedAt       *time.Time          `json:"finished_at"`
	LatencyMS        int64               `json:"latency_ms"`
	NormalizedAnswer *string             `json:"normalized_answer"`
	AnswerFormat     string              `json:"answer_format"`
	OutputMode       string              `json:"output_mode"`
	FormatCompliant  *bool               `json:"format_compliant"`
	GraderVersion    string              `json:"grader_version"`
	Protocol         string              `json:"protocol"`
	ReportedModel    *string             `json:"reported_model"`
	ConfigRevision   string              `json:"config_revision"`
}

type IQCheckRepository interface {
	ConfigureIQCheck(context.Context, int64, domain.IQCheckSettings) (domain.IQCheck, error)
	QueueIQCheck(context.Context, int64) error
	ClaimIQChecks(context.Context, time.Time, int) ([]IQCheckClaim, error)
	CompleteIQCheck(context.Context, IQCheckClaim, iqcheck.Result, time.Time) error
	ListIQCheckRecords(context.Context, int64) ([]IQCheckRecord, error)
}

func ValidateIQCheckSettings(platform string, settings *domain.IQCheckSettings) error {
	if settings == nil {
		return nil
	}
	if platform != PlatformOpenAI {
		return ErrIQCheckInvalid
	}
	if err := settings.Validate(); err != nil {
		return infraerrors.BadRequest("IQ_CHECK_INVALID", err.Error())
	}
	return nil
}

type iqStatusContextKey struct{}

func WithIQStatusFilter(ctx context.Context, status string) context.Context {
	return context.WithValue(ctx, iqStatusContextKey{}, status)
}
func IQStatusFilter(ctx context.Context) string {
	value, _ := ctx.Value(iqStatusContextKey{}).(string)
	return value
}
func ValidIQStatusFilter(status string) bool {
	return status == "" || status == "smart" || status == "degraded" || status == "unknown"
}

type IQCheckService struct {
	concurrency    *ConcurrencyService
	maxConcurrency int
	repo           IQCheckRepository
	accounts       AccountRepository
	tester         *AccountTestService
	tokens         *OpenAITokenProvider
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	wake           chan struct{}
	modelsMu       sync.Mutex
	modelsCache    map[string]IQModelCatalog
	modelsFlight   singleflight.Group
}

func ProvideIQCheckService(accounts AccountRepository, tester *AccountTestService, tokens *OpenAITokenProvider, concurrency *ConcurrencyService) *IQCheckService {
	repo, ok := accounts.(IQCheckRepository)
	maxConcurrency := iqMonitoringConfig()
	s := &IQCheckService{wake: make(chan struct{}, 1), concurrency: concurrency, maxConcurrency: maxConcurrency, repo: repo, accounts: accounts, tester: tester, tokens: tokens}
	if !ok {
		return s
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.run(ctx) }()
	return s
}

func (s *IQCheckService) Stop() {
	if s.cancel != nil {
		s.cancel()
		s.wg.Wait()
	}
}
func (s *IQCheckService) Configure(ctx context.Context, id int64, settings domain.IQCheckSettings) (domain.IQCheck, error) {
	if s.repo == nil {
		return domain.IQCheck{}, ErrIQCheckInvalid
	}
	return s.repo.ConfigureIQCheck(ctx, id, settings)
}
func (s *IQCheckService) Queue(ctx context.Context, id int64) error {
	if s.repo == nil {
		return ErrIQCheckInvalid
	}
	err := s.repo.QueueIQCheck(ctx, id)
	s.notify()
	return err
}
func (s *IQCheckService) Records(ctx context.Context, id int64) ([]IQCheckRecord, error) {
	if s.repo == nil {
		return nil, ErrIQCheckInvalid
	}
	return s.repo.ListIQCheckRecords(ctx, id)
}

func (s *IQCheckService) run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		claims, err := s.repo.ClaimIQChecks(ctx, time.Now().UTC(), s.maxConcurrency)
		if err != nil && ctx.Err() == nil {
			logger.LegacyPrintf("iq_check", "claim failed: %v", err)
		}
		for _, claim := range claims {
			s.wg.Add(1)
			go func(claim IQCheckClaim) {
				defer s.wg.Done()
				s.execute(ctx, claim)
			}(claim)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

func (s *IQCheckService) probe(ctx context.Context, id int64, claims ...IQCheckClaim) (result iqcheck.Result) {
	transport := "http"
	stateProtection := "unmanaged"
	stateTicketID := ""
	stateModel := ""
	stateFingerprint := ""
	stateRevision := ""
	started := time.Now()
	defer func() {
		if result.Diagnostic == nil {
			result.Diagnostic = (&iqcheck.Diagnostic{ParserVersion: iqcheck.ParserVersion, Stage: "request", Code: result.Reason, Transport: transport}).Bounded()
		}
		result.StateTicketID = stateTicketID
		result.StateModel = stateModel
		result.StateFingerprint = stateFingerprint
		result.StateRevision = stateRevision
		result.Diagnostic.StateProtection = stateProtection
		result.Diagnostic.TotalMS = time.Since(started).Milliseconds()
		if transport == "plugin" {
			result.Diagnostic.RetryVisibility = "unknown"
		} else {
			result.Diagnostic.RetryVisibility = "single_attempt"
		}
	}()
	if s.tester == nil {
		return iqcheck.Unknown("transport_unavailable")
	}
	account, err := s.accounts.GetByID(ctx, id)
	if err != nil {
		return iqcheck.Unknown("account_unavailable")
	}
	if account.Platform != PlatformOpenAI || account.IsCredentialShadow() {
		return iqcheck.Unknown("unsupported_model")
	}
	if s.tester.pluginManager != nil && s.tester.pluginManager.ShouldRouteOpenAIOAuth(account) {
		transport = "plugin"
	}
	profile := account.IQCheck.Profile()
	if len(claims) > 0 {
		if account.IQCheck.Revision != claims[0].Revision {
			return iqcheck.Unknown("cancelled_by_account_change")
		}
		profile = claims[0].Profile.Defaults()
	}
	isOAuth := account.IsOAuth()
	chat := !isOAuth && !openai_compat.ShouldUseResponsesAPI(account.Extra)
	if len(claims) > 0 && claims[0].Protocol != "" {
		protocol := "responses"
		if chat {
			protocol = "chat_completions"
		}
		if protocol != claims[0].Protocol {
			return iqcheck.Unknown("cancelled_by_account_change")
		}
	}
	apiURL := chatgptCodexAPIURL
	token := ""
	if isOAuth {
		if !account.IsOpenAIAgentIdentity() {
			if account.Type == AccountTypeOAuth && s.tokens != nil {
				// A failed IQ probe must not invoke the request-path missing-token quarantine.
				expiresAt := account.GetCredentialAsTime("expires_at")
				if !account.IsOpenAIPersonalAccessToken() && expiresAt != nil && !time.Now().Before(*expiresAt) && strings.TrimSpace(account.GetOpenAIRefreshToken()) == "" {
					return iqcheck.Unknown("authentication_unavailable")
				}
				token, err = s.tokens.GetAccessToken(ctx, account)
			} else {
				token = account.GetOpenAIAccessToken()
			}
		}
	} else if account.Type == AccountTypeAPIKey {
		token = account.GetOpenAIProtocolAPIKey()
		baseURL := account.GetOpenAIBaseURL()
		if baseURL == "" {
			baseURL = "https://api.openai.com"
		}
		baseURL, err = s.tester.validateUpstreamBaseURL(baseURL)
		if err == nil {
			if chat {
				apiURL = buildOpenAIChatCompletionsURL(baseURL)
			} else {
				apiURL = buildOpenAIResponsesURLForPlatform(account.Platform, baseURL)
			}
		}
	} else {
		return iqcheck.Unknown("unsupported_account_type")
	}
	if err != nil || (token == "" && !account.IsOpenAIAgentIdentity()) {
		return iqcheck.Unknown("authentication_unavailable")
	}
	payload, _ := json.Marshal(iqcheck.Payload(chat, profile))
	req, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileOpenAI)), http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return iqcheck.Unknown("invalid_endpoint")
	}
	req = req.WithContext(WithHTTPUpstreamSingleAttempt(req.Context()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if account.IsOpenAIAgentIdentity() {
		headers, err := buildAgentIdentityAuthenticationHeaders(ctx, s.accounts, s.tester.agentIdentityWS, &s.tester.agentIdentityTaskMu, account)
		if err != nil {
			return iqcheck.Unknown("authentication_unavailable")
		}
		for key, values := range headers {
			req.Header[key] = values
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if isOAuth {
		req.Host = "chatgpt.com"
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		identity := resolveCodexOutboundIdentity("")
		req.Header.Set("Originator", identity.originator)
		req.Header.Set("User-Agent", identity.userAgent)
		setOpenAIChatGPTAccountHeaders(req.Header, account)
		enforceCodexIdentityHeadersWithUA(req.Header, account.GetOpenAIUserAgent())
	} else {
		applyIQAPIKeyHeaders(req.Header)
	}
	account.ApplyHeaderOverrides(req.Header)
	// Disable net/http replay of a POST on a reused connection, including overrides.
	req.GetBody = nil
	req.Header.Del("Idempotency-Key")
	req.Header.Del("X-Idempotency-Key")
	for _, name := range []string{"Session_id", "Conversation_id", "X-Conversation-Id", "Previous_response_id"} {
		req.Header.Del(name)
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	if gateway := s.tester.openaiGatewayService; gateway != nil {
		outbound := gateway.openAICodexTicketOutboundModel(account, profile.Model, false)
		// IQ uses the same final outbound model as business routing.
		if outbound != profile.Model {
			payloadProfile := profile
			payloadProfile.Model = outbound
			encoded, e := json.Marshal(iqcheck.Payload(chat, payloadProfile))
			if e != nil {
				return iqcheck.Unknown("request_failed")
			}
			req.Body = io.NopCloser(bytes.NewReader(encoded))
			req.ContentLength = int64(len(encoded))
			req.GetBody = nil
		}
		if codexAccountTicketConfigOf(account).manages(outbound) && gateway.openAICodexTicketEnabledContext(ctx) {
			stateProtection = "unprotected"
			stateModel = outbound
			stateFingerprint = codexTicketFixedProxyFingerprint(account)
			stateRevision = codexAccountTicketConfigOf(account).Revision
		}
		if e := gateway.applyOpenAICodexTicketToRequest(ctx, account, outbound, req); e != nil {
			return iqcheck.Unknown("waiting_state")
		}
		if receipt, _ := req.Context().Value(codexTicketReceiptContextKey{}).(*codexTicketReceipt); receipt != nil {
			stateProtection = "protected"
			stateModel = receipt.model
			stateTicketID = receipt.identity()
		}
	}
	if gateway := s.tester.openaiGatewayService; gateway != nil && codexAccountTicketConfigOf(account).Enabled {
		budgetModel := gateway.openAICodexTicketOutboundModel(account, profile.Model, false)
		reservation, e := gateway.reserveCodexBudget(ctx, account.ID, budgetModel, 1, "iq")
		if e != nil {
			return iqcheck.Unknown("background_budget")
		}
		defer gateway.releaseCodexBudget(account.ID, reservation)
		if e = gateway.consumeCodexBudget(ctx, account.ID, budgetModel, "iq", reservation); e != nil {
			return iqcheck.Unknown("background_budget")
		}
	}
	if err := applyIQProbeRequestIdentity(req, account); err != nil {
		return iqcheck.Unknown("request_failed")
	}
	var resp *http.Response
	// STATE-managed probes use the exact business transport. A tester-only TLS
	// override must not change the transport against which the ticket was verified.
	if gateway := s.tester.openaiGatewayService; gateway != nil && stateProtection != "unmanaged" {
		resp, err = gateway.doOpenAIUpstream(req, proxyURL, account)
	} else {
		resp, err = s.tester.doOpenAIAccountTestUpstream(req, proxyURL, account, true)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return iqcheck.Unknown("interrupted")
		}
		var networkError net.Error
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout() {
			return iqcheck.Unknown("timeout")
		}
		return iqcheck.Unknown("request_failed")
	}
	if gateway := s.tester.openaiGatewayService; gateway != nil && stateProtection == "unmanaged" {
		gateway.observeCodexTicketResponse(req, resp)
	}
	defer func() { _ = resp.Body.Close() }()
	headerMS := time.Since(started).Milliseconds()
	result = iqcheck.ParseHTTP(resp, chat, transport)
	if result.Diagnostic != nil {
		result.Diagnostic.FirstByteMS += headerMS
	}
	return result
}

func (s *IQCheckService) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
