package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

type controlledSchedulingContextKey struct{}

// ControlledRequest is shared by all conversion, OAuth and failover paths for
// one logical request. Credentials and request content are never retained.
type ControlledRequest struct {
	mu                 sync.Mutex
	ID                 string
	Started            time.Time
	ClientDeadline     time.Time
	clientContext      context.Context
	Model              string
	Protocol           string
	Reasoning          string
	Stream             bool
	ContextTokens      int64 // -1 means unknown; never infer tokens from byte length.
	SessionID          string
	ReplaySafe         bool
	policyLoaded       bool
	Policy             scheduling.Policy
	Profile            scheduling.LatencyProfile
	Ledger             *scheduling.AttemptLedger
	Decision           scheduling.Decision
	decisionPending    bool
	fallback           bool
	owner              bool
	ownerAccountID     int64
	control            *ControlledSchedulingService
	history            []SchedulingAttemptTrace
	semanticAt         time.Time
	answerAt           time.Time
	finish             func()
	currentAttemptID   string
	gateRejections     int
	lastBackoffAttempt int
}

type SchedulingAttemptTrace struct {
	AccountID              int64     `json:"account_id"`
	AttemptID              string    `json:"attempt_id"`
	Priority               int       `json:"priority"`
	Reason                 string    `json:"reason"`
	Started                time.Time `json:"started_at"`
	Outcome                string    `json:"outcome"`
	FirstEventMS           *int64    `json:"first_event_ms,omitempty"`
	FirstSemanticMS        *int64    `json:"first_semantic_ms,omitempty"`
	FirstAnswerMS          *int64    `json:"first_answer_ms,omitempty"`
	OverallFirstSemanticMS *int64    `json:"overall_first_semantic_ms,omitempty"`
	RemainingBudgetMS      *int64    `json:"remaining_budget_ms,omitempty"`
	StopReason             string    `json:"stop_reason"`
	PolicyVersion          int64     `json:"policy_version"`
	MetricVersion          string    `json:"metric_version"`
}

func NewControlledRequestContext(ctx context.Context, protocol string) context.Context {
	deadline, _ := ctx.Deadline()
	r := &ControlledRequest{ID: uuid.NewString(), Started: time.Now(), ClientDeadline: deadline, clientContext: ctx, Protocol: scheduling.CanonicalTransport(protocol), Reasoning: "unknown", ContextTokens: -1, ReplaySafe: true}
	return context.WithValue(ctx, controlledSchedulingContextKey{}, r)
}
func controlledRequest(ctx context.Context) *ControlledRequest {
	r, _ := ctx.Value(controlledSchedulingContextKey{}).(*ControlledRequest)
	return r
}

// Freeze WS metadata before selecting the first account. Waiting for a client
// response.create on an idle socket is not part of that turn's first-output D.
func CaptureControlledWSFirstRequest(ctx context.Context, body []byte, received time.Time) {
	r := controlledRequest(ctx)
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.policyLoaded {
		r.mu.Unlock()
		return
	}
	r.Protocol = "ws"
	r.Started = received
	r.mu.Unlock()
	r.metadata(body)
}

// Capture bounded metadata while the existing handler reads the body. This does
// not pre-read, rewind, expand the body limit, or retain sensitive content.
type schedulingMetadataBody struct {
	io.ReadCloser
	request   *ControlledRequest
	buf       bytes.Buffer
	truncated bool
}

func (b *schedulingMetadataBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if !b.truncated && n > 0 {
		if b.buf.Len()+n <= 256*1024 {
			_, _ = b.buf.Write(p[:n])
		} else {
			b.truncated = true
			b.request.mu.Lock()
			b.request.ReplaySafe = false
			b.request.mu.Unlock()
			b.buf.Reset()
		}
	}
	if err == io.EOF && !b.truncated {
		b.request.metadata(b.buf.Bytes())
		b.buf.Reset()
	}
	return n, err
}

// CaptureControlledRequestMetadata reuses the handler's validated body, model
// and stream (including Gemini URL/action). No context token count is inferred.
func CaptureControlledRequestMetadata(ctx context.Context, body []byte, model string, stream bool) {
	r := controlledRequest(ctx)
	if r == nil {
		return
	}
	r.metadata(body)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.policyLoaded {
		r.Model = model
		r.Stream = stream
	}
}
func (r *ControlledRequest) metadata(body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.policyLoaded {
		return
	}
	fields, replaySafe, valid := readSchedulingMetadata(bytes.NewReader(body), r.Protocol, "root")
	if !valid {
		r.Reasoning = "unknown"
		r.ReplaySafe = false
		return
	}
	r.ReplaySafe = replaySafe
	if raw, present := fields["model"]; present && json.Unmarshal(raw, &r.Model) != nil {
		r.ReplaySafe = false
	}
	if raw, present := fields["stream"]; present && json.Unmarshal(raw, &r.Stream) != nil {
		r.ReplaySafe = false
	}
	r.Reasoning = controlledReasoningLabel(r.Protocol, fields)
}

