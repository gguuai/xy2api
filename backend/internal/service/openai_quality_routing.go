package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// The optional cache extension batches quality and legacy sticky reads. Existing
// GatewayCache implementations remain compatible and use local protection.
type OpenAIQualityRoutingStore interface {
	ReadOpenAIQualityRouting(context.Context, string, int64, []string) (*OpenAIQualityState, []int64, error)
	UpdateOpenAIQualityRouting(context.Context, string, OpenAIQualityChange, time.Duration) (*OpenAIQualityState, error)
}

type OpenAIQualityAvoid struct {
	Provider string `json:"provider"`
	At       int64  `json:"at"`
	Until    int64  `json:"until"`
}

type OpenAIQualityState struct {
	Owner      int64                         `json:"owner"`
	Revision   uint64                        `json:"revision"`
	Generation uint64                        `json:"generation"`
	Binding    int64                         `json:"binding"`
	Avoid      map[string]OpenAIQualityAvoid `json:"avoid"`
	Rotation   string                        `json:"rotation"`
	Expires    int64                         `json:"expires"`
}

type OpenAIQualityChange struct {
	PreviousBinding int64  `json:"previous_binding"`
	Kind            string `json:"kind"`
	Generation      uint64 `json:"generation"`
	AccountID       int64  `json:"account_id"`
	Provider        string `json:"provider"`
	Now             int64  `json:"now"`
	Until           int64  `json:"until"`
	Expires         int64  `json:"expires"`
	Rotation        string `json:"rotation"`
}

type openAIQualityLocal struct {
	mu    sync.Mutex
	state OpenAIQualityState
}

type openAIQualityContextKey struct{}
type openAIQualityRequest struct {
	mu              sync.Mutex
	scope, model    string
	group           int64
	apiID           int64
	session         string
	loaded          bool
	state           OpenAIQualityState
	routeGeneration uint64
	sticky          int64
	pinned          bool
	replay          []json.RawMessage
	replayResponse  string
	replayComplete  bool
}

var openAIQualityCounters sync.Map // event -> *atomic.Uint64
var openAIQualitySyncSlots = make(chan struct{}, 64)

func OpenAIQualityRoutingMetrics() map[string]uint64 {
	var out map[string]uint64
	openAIQualityCounters.Range(func(k, v any) bool {
		key, keyOK := k.(string)
		counter, counterOK := v.(*atomic.Uint64)
		if keyOK && counterOK {
			if out == nil {
				out = make(map[string]uint64)
			}
			out[key] = counter.Load()
		}
		return true
	})
	return out
}

func qualityEvent(event, scope string, account int64, generation uint64, details ...any) {
	v, _ := openAIQualityCounters.LoadOrStore(event, &atomic.Uint64{})
	if counter, ok := v.(*atomic.Uint64); ok {
		counter.Add(1)
	}
	attrs := []any{"event", event, "scope", scope, "account_id", account, "generation", generation}
	slog.Info("openai_quality_routing", append(attrs, details...)...)
}