// Metadata labels describe explicit client choices, not inferred model defaults.
// In particular, token budgets never imply low/medium/high reasoning effort.
func controlledReasoningLabel(protocol string, fields map[string]json.RawMessage) string {
	var level, budget json.RawMessage
	configured := false
	switch protocol {
	case "gemini":
		raw, present := controlledMetadataAlias(fields, "generationConfig", "generation_config")
		if !present {
			return "default"
		}
		config, valid := controlledMetadataObject(raw)
		if !valid {
			return "unknown"
		}
		raw, present = controlledMetadataAlias(config, "thinkingConfig", "thinking_config")
		if !present {
			return "default"
		}
		thinking, valid := controlledMetadataObject(raw)
		if !valid {
			return "unknown"
		}
		configured = true
		level, _ = controlledMetadataAlias(thinking, "thinkingLevel", "thinking_level")
		budget, _ = controlledMetadataAlias(thinking, "thinkingBudget", "thinking_budget")
	case "messages":
		if raw, present := fields["thinking"]; present {
			configured = true
			thinking, valid := controlledMetadataObject(raw)
			if !valid {
				return "unknown"
			}
			var kind string
			if json.Unmarshal(thinking["type"], &kind) != nil {
				return "unknown"
			}
			switch strings.ToLower(strings.TrimSpace(kind)) {
			case "disabled":
				return "none"
			case "enabled", "adaptive":
				budget = thinking["budget_tokens"]
			default:
				return "unknown"
			}
		}
		if raw, present := fields["output_config"]; present {
			output, valid := controlledMetadataObject(raw)
			if !valid {
				return "unknown"
			}
			level = output["effort"]
			configured = configured || level != nil
		}
	default:
		if raw, present := fields["reasoning"]; present {
			reasoning, valid := controlledMetadataObject(raw)
			if !valid {
				return "unknown"
			}
			level = reasoning["effort"]
		}
		if level == nil {
			level = fields["reasoning_effort"]
		}
		configured = level != nil
	}
	label := ""
	if level != nil {
		var effort string
		if json.Unmarshal(level, &effort) != nil {
			return "unknown"
		}
		effort = strings.ToLower(strings.TrimSpace(effort))
		valid := false
		switch protocol {
		case "gemini":
			valid = effort == "minimal" || effort == "low" || effort == "medium" || effort == "high"
		case "messages":
			valid = effort == "low" || effort == "medium" || effort == "high" || effort == "xhigh" || effort == "max"
		default:
			valid = effort == "none" || effort == "minimal" || effort == "low" || effort == "medium" || effort == "high" || effort == "xhigh"
		}
		if !valid {
			return "unknown"
		}
		label = effort
	}
	if budget != nil {
		value, err := strconv.ParseInt(strings.TrimSpace(string(budget)), 10, 64)
		if err != nil {
			return "unknown"
		}
		if label != "" {
			label += "|"
		}
		label += "budget:" + strconv.FormatInt(value, 10)
	}
	if label != "" {
		return label
	}
	if configured {
		return "unknown"
	}
	return "default"
}

func controlledMetadataObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	err := json.Unmarshal(raw, &object)
	return object, err == nil && object != nil
}

func controlledMetadataAlias(fields map[string]json.RawMessage, camel, snake string) (json.RawMessage, bool) {
	if raw, present := fields[camel]; present {
		return raw, true
	}
	raw, present := fields[snake]
	return raw, present
}

func (r *ControlledRequest) markSemantic(at time.Time, answer bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.semanticAt.IsZero() {
		r.semanticAt = at
	}
	if answer && r.answerAt.IsZero() {
		r.answerAt = at
	}
	if r.Ledger != nil {
		r.Ledger.MarkSemanticCommit()
	}
}
func (r *ControlledRequest) Close() {
	r.mu.Lock()
	fn := r.finish
	r.finish = nil
	ctrl := r.control
	d := r.Decision
	pending := r.decisionPending
	r.decisionPending = false
	r.mu.Unlock()
	if fn != nil {
		fn()
	}
	if pending && ctrl != nil {
		parent := r.clientContext
		if parent == nil {
			parent = context.Background()
		}
		cleanup, stop := controlledPreparationContext(parent, r)
		ctrl.releaseDecisionContext(cleanup, d)
		stop()
	}
	r.mu.Lock()
	semantic, attempt := r.semanticAt, r.currentAttemptID
	r.mu.Unlock()
	if ctrl != nil && attempt != "" && !semantic.IsZero() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = ctrl.Store.RecordAttemptMetrics(ctx, attempt, map[string]any{"overall_first_semantic_ms": semantic.Sub(r.Started).Milliseconds()})
	}
}

// Install one ledger before body parsing and queues. WS adapters start a new
// request context for each response.create, rather than sharing a connection one.
func ControlledSchedulingMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		protocol := "http"
		if strings.Contains(c.Request.URL.Path, "messages") {
			protocol = "messages"
		} else if strings.Contains(c.Request.URL.Path, "chat/completions") {
			protocol = "chat"
		} else if strings.Contains(c.Request.URL.Path, "responses") {
			protocol = "responses"
		} else if strings.Contains(c.Request.URL.Path, "generateContent") || strings.Contains(c.Request.URL.Path, "streamGenerateContent") {
			protocol = "gemini"
		}
		ctx := NewControlledRequestContext(c.Request.Context(), protocol)
		r := controlledRequest(ctx)
		c.Header("X-Scheduling-Request-Id", r.ID)
		c.Request = c.Request.WithContext(ctx)
		if c.Request.Body != nil {
			c.Request.Body = &schedulingMetadataBody{ReadCloser: c.Request.Body, request: r}
		}
		c.Writer = &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
		defer r.Close()
		c.Next()
	}
}

type schedulingResponseWriter struct {
	gin.ResponseWriter
	request *ControlledRequest
	parser  semanticEventParser
}

func (w *schedulingResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if n > 0 && w.Status() < 400 {
		if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
			w.parser.Feed(p[:n], func(semantic, answer, terminal bool) {
				if semantic {
					w.request.markSemantic(time.Now(), answer)
				}
			})
		} else {
			w.request.markSemantic(time.Now(), false)
		}
	}
	return n, err
}
func (w *schedulingResponseWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

var _ http.ResponseWriter = (*schedulingResponseWriter)(nil)