var qualityModelPattern = regexp.MustCompile(`^gpt-([0-9]+)(?:\.([0-9]+))?(?:-(astra|sol|terra|luna))?((?:-[a-z0-9]+)*)$`)
var qualityDatePattern = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}(?:$|-(?:max|high|medium|low|minimal|xhigh|none)$)`)

// Deliberately does not use the forgiving Codex model mapper: unknown names and
// suffixes are not evidence of a downgrade.
func canonicalQualityModel(raw string) string {
	if len(raw) > upstreamResponseModelMaxLength {
		return ""
	}
	v := strings.ToLower(strings.TrimSpace(raw))
	if slash := strings.LastIndexByte(v, '/'); slash >= 0 {
		v = strings.TrimSpace(v[slash+1:])
	}
	v = strings.ReplaceAll(v, "_", "-")
	if strings.ContainsAny(v, " \t\r\n") {
		v = strings.Join(strings.Fields(v), "-")
	}
	for strings.Contains(v, "--") {
		v = strings.ReplaceAll(v, "--", "-")
	}
	if strings.HasPrefix(v, "gpt") && len(v) > 3 && v[3] >= '0' && v[3] <= '9' {
		v = "gpt-" + v[3:]
	}
	if loc := qualityDatePattern.FindStringIndex(v); loc != nil {
		dateEnd := loc[0] + 11
		if _, err := time.Parse("2006-01-02", v[loc[0]+1:dateEnd]); err != nil {
			return ""
		}
		v = v[:loc[0]] + v[dateEnd:]
	}
	m := qualityModelPattern.FindStringSubmatch(v)
	if m == nil {
		return ""
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 5 || major == 5 && minor < 6 {
		return ""
	}
	if m[4] != "" {
		switch m[4] {
		case "-max", "-high", "-medium", "-low", "-minimal", "-xhigh", "-none":
		default:
			return ""
		}
	}
	base := "gpt-" + m[1]
	if m[2] != "" {
		base += "." + m[2]
	}
	if m[3] != "" {
		return base + "-" + m[3]
	}
	switch base {
	case "gpt-6":
		return "gpt-6-astra"
	case "gpt-5.6":
		return "gpt-5.6-sol"
	}
	return base
}

func (s *OpenAIGatewayService) qualityConfig() config.OpenAIQualityRoutingConfig {
	if s.cfg != nil {
		return s.cfg.Gateway.OpenAIQualityRouting
	}
	return config.OpenAIQualityRoutingConfig{}
}

func (s *OpenAIGatewayService) qualityDowngrade(outbound, observed string) bool {
	from, to := canonicalQualityModel(outbound), canonicalQualityModel(observed)
	if from == "" || to == "" || from == to {
		return false
	}
	if to == "gpt-5.6-luna" {
		switch from {
		case "gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra":
			return true
		}
	}
	for _, r := range s.qualityConfig().Rules {
		if canonicalQualityModel(r.From) == from && canonicalQualityModel(r.To) == to {
			return true
		}
	}
	return false
}

func qualityDigest(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}

func qualityRequest(ctx context.Context) *openAIQualityRequest {
	if ctx == nil || Sub2APISchedulingEnabled(ctx) {
		return nil
	}
	q, _ := ctx.Value(openAIQualityContextKey{}).(*openAIQualityRequest)
	return q
}

func CopyOpenAIQualityRoutingContext(ctx, source context.Context) context.Context {
	if q := qualityRequest(source); q != nil {
		return context.WithValue(ctx, openAIQualityContextKey{}, q)
	}
	return ctx
}

// OpenAIQualityRerouteError carries an unsent next turn. It is not a transport
// failure and must not replay a completed turn or charge a failover attempt.
type OpenAIQualityRerouteError struct {
	Payload []byte
	Model   string
}

func (*OpenAIQualityRerouteError) Error() string {
	return "quality routing: move unsent websocket turn"
}

func (s *OpenAIGatewayService) AdvanceOpenAIQualityTurn(ctx context.Context, account *Account, body []byte, model string) error {
	q := qualityRequest(ctx)
	if q == nil || account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeAPIKey {
		return nil
	}
	canonical := canonicalQualityModel(model)
	q.mu.Lock()
	oldModel, oldGeneration := q.model, q.routeGeneration
	q.model = canonical
	q.scope = qualityDigest(fmt.Sprintf("%d\x00%d\x00%s\x00%s", q.apiID, q.group, q.session, canonical))
	q.loaded = false
	q.state = OpenAIQualityState{}
	q.sticky = 0
	previous := gjson.GetBytes(body, "previous_response_id").String()
	q.pinned = previous != "" || qualityIncompleteTools(body)
	replay, replayComplete := q.replay, q.replayComplete && previous != "" && previous == q.replayResponse
	q.mu.Unlock()
	if canonical == "" {
		return nil
	}
	s.loadQuality(ctx, q)
	q.mu.Lock()
	st := cloneQualityState(q.state)
	scope, pinned := q.scope, q.pinned
	q.mu.Unlock()
	if s.qualityConfig().EffectiveMode() != "enforce" || st.Generation == 0 || oldModel == canonical && st.Generation <= oldGeneration {
		return nil
	}
	bad, affected := st.Avoid[strconv.FormatInt(account.ID, 10)]
	if !affected || bad.Until <= time.Now().UnixMilli() {
		return nil
	}
	if pinned && replayComplete {
		items, exists, extractErr := openAIWSExtractNormalizedInputSequence(body)
		if extractErr == nil && exists {
			merged := combineOpenAIWSReplayItems(replay, items)
			if rebuilt, rebuildErr := setOpenAIWSPayloadInputSequence(body, merged, true); rebuildErr == nil {
				rebuilt = RemovePreviousResponseIDFromBody(rebuilt)
				if !qualityIncompleteTools(rebuilt) {
					body = rebuilt
					pinned = false
					q.mu.Lock()
					q.pinned = false
					q.mu.Unlock()
				}
			}
		}
	}
	if pinned {
		qualityEvent("continuation_pinned", scope, account.ID, st.Generation)
		return nil
	}
	return &OpenAIQualityRerouteError{Payload: s.ReplaceModelInBody(body, model), Model: model}
}

// Only a completed turn with a known complete input/output chain can authorize
// removing previous_response_id. Bound memory; an incomplete chain stays pinned.
func rememberOpenAIQualityWSTurn(ctx context.Context, body []byte, response string, output []json.RawMessage, complete bool) {
	q := qualityRequest(ctx)
	if q == nil {
		return
	}
	items, exists, err := openAIWSExtractNormalizedInputSequence(body)
	q.mu.Lock()
	defer q.mu.Unlock()
	previous := gjson.GetBytes(body, "previous_response_id").String()
	valid := complete && exists && err == nil && !qualityIncompleteTools(body)
	if previous != "" {
		valid = complete && exists && err == nil && q.replayComplete && q.replayResponse == previous
		if valid {
			items = combineOpenAIWSReplayItems(q.replay, items)
		}
	}
	items = combineOpenAIWSReplayItems(items, output)
	bytes := 0
	for _, item := range items {
		bytes += len(item)
	}
	q.replayComplete = valid && bytes <= 8*1024*1024
	q.replayResponse = response
	q.replay = nil
	if q.replayComplete {
		q.replay = items
	}
}

// AttachOpenAIQualityRouting is called after the final local session identity
// is resolved. It never changes that identity or the incoming request bytes.
func (s *OpenAIGatewayService) AttachOpenAIQualityRouting(c *gin.Context, session string, body []byte) {
	if s == nil || c == nil || c.Request == nil || session == "" || s.qualityConfig().EffectiveMode() == "off" || Sub2APISchedulingEnabled(c.Request.Context()) || isGrokRequestContext(c) {
		return
	}
	apiID := getAPIKeyIDFromContext(c)
	model := canonicalQualityModel(gjson.GetBytes(body, "model").String())
	if apiID <= 0 || model == "" || IsExplicitImageGenerationIntent(c.Request.URL.Path, model, body) {
		return
	}
	group := getOpenAIGroupIDFromContext(c)
	scope := qualityDigest(fmt.Sprintf("%d\x00%d\x00%s\x00%s", apiID, group, session, model))
	if current := qualityRequest(c.Request.Context()); current != nil {
		current.mu.Lock()
		same := current.scope == scope
		current.mu.Unlock()
		if same {
			return
		}
	}
	q := &openAIQualityRequest{scope: scope, model: model, group: group, apiID: apiID, session: session,
		pinned: strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()) != "" || qualityIncompleteTools(body)}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), openAIQualityContextKey{}, q))
}

func qualityIncompleteTools(body []byte) bool {
	if !bytes.Contains(body, []byte(`"function_call_output"`)) && !bytes.Contains(body, []byte(`"tool_result"`)) && !bytes.Contains(body, []byte(`"tool_call_id"`)) && !bytes.Contains(body, []byte(`"item_reference"`)) && !bytes.Contains(body, []byte(`"compaction"`)) {
		return false
	}
	calls := make(map[string]bool)
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == "item_reference" || item.Get("type").String() == "compaction" {
			return true
		}
		if item.Get("type").String() == "function_call" {
			calls[item.Get("call_id").String()] = true
		}
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == "function_call_output" && !calls[item.Get("call_id").String()] {
			return true
		}
	}
	for _, message := range gjson.GetBytes(body, "messages").Array() {
		for _, call := range message.Get("tool_calls").Array() {
			calls[call.Get("id").String()] = true
		}
		for _, block := range message.Get("content").Array() {
			if block.Get("type").String() == "tool_use" {
				calls[block.Get("id").String()] = true
			}
		}
	}
	for _, message := range gjson.GetBytes(body, "messages").Array() {
		if message.Get("role").String() == "tool" && !calls[message.Get("tool_call_id").String()] {
			return true
		}
		for _, block := range message.Get("content").Array() {
			if block.Get("type").String() == "tool_result" && !calls[block.Get("tool_use_id").String()] {
				return true
			}
		}
	}
	return false
}

func qualityProvider(a *Account) string {
	raw := a.GetOpenAIBaseURL()
	if a.IsOpenAIOAuthLike() {
		raw = "https://chatgpt.com/backend-api/codex"
	}
	if raw == "" {
		raw = "https://api.openai.com/v1"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return qualityDigest(raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if u.Scheme == "https" && strings.HasSuffix(u.Host, ":443") {
		u.Host = strings.TrimSuffix(u.Host, ":443")
	}
	if u.Scheme == "http" && strings.HasSuffix(u.Host, ":80") {
		u.Host = strings.TrimSuffix(u.Host, ":80")
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages"} {
		u.Path = strings.TrimSuffix(u.Path, suffix)
	}
	return qualityDigest(u.String())
}

func cloneQualityState(st OpenAIQualityState) OpenAIQualityState {
	copy := st
	copy.Avoid = make(map[string]OpenAIQualityAvoid, len(st.Avoid))
	for k, v := range st.Avoid {
		copy.Avoid[k] = v
	}
	return copy
}

// ApplyOpenAIQualityChange mirrors the Redis transaction for local failover.
func ApplyOpenAIQualityChange(st OpenAIQualityState, change OpenAIQualityChange) OpenAIQualityState {
	if st.Expires <= change.Now {
		st = OpenAIQualityState{}
	}
	if st.Generation != change.Generation {
		return st
	}
	st = cloneQualityState(st)
	switch change.Kind {
	case "evidence":
		st.Revision++
		st.Owner = change.AccountID
		st.Generation++
		st.Binding = 0
		st.Rotation = change.Rotation
		st.Avoid[strconv.FormatInt(change.AccountID, 10)] = OpenAIQualityAvoid{Provider: change.Provider, At: change.Now, Until: change.Until}
		st.Expires = change.Expires
	case "bind":
		if st.Generation > 0 && (st.Binding == change.PreviousBinding || st.Binding == change.AccountID) {
			st.Revision++
			st.Binding = change.AccountID
			st.Expires = change.Expires
		}
	}
	return st
}

func (s *OpenAIGatewayService) qualityLocal(scope string) *openAIQualityLocal {
	v, _ := s.openaiQualityStates.LoadOrStore(scope, &openAIQualityLocal{})
	local, _ := v.(*openAIQualityLocal)
	return local
}

func (s *OpenAIGatewayService) loadQuality(ctx context.Context, q *openAIQualityRequest) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.loaded {
		return
	}
	q.loaded = true
	if store, ok := s.cache.(OpenAIQualityRoutingStore); ok {
		keys := []string{s.openAISessionCacheKey(q.session)}
		if s.openAISessionHashReadOldFallbackEnabled() {
			if old := s.openAILegacySessionCacheKey(ctx, q.session); old != "" {
				keys = append(keys, old)
			}
		}
		st, bindings, err := store.ReadOpenAIQualityRouting(ctx, q.scope, q.group, keys)
		if err != nil {
			qualityEvent("storage_read_failed", q.scope, 0, 0)
		}
		if st != nil && st.Expires > time.Now().UnixMilli() {
			q.state = cloneQualityState(*st)
		}
		for _, id := range bindings {
			if id > 0 {
				q.sticky = id
				break
			}
		}
	}
	if v, ok := s.openaiQualityStates.Load(q.scope); ok {
		local, _ := v.(*openAIQualityLocal)
		local.mu.Lock()
		if local.state.Expires > time.Now().UnixMilli() && (local.state.Generation > q.state.Generation || local.state.Generation == q.state.Generation && local.state.Revision > q.state.Revision) {
			q.state = cloneQualityState(local.state)
		}
		local.mu.Unlock()
	}
	if q.state.Generation > 0 {
		local := s.qualityLocal(q.scope)
		local.mu.Lock()
		if q.state.Generation > local.state.Generation || q.state.Generation == local.state.Generation && q.state.Revision >= local.state.Revision {
			local.state = cloneQualityState(q.state)
		}
		local.mu.Unlock()
	}
}

func (s *OpenAIGatewayService) qualityUpdate(scope string, change OpenAIQualityChange) OpenAIQualityState {
	ttl := s.openAIWSSessionStickyTTL()
	change.Expires = change.Now + ttl.Milliseconds()
	local := s.qualityLocal(scope)
	local.mu.Lock()
	local.state = ApplyOpenAIQualityChange(local.state, change)
	state := cloneQualityState(local.state)
	local.mu.Unlock()
	// Cancellation of the client must not discard already observed evidence.
	if store, ok := s.cache.(OpenAIQualityRoutingStore); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		type syncResult struct {
			state *OpenAIQualityState
			err   error
		}
		result := make(chan syncResult, 1)
		var remote *OpenAIQualityState
		var err error
		select {
		case openAIQualitySyncSlots <- struct{}{}:
			go func() {
				defer func() { <-openAIQualitySyncSlots }()
				st, updateErr := store.UpdateOpenAIQualityRouting(ctx, scope, change, ttl)
				result <- syncResult{st, updateErr}
			}()
			select {
			case r := <-result:
				remote, err = r.state, r.err
			case <-ctx.Done():
				err = ctx.Err()
			}
		default:
			err = errors.New("quality state synchronization busy")
		}
		cancel()
		if err != nil {
			qualityEvent("storage_write_failed", scope, change.AccountID, state.Generation)
		} else if remote != nil {
			local.mu.Lock()
			if remote.Generation > local.state.Generation || remote.Generation == local.state.Generation && remote.Revision >= local.state.Revision {
				local.state = cloneQualityState(*remote)
			}
			state = cloneQualityState(local.state)
			local.mu.Unlock()
		}
	}
	if s.openaiQualityWrites.Add(1)%128 == 0 {
		now := time.Now().UnixMilli()
		s.openaiQualityStates.Range(func(key, value any) bool {
			entry, _ := value.(*openAIQualityLocal)
			entry.mu.Lock()
			expired := entry.state.Expires <= now
			entry.mu.Unlock()
			if expired {
				s.openaiQualityStates.CompareAndDelete(key, value)
			}
			return true
		})
	}
	return state
}

func (s *OpenAIGatewayService) qualityObserver(ctx context.Context, account *Account, model string) *upstreamResponseModelObserver {
	o := &upstreamResponseModelObserver{}
	q := qualityRequest(ctx)
	if q == nil || account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeAPIKey || canonicalQualityModel(model) == "" {
		return o
	}
	s.loadQuality(ctx, q)
	q.mu.Lock()
	generation := q.routeGeneration
	scope := q.scope
	q.mu.Unlock()
	var once sync.Once
	o.quality = func(observed string) {
		if !s.qualityDowngrade(model, observed) {
			return
		}
		once.Do(func() {
			qualityEvent("downgrade_evidence", scope, account.ID, generation, "outbound_model", model, "response_model", observed)
			if s.qualityConfig().EffectiveMode() != "enforce" {
				return
			}
			now := time.Now().UnixMilli()
			st := s.qualityUpdate(scope, OpenAIQualityChange{Kind: "evidence", Generation: generation,
				AccountID: account.ID, Provider: qualityProvider(account), Now: now,
				Until:    now + int64(s.qualityConfig().EffectiveAvoidSeconds())*1000,
				Rotation: strings.ReplaceAll(uuid.NewString(), "-", "")})
			if st.Generation > generation {
				qualityEvent("sticky_invalidated", scope, account.ID, st.Generation)
			}
		})
	}
	return o
}

// inspectQualityDeclaration is only called after a candidate model is found.
// Reject duplicate or conflicting declarations without adding work to deltas.
func (o *upstreamResponseModelObserver) inspectQualityDeclaration(payload []byte, paths ...string) {
	if o == nil || o.quality == nil {
		return
	}
	model := ""
	for _, path := range paths {
		v := gjson.GetBytes(payload, path)
		if !v.Exists() {
			continue
		}
		if v.Type != gjson.String || strings.TrimSpace(v.String()) == "" {
			return
		}
		if model != "" && model != strings.TrimSpace(v.String()) {
			return
		}
		model = strings.TrimSpace(v.String())
	}
	if model == "" || !gjson.ValidBytes(payload) || qualityDuplicateModelKey(payload) {
		return
	}
	o.quality(model)
}

func qualityDuplicateModelKey(payload []byte) bool {
	// gjson iteration preserves duplicate object members; inspect only the two
	// protocol envelope objects, never user text or function arguments.
	duplicate := false
	for _, obj := range []gjson.Result{gjson.ParseBytes(payload), gjson.GetBytes(payload, "response"), gjson.GetBytes(payload, "message")} {
		seen := make(map[string]bool)
		obj.ForEach(func(k, _ gjson.Result) bool {
			if k.String() == "model" || k.String() == "response" || k.String() == "message" {
				if seen[k.String()] {
					duplicate = true
				}
				seen[k.String()] = true
			}
			return !duplicate
		})
	}
	return duplicate
}

func (s *OpenAIGatewayService) qualityRotation(ctx context.Context, account *Account) string {
	q := qualityRequest(ctx)
	if q == nil || account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeAPIKey || s.qualityConfig().EffectiveMode() != "enforce" {
		return ""
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pinned {
		return ""
	}
	for _, bad := range q.state.Avoid {
		if bad.Provider == qualityProvider(account) {
			return q.state.Rotation
		}
	}
	return ""
}

func (s *OpenAIGatewayService) qualityConnectionScope(ctx context.Context, account *Account, session string) string {
	q := qualityRequest(ctx)
	if q == nil || account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeAPIKey || s.qualityConfig().EffectiveMode() != "enforce" {
		return session
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pinned || q.state.Generation == 0 {
		return session
	}
	return qualityDigest(session + ":quality:" + q.scope + ":" + q.state.Rotation)
}

func qualityRotateBody(body []byte, rotation string, responses bool) []byte {
	if rotation == "" {
		return body
	}
	for _, path := range []string{"session_id", "conversation_id", "client_metadata.session_id", "client_metadata.conversation_id", "prompt_cache_key"} {
		if gjson.GetBytes(body, path).Exists() || responses && path == "prompt_cache_key" {
			if changed, err := sjson.SetBytes(body, path, rotation); err == nil {
				body = changed
			}
		}
	}
	return body
}

func qualityRotateHeaders(headers http.Header, rotation string) {
	if rotation == "" {
		return
	}
	for _, name := range []string{"session_id", "conversation_id", "session-id", "x-session-id", "x-session-affinity", "x-opencode-session", "x-conversation-id"} {
		if headers.Get(name) != "" {
			headers.Set(name, rotation)
		}
	}
	headers.Del("x-codex-turn-state")
}

func (s *OpenAIGatewayService) prepareQualityHTTP(ctx context.Context, c *gin.Context, account *Account, req *http.Request, body []byte) {
	if qualityRequest(ctx) == nil || req.GetBody == nil {
		return
	}
	o := s.qualityObserver(ctx, account, gjson.GetBytes(body, "model").String())
	if c != nil {
		c.Set(upstreamResponseModelObserverContextKey, o)
	}
	rotation := s.qualityRotation(ctx, account)
	if rotation == "" {
		return
	}
	reader, err := req.GetBody()
	if err != nil {
		return
	}
	body, err = io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		return
	}
	body = qualityRotateBody(body, rotation, strings.Contains(req.URL.Path, "/responses"))
	qualityRotateHeaders(req.Header, rotation)
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	q := qualityRequest(ctx)
	q.mu.Lock()
	scope, generation := q.scope, q.state.Generation
	q.mu.Unlock()
	qualityEvent("identity_rotated", scope, account.ID, generation)
}

// qualityExclusionTiers reuses the ordinary scheduler for every pass, including
// all permission, model, quota, concurrency and profit admission checks.
func qualityExclusionTiers(accounts []Account, st OpenAIQualityState, excluded map[int64]struct{}) []map[int64]struct{} {
	now := time.Now().UnixMilli()
	providers := make(map[string]bool)
	for _, bad := range st.Avoid {
		if bad.Until > now {
			providers[bad.Provider] = true
		}
	}
	base := func() map[int64]struct{} {
		m := make(map[int64]struct{}, len(excluded))
		for id := range excluded {
			m[id] = struct{}{}
		}
		return m
	}
	otherProvider, otherAccount := base(), base()
	type badCandidate struct{ id, at int64 }
	var bads []badCandidate
	for i := range accounts {
		a := &accounts[i]
		if providers[qualityProvider(a)] {
			otherProvider[a.ID] = struct{}{}
		}
		if bad, ok := st.Avoid[strconv.FormatInt(a.ID, 10)]; ok && bad.Until > now {
			otherAccount[a.ID] = struct{}{}
			bads = append(bads, badCandidate{a.ID, bad.At})
		}
	}
	tiers := []map[int64]struct{}{otherProvider, otherAccount}
	sort.Slice(bads, func(i, j int) bool {
		if bads[i].at == bads[j].at {
			return bads[i].id < bads[j].id
		}
		return bads[i].at < bads[j].at
	})
	for _, bad := range bads {
		pass := base()
		for _, other := range bads {
			if other.id != bad.id {
				pass[other.id] = struct{}{}
			}
		}
		tiers = append(tiers, pass)
	}
	return tiers
}

func (s *OpenAIGatewayService) qualitySticky(ctx context.Context, group *int64, session string) (int64, bool) {
	q := qualityRequest(ctx)
	if q == nil || q.group != derefGroupID(group) || q.session != session {
		return 0, false
	}
	s.loadQuality(ctx, q)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.state.Generation > 0 && !q.pinned && s.qualityConfig().EffectiveMode() == "enforce" {
		return q.state.Binding, true
	}
	_, batched := s.cache.(OpenAIQualityRoutingStore)
	return q.sticky, batched
}

func (s *OpenAIGatewayService) qualityOwnsSticky(ctx context.Context, group *int64, session string) bool {
	q := qualityRequest(ctx)
	if q == nil || q.group != derefGroupID(group) || q.session != session || s.qualityConfig().EffectiveMode() != "enforce" {
		return false
	}
	q.mu.Lock()
	active := q.state.Generation > 0
	scope := q.scope
	q.mu.Unlock()
	if active {
		return true
	}
	if v, ok := s.openaiQualityStates.Load(scope); ok {
		entry, _ := v.(*openAIQualityLocal)
		entry.mu.Lock()
		defer entry.mu.Unlock()
		return entry.state.Generation > 0 && entry.state.Expires > time.Now().UnixMilli()
	}
	return false
}

func (s *OpenAIGatewayService) selectAccountWithQualityRouting(
	ctx context.Context, group *int64, previous, session, model string, excluded map[int64]struct{},
	transport OpenAIUpstreamTransport, capability OpenAIEndpointCapability, image OpenAIImagesCapability,
	compact bool, platform string, canMove, useCost bool,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	selectPass := func(pass map[int64]struct{}) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
		return s.selectAccountWithoutQualityRouting(ctx, group, previous, session, model, pass, transport, capability, image, compact, platform, canMove, useCost)
	}
	q := qualityRequest(ctx)
	if q == nil || NormalizeOpenAICompatiblePlatform(platform) != PlatformOpenAI || image != "" {
		return selectPass(excluded)
	}
	s.loadQuality(ctx, q)
	q.mu.Lock()
	state := cloneQualityState(q.state)
	pinned := q.pinned
	scope := q.scope
	q.mu.Unlock()
	if state.Generation == 0 || s.qualityConfig().EffectiveMode() != "enforce" {
		return selectPass(excluded)
	}
	if pinned {
		canMove = false
		qualityEvent("continuation_pinned", scope, state.Binding, state.Generation)
		if previous != "" {
			selection, err := s.selectAccountByPreviousResponseIDForCapability(s.withOpenAIProfitControlGate(ctx, group), group, previous, model, excluded, capability, compact)
			if err != nil {
				return nil, OpenAIAccountScheduleDecision{}, err
			}
			if selection != nil && selection.Account != nil && s.isOpenAIAccountTransportCompatible(selection.Account, transport) {
				return selection, OpenAIAccountScheduleDecision{Layer: openAIAccountScheduleLayerPreviousResponse, StickyPreviousHit: true, SelectedAccountID: selection.Account.ID}, nil
			}
			if selection != nil && selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			return nil, OpenAIAccountScheduleDecision{}, ErrNoAvailableAccounts
		}
		owner := state.Owner
		if state.Binding > 0 {
			owner = state.Binding
		}
		accounts, err := s.listSchedulableAccounts(ctx, group, platform)
		if err != nil {
			return nil, OpenAIAccountScheduleDecision{}, err
		}
		pass := cloneExcludedAccountIDs(excluded)
		if pass == nil {
			pass = make(map[int64]struct{})
		}
		for _, a := range accounts {
			if a.ID != owner {
				pass[a.ID] = struct{}{}
			}
		}
		return selectPass(pass)
	}
	accounts, err := s.listSchedulableAccounts(ctx, group, platform)
	if err != nil {
		return nil, OpenAIAccountScheduleDecision{}, err
	}
	// A replacement stays sticky even when an old provider's cooldown expires.
	tiers := qualityExclusionTiers(accounts, state, excluded)
	if state.Binding > 0 {
		if _, skip := excluded[state.Binding]; !skip {
			if bad, exists := state.Avoid[strconv.FormatInt(state.Binding, 10)]; !exists || bad.Until <= time.Now().UnixMilli() {
				bound := make(map[int64]struct{}, len(accounts)+len(excluded))
				for id := range excluded {
					bound[id] = struct{}{}
				}
				for _, a := range accounts {
					if a.ID != state.Binding {
						bound[a.ID] = struct{}{}
					}
				}
				tiers = append([]map[int64]struct{}{bound}, tiers...)
			}
		}
	}
	for _, pass := range tiers {
		selection, decision, selectErr := selectPass(pass)
		if selectErr == nil && selection != nil && selection.Account != nil {
			id := selection.Account.ID
			st := s.qualityUpdate(scope, OpenAIQualityChange{Kind: "bind", Generation: state.Generation, PreviousBinding: state.Binding, AccountID: id, Now: time.Now().UnixMilli()})
			q.mu.Lock()
			q.state = st
			q.routeGeneration = state.Generation
			q.mu.Unlock()
			if id != state.Binding {
				qualityEvent("route_changed", scope, id, state.Generation)
			}
			return selection, decision, nil
		}
		if selectErr != nil && !errors.Is(selectErr, ErrNoAvailableAccounts) && !errors.Is(selectErr, ErrNoAvailableCompactAccounts) {
			return selection, decision, selectErr
		}
		err = selectErr
	}
	if err == nil {
		err = ErrNoAvailableAccounts
	}
	return nil, OpenAIAccountScheduleDecision{}, err
}
