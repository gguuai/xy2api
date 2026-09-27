package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/tidwall/gjson"
)

type controlledDispatchContextKey struct{}
type controlledDispatch struct {
	service            *ControlledSchedulingService
	request            *ControlledRequest
	ticket             scheduling.DispatchTicket
	budget             scheduling.BudgetReservation
	decision           scheduling.Decision
	ctx                context.Context
	cancel             context.CancelFunc
	mu                 sync.Mutex
	once               sync.Once
	sent               bool
	semantic           time.Time
	answer             time.Time
	firstEvent         time.Time
	started            time.Time
	attemptDeadline    time.Time
	terminal           bool
	upstreamFailure    bool
	timeout            bool
	clipped            bool
	status             int
	timer              *time.Timer
	done               chan struct{}
	semanticReady      chan struct{}
	adminCancelled     atomic.Bool
	excluded           bool
	toolPending        bool
	retryAfter         time.Time
	parser             semanticEventParser
	semanticObservable bool
	viableFallback     bool
	commitToolPending  bool
}

func (s *ControlledSchedulingService) beginDispatch(ctx context.Context, accountID int64, concurrency int) (*controlledDispatch, error) {
	if s == nil || controlledRequest(ctx) == nil {
		return nil, nil
	}
	r := controlledRequest(ctx)
	r.mu.Lock()
	p := r.Policy
	ledger := r.Ledger
	decision := r.Decision
	safe := r.ReplaySafe
	sessionID := r.SessionID
	owner := r.owner
	ownerAccountID := r.ownerAccountID
	r.mu.Unlock()
	if r.clientContext != nil && r.clientContext.Err() != nil {
		return nil, r.clientContext.Err()
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if accountID <= 0 {
		return nil, scheduling.ErrInvalidControl
	}
	// Strong continuation ownership survives allocation-policy rollback and
	// adapter rewrites. Manual control cannot be bypassed by selecting a peer.
	if owner && ownerAccountID > 0 && accountID != ownerAccountID {
		return nil, scheduling.ErrControlBlocked
	}
	if p.Enabled && decision.AccountID != 0 && decision.AccountID != accountID {
		return nil, fmt.Errorf("scheduler_account_mismatch")
	}
	if p.Enabled && decision.AccountID == 0 {
		a, e := s.accounts.GetByID(ctx, accountID)
		if e != nil {
			return nil, e
		}
		if a == nil {
			return nil, scheduling.ErrNoCandidate
		}
		decision = scheduling.Decision{AccountID: accountID, Priority: a.Priority, PolicyVersion: p.Version, Reason: "protocol_owner"}
		for _, rule := range p.Accounts {
			if rule.AccountID == accountID {
				if rule.Weight == 0 {
					return nil, scheduling.ErrNoCandidate
				}
				if rule.Priority != nil {
					decision.Priority = *rule.Priority
				}
			}
		}
	}
	var br scheduling.BudgetReservation
	if p.Enabled {
		if ledger == nil {
			return nil, scheduling.ErrAttemptBudget
		}
		if e := ledger.CanAttempt(accountID, decision.Priority, time.Now(), safe); e != nil {
			return nil, e
		}
		retry := ledger.Snapshot().Attempts > 0
		if retry || (!owner && p.Mode != scheduling.ModePin) {
			var e error
			br, e = s.Runtime.AcquireDispatchBudget(ctx, p, r.ID, retry)
			if e != nil {
				return nil, e
			}
		}
	}
	// Unknown remote work still occupies the single probe slot.
	if decision.Probe && (concurrency <= 0 || concurrency > 1) {
		concurrency = 1
	}
	ticket, e := s.Store.BeginDispatch(ctx, scheduling.DispatchRequest{RequestID: r.ID, AccountID: accountID, SessionID: sessionID, NodeID: s.node, LeaseDuration: 2 * time.Minute, HardConcurrency: concurrency})
	if e != nil {
		if br.ID != "" {
			_ = s.Runtime.RefundDispatchBudget(context.WithoutCancel(ctx), br)
		}
		if p.Enabled && !owner && p.Mode != scheduling.ModePin && (errors.Is(e, scheduling.ErrControlBlocked) || errors.Is(e, scheduling.ErrCapacity)) {
			s.releaseDecision(decision)
			r.mu.Lock()
			r.decisionPending = false
			r.gateRejections++
			rejections := r.gateRejections
			r.mu.Unlock()
			if rejections >= 128 {
				return nil, scheduling.ErrAttemptBudget
			}
			return nil, &UpstreamFailoverError{StatusCode: 503, PreDispatchSelectionInvalidated: true, ClientMessage: "candidate lost dispatch admission"}
		}
		if !errors.Is(e, scheduling.ErrControlBlocked) && !errors.Is(e, scheduling.ErrCapacity) && !errors.Is(e, scheduling.ErrAttemptIdentity) {
			return nil, fmt.Errorf("%w: %v", scheduling.ErrSharedState, e)
		}
		return nil, e
	}
	live, cancel := context.WithCancel(ctx)
	d := &controlledDispatch{service: s, request: r, ticket: ticket, budget: br, decision: decision, cancel: cancel, done: make(chan struct{}), semanticReady: make(chan struct{}), semanticObservable: true}
	d.ctx = context.WithValue(live, controlledDispatchContextKey{}, d)
	s.active.Store(ticket.TicketID, d)
	r.mu.Lock()
	r.finish = func() {
		d.Finish("handler_returned", false, fmt.Errorf("handler returned without observed upstream terminal"))
	}
	r.mu.Unlock()
	go d.maintainLease()
	return d, nil
}
func (d *controlledDispatch) Context() context.Context {
	if d == nil {
		return context.Background()
	}
	return d.ctx
}
func (d *controlledDispatch) MarkSent() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sent {
		return fmt.Errorf("dispatch already sent")
	}
	if err := d.ctx.Err(); err != nil {
		return err
	}
	r := d.request
	r.mu.Lock()
	p, ledger, safe, owner := r.Policy, r.Ledger, r.ReplaySafe, r.owner
	r.mu.Unlock()
	attempt := 1
	if p.Enabled {
		if ledger == nil {
			return scheduling.ErrAttemptBudget
		}
		if err := ledger.CanAttempt(d.ticket.AccountID, d.decision.Priority, time.Now(), safe); err != nil {
			return err
		}
		attempt = ledger.Snapshot().Attempts + 1
	}
	kind := "ordinary_first"
	switch {
	case attempt > 1:
		kind = "retry"
	case owner:
		kind = "owner"
	case p.Mode == scheduling.ModePin:
		kind = "pin"
	case d.decision.Probe:
		kind = "probe"
	case d.decision.Reason == "degraded_best_effort":
		kind = "fallback"
	}
	// Complete all fallible preparation before charging the actual-attempt ledger.
	// A later Redis/cancellation failure is settled as not_sent and compensated;
	// the statistics query excludes those aborted dispatch tickets.
	if err := d.service.Store.RecordAttemptMetrics(d.ctx, d.ticket.TicketID, map[string]any{"sent_at": time.Now().UTC().Format(time.RFC3339Nano), "group_id": p.GroupID, "model": p.Model, "policy_version": p.Version, "dispatch_kind": kind, "attempt_number": attempt, "priority": d.decision.Priority, "reason": d.decision.Reason, "metric_version": SchedulingMetricVersion}); err != nil {
		return fmt.Errorf("%w: dispatch record: %v", scheduling.ErrSharedState, err)
	}
	if p.Enabled {
		if d.budget.ID != "" {
			if err := d.service.Runtime.CommitDispatchBudget(d.ctx, d.budget); err != nil {
				return err
			}
		}
		if d.decision.ReservationID != "" {
			if err := d.service.Runtime.CommitSelection(d.ctx, d.decision); err != nil {
				return err
			}
		}
		if err := d.ctx.Err(); err != nil {
			return err
		}
		if err := ledger.BeginAttempt(d.ticket.AccountID, d.decision.Priority, time.Now(), safe); err != nil {
			return err
		}
	}
	d.sent = true
	d.started = time.Now()
	r.mu.Lock()
	r.decisionPending = false
	r.currentAttemptID = d.ticket.TicketID
	fallback := r.fallback
	r.mu.Unlock()
	if p.Enabled {
		if safe && fallback {
			if allowed, e := d.service.Runtime.CanRetry(d.ctx, p); e == nil && allowed {
				d.viableFallback = true
			}
		}
		d.startFirstOutputTimerLocked()
	}
	return nil
}

// Called with d.mu held. Non-streaming completion is bounded by the overall
// budget, but can never become a fabricated first-semantic-output observation.
func (d *controlledDispatch) startFirstOutputTimerLocked() {
	if d.timer != nil {
		d.timer.Stop()
	}
	if !d.semantic.IsZero() {
		return
	}
	r := d.request
	r.mu.Lock()
	p, profile, ledger := r.Policy, r.Profile, r.Ledger
	r.mu.Unlock()
	if !p.Enabled || ledger == nil {
		return
	}
	now := time.Now()
	window := ledger.Remaining(now)
	if d.semanticObservable {
		window = ledger.AttemptWindow(now, d.viableFallback)
	}
	if window <= 0 && ledger.Snapshot().Deadline.IsZero() {
		return
	}
	deadline := now.Add(window)
	configured := time.Duration(profile.AttemptTimeoutMS) * time.Millisecond
	if d.semanticObservable && configured > 0 {
		fullAttemptDeadline := d.started.Add(configured)
		if fullAttemptDeadline.Before(deadline) {
			deadline = fullAttemptDeadline
		}
	}
	// Headers can identify a streaming protocol, but cannot give this attempt
	// more time or take back a fallback window reserved at dispatch.
	if !d.attemptDeadline.IsZero() && d.attemptDeadline.Before(deadline) {
		deadline = d.attemptDeadline
	}
	d.attemptDeadline = deadline
	d.clipped = !d.semanticObservable || configured <= 0 || deadline.Before(d.started.Add(configured))
	window = time.Until(deadline)
	if window <= 0 {
		window = time.Nanosecond
	}
	d.timer = time.AfterFunc(window, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.semantic.IsZero() {
			d.timeout = true
			if d.semanticObservable {
				ledger.MarkFirstOutputTimeout()
			}
			d.cancel()
		}
	})
}
func (d *controlledDispatch) noteEvent(semantic, answer, terminal bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if d.firstEvent.IsZero() {
		d.firstEvent = now
	}
	if semantic && d.semantic.IsZero() {
		d.semantic = now
		if d.timer != nil {
			d.timer.Stop()
		}
		close(d.semanticReady)
	}
	if answer && d.answer.IsZero() {
		d.answer = now
	}
	if terminal {
		d.terminal = true
	}
}
func (d *controlledDispatch) ObserveFrame(frame []byte) {
	if d == nil {
		return
	}
	semantic, answer, terminal, tool := classifySemanticEvent(frame)
	v := gjson.ParseBytes(frame)
	kind := v.Get("type").String()
	d.mu.Lock()
	if tool {
		d.toolPending = true
	}
	if terminal && d.toolPending {
		semantic = true
		d.toolPending = false
	}
	if kind == "content_block_stop" {
		if d.toolPending {
			semantic = true
			d.toolPending = false
		}
		terminal = false
	}
	if kind == "error" || kind == "response.failed" || kind == "response.incomplete" || kind == "response.cancelled" {
		terminal = true
		code := v.Get("error.code").String()
		if code == "" {
			code = v.Get("response.error.code").String()
		}
		switch code {
		case "invalid_request_error", "invalid_request", "context_length_exceeded", "content_policy_violation":
			d.excluded = true
			d.request.mu.Lock()
			d.request.ReplaySafe = false
			d.request.mu.Unlock()
		default:
			if kind == "response.incomplete" || kind == "response.cancelled" {
				d.excluded = true
			} else {
				d.upstreamFailure = true
			}
		}
	}
	d.mu.Unlock()
	d.noteEvent(semantic, answer, terminal)
}

func (d *controlledDispatch) CommitOutput(frame []byte) {
	if d == nil {
		return
	}
	semantic, answer, terminal, tool := classifySemanticEvent(frame)
	d.mu.Lock()
	if tool {
		d.commitToolPending = true
	}
	if d.commitToolPending && (terminal || gjson.GetBytes(frame, "type").String() == "content_block_stop") {
		semantic = true
		d.commitToolPending = false
	}
	d.mu.Unlock()
	if semantic {
		d.request.markSemantic(time.Now(), answer)
	}
}

func (d *controlledDispatch) maintainLease() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ticks := 0
	for {
		select {
		case <-d.done:
			return
		case <-ticker.C:
			ticks++
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if ticks%30 == 0 {
				if err := d.service.Store.RenewAttempt(ctx, d.ticket.TicketID, 2*time.Minute); err != nil {
					slog.Warn("scheduling lease renewal uncertain", "attempt_id", d.ticket.TicketID, "error", err)
				}
			}
			requested, e := d.service.Store.AttemptCancellationRequested(ctx, d.ticket.TicketID)
			if e == nil && requested {
				d.adminCancelled.Store(true)
				d.cancel()
			}
			cancel()
		}
	}
}
func (d *controlledDispatch) Finish(outcome string, remoteTerminal bool, err error) {
	if d == nil {
		return
	}
	d.once.Do(func() {
		d.mu.Lock()
		if d.timer != nil {
			d.timer.Stop()
		}
		sent, semantic, answer, firstEvent, started := d.sent, d.semantic, d.answer, d.firstEvent, d.started
		timeout, clipped, upstreamFailure, explicitExcluded, terminal, retryAfter, status := d.timeout, d.clipped, d.upstreamFailure, d.excluded, d.terminal, d.retryAfter, d.status
		d.mu.Unlock()
		close(d.done)
		d.cancel()
		d.service.active.Delete(d.ticket.TicketID)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r := d.request
		r.mu.Lock()
		p, profile, reasoning, transport, bucket, ledger := r.Policy, r.Profile, r.Reasoning, r.Protocol, controlledBucket(r), r.Ledger
		overallSemantic := r.semanticAt
		r.mu.Unlock()
		excluded := explicitExcluded || d.adminCancelled.Load() || (r.clientContext != nil && r.clientContext.Err() != nil) || (errors.Is(err, context.Canceled) && !timeout) || (timeout && clipped)
		if timeout {
			outcome = "first_output_timeout"
			if clipped {
				outcome = "first_output_budget_exhausted"
			}
		}
		if d.adminCancelled.Load() {
			outcome = "administrator_cancelled"
		}
		if r.clientContext != nil && r.clientContext.Err() != nil {
			outcome = "client_cancelled"
		}
		if !sent {
			d.service.releaseDecision(d.decision)
			if d.budget.ID != "" {
				_ = d.service.Runtime.RefundDispatchBudget(ctx, d.budget)
			}
			remoteTerminal = true
			outcome = "not_sent"
		}
		knownTerminal := remoteTerminal || terminal
		completed := sent && knownTerminal && !upstreamFailure && !excluded && err == nil && !timeout
		if upstreamFailure && outcome == "completed" {
			outcome = "upstream_error"
		}
		if knownTerminal {
			certainty := "remote_terminal"
			if !sent || outcome == "not_sent" {
				certainty = "proven_not_sent"
			}
			if e := d.service.Store.RecordTerminalIntent(ctx, d.ticket.TicketID, outcome, certainty, sent && status < 400); e != nil {
				slog.Warn("scheduling terminal intent pending", "attempt_id", d.ticket.TicketID, "error", e)
			}
			if e := d.service.Store.SettleAttempt(ctx, d.ticket.TicketID, outcome, sent && status < 400); e != nil {
				slog.Warn("scheduling settlement pending", "attempt_id", d.ticket.TicketID, "error", e)
			}
		} else {
			if e := d.service.Store.MarkAttemptUnknown(ctx, d.ticket.TicketID); e != nil {
				slog.Warn("scheduling unknown attempt persistence failed", "attempt_id", d.ticket.TicketID, "error", e)
			}
		}
		if sent && p.Enabled {
			if e := d.service.Runtime.ForgetDispatchReceipts(ctx, d.decision, d.budget); e != nil {
				slog.Warn("scheduling receipt cleanup pending", "attempt_id", d.ticket.TicketID, "error", e)
			}
			o := scheduling.Observation{AccountID: d.ticket.AccountID, Model: p.Model, Profile: profile, Reasoning: reasoning, Transport: transport, ContextBucket: bucket, At: time.Now(), HasSemanticOutput: !semantic.IsZero(), Completed: completed, FirstOutputTimeout: timeout && !clipped, AttributableFailure: !excluded && (upstreamFailure || (err != nil && !timeout)), Excluded: excluded, RetryAfter: retryAfter}
			if !semantic.IsZero() {
				o.TTFT = semantic.Sub(started)
			} else if timeout {
				o.TTFT = time.Since(started)
			}
			if e := d.service.Runtime.Observe(ctx, o); e != nil {
				slog.Warn("scheduling health observation pending", "attempt_id", d.ticket.TicketID, "error", e)
			}
			if knownTerminal {
				_ = d.service.Runtime.ReleaseProbe(ctx, d.decision.ProbeToken)
			}
		}
		trace := SchedulingAttemptTrace{AccountID: d.ticket.AccountID, AttemptID: d.ticket.TicketID, Priority: d.decision.Priority, Reason: d.decision.Reason, Started: started, Outcome: outcome, StopReason: outcome, MetricVersion: SchedulingMetricVersion, PolicyVersion: p.Version}
		ms := func(t time.Time) *int64 {
			if t.IsZero() || started.IsZero() {
				return nil
			}
			v := t.Sub(started).Milliseconds()
			return &v
		}
		trace.FirstEventMS = ms(firstEvent)
		trace.FirstSemanticMS = ms(semantic)
		trace.FirstAnswerMS = ms(answer)
		if !overallSemantic.IsZero() {
			v := overallSemantic.Sub(r.Started).Milliseconds()
			trace.OverallFirstSemanticMS = &v
		}
		if ledger != nil && !ledger.Snapshot().Deadline.IsZero() {
			v := ledger.Remaining(time.Now()).Milliseconds()
			trace.RemainingBudgetMS = &v
		}
		if e := d.service.Store.RecordAttemptMetrics(ctx, d.ticket.TicketID, trace); e != nil {
			slog.Warn("scheduling attempt metrics pending", "attempt_id", d.ticket.TicketID, "error", e)
		}
		r.mu.Lock()
		r.history = append(r.history, trace)
		r.mu.Unlock()
		slog.Info("scheduling_attempt", "request_id", r.ID, "attempt_id", trace.AttemptID, "account_id", trace.AccountID, "priority", trace.Priority, "policy_version", p.Version, "reason", trace.Reason, "outcome", trace.Outcome, "metric_version", trace.MetricVersion, "first_semantic_ms", trace.FirstSemanticMS)
	})
}
func (d *controlledDispatch) observeRateLimit(value string) {
	if d == nil {
		return
	}
	var until time.Time
	if seconds, e := strconv.ParseInt(strings.TrimSpace(value), 10, 64); e == nil && seconds >= 0 && seconds <= 86400*365 {
		until = time.Now().Add(time.Duration(seconds) * time.Second)
	} else if parsed, e := http.ParseTime(value); e == nil {
		until = parsed
	}
	d.mu.Lock()
	d.retryAfter = until
	d.mu.Unlock()
	d.request.mu.Lock()
	ledger := d.request.Ledger
	d.request.mu.Unlock()
	if ledger != nil {
		ledger.BlockFailureDomain(fmt.Sprint(d.ticket.FamilyID))
	}
}

type controlledHTTPUpstream struct {
	base    HTTPUpstream
	service *ControlledSchedulingService
}

func (u *controlledHTTPUpstream) Do(req *http.Request, proxy string, accountID int64, concurrency int) (*http.Response, error) {
	return u.service.roundTrip(req, accountID, concurrency, func(r *http.Request) (*http.Response, error) { return u.base.Do(r, proxy, accountID, concurrency) })
}
func (u *controlledHTTPUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return u.service.roundTrip(req, accountID, concurrency, func(r *http.Request) (*http.Response, error) {
		return u.base.DoWithTLS(r, proxy, accountID, concurrency, profile)
	})
}
func (s *ControlledSchedulingService) roundTrip(req *http.Request, accountID int64, concurrency int, send func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	if req.Method != http.MethodPost && req.Method != http.MethodPut {
		return send(req)
	}
	if req.Context().Value(controlledDispatchContextKey{}) != nil {
		return send(req)
	}
	d, err := s.beginDispatch(req.Context(), accountID, concurrency)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return send(req)
	}
	// Read a duplicate body only when the transport supplied a safe GetBody.
	// Unknown/multipart input has no observable TTFT until an SSE header proves it.
	d.semanticObservable = (strings.Contains(req.URL.Path, "streamGenerateContent") || strings.HasSuffix(req.URL.Path, "/invoke-with-response-stream"))
	if req.GetBody != nil {
		if duplicate, e := req.GetBody(); e == nil {
			raw, _ := io.ReadAll(io.LimitReader(duplicate, 256*1024))
			_ = duplicate.Close()
			if gjson.ValidBytes(raw) {
				d.semanticObservable = d.semanticObservable || gjson.GetBytes(raw, "stream").Bool()
			}
		}
	}
	if err = d.MarkSent(); err != nil {
		d.Finish("not_sent", true, err)
		return nil, err
	}
	response, err := send(req.WithContext(d.Context()))
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			d.Finish("not_sent", true, err)
		} else {
			d.Finish("transport_error", false, err)
		}
		d.mu.Lock()
		timedOut := d.timeout
		d.mu.Unlock()
		if timedOut {
			err = fmt.Errorf("first_output_timeout: %w", context.DeadlineExceeded)
		}
		return response, err
	}
	if response == nil || response.Body == nil {
		d.Finish("empty_response", true, io.ErrUnexpectedEOF)
		return response, io.ErrUnexpectedEOF
	}
	d.mu.Lock()
	d.status = response.StatusCode
	d.upstreamFailure = response.StatusCode == 429 || response.StatusCode >= 500
	d.excluded = response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != 401 && response.StatusCode != 403 && response.StatusCode != 429
	d.mu.Unlock()
	if response.StatusCode == 429 {
		d.observeRateLimit(response.Header.Get("Retry-After"))
	}
	if response.StatusCode == 400 || response.StatusCode == 413 || response.StatusCode == 422 {
		d.request.mu.Lock()
		d.request.ReplaySafe = false
		d.request.mu.Unlock()
	}
	d.noteEvent(false, false, false)
	d.mu.Lock()
	d.semanticObservable = (strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") || strings.Contains(response.Header.Get("Content-Type"), "application/vnd.amazon.eventstream")) && response.StatusCode < 400
	if response.StatusCode >= 400 {
		if d.timer != nil {
			d.timer.Stop()
		}
	} else {
		d.startFirstOutputTimerLocked()
	}
	d.mu.Unlock()
	d.parser.onFrame = d.ObserveFrame
	body := &controlledResponseBody{ReadCloser: response.Body, dispatch: d, sse: strings.Contains(response.Header.Get("Content-Type"), "text/event-stream"), success: response.StatusCode < 400, decoded: strings.Contains(response.Header.Get("Content-Type"), "application/vnd.amazon.eventstream")}
	response.Body = body
	// Close blocked body reads on first-output timeout or explicit forced stop.
	go func() {
		select {
		case <-d.done:
			return
		case <-d.ctx.Done():
			_ = body.ReadCloser.Close()
		}
	}()
	if body.sse && body.success && ControlledSchedulingEnabled(req.Context()) {
		var prefix bytes.Buffer
		buf := make([]byte, 16*1024)
		for {
			n, e := body.Read(buf)
			if n > 0 {
				prefix.Write(buf[:n])
			}
			d.mu.Lock()
			ready := !d.semantic.IsZero() || d.terminal
			timedOut := d.timeout
			d.mu.Unlock()
			if ready {
				response.Body = &prefixedControlledBody{Reader: io.MultiReader(bytes.NewReader(prefix.Bytes()), body), closer: body}
				return response, nil
			}
			if prefix.Len() > openAIFirstOutputStageMaxBytes {
				_ = body.Close()
				return nil, fmt.Errorf("first_output_preamble_limit")
			}
			if e != nil {
				if timedOut {
					return nil, fmt.Errorf("first_output_timeout: %w", context.DeadlineExceeded)
				}
				if e == io.EOF {
					e = io.ErrUnexpectedEOF
				}
				return nil, e
			}
		}
	}
	return response, nil
}

type prefixedControlledBody struct {
	io.Reader
	closer io.Closer
}

func (b *prefixedControlledBody) Close() error { return b.closer.Close() }

type controlledResponseBody struct {
	io.ReadCloser
	dispatch     *controlledDispatch
	sse, success bool
	decoded      bool
}

func (b *controlledResponseBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	if b.decoded {
		return n, e
	}
	if n > 0 && b.success && b.sse {
		b.dispatch.parser.Feed(p[:n], nil)
	}
	if e == io.EOF && b.success && b.sse {
		b.dispatch.parser.Feed([]byte("\n\n"), nil)
	}
	if e != nil {
		outcome := "completed"
		remoteTerminal := e == io.EOF
		observedErr := eIfNotEOF(e)
		if e != io.EOF {
			outcome = "stream_error"
		}
		if !b.success {
			outcome = "http_error"
		}
		if b.success && (b.sse || b.decoded) && e == io.EOF {
			b.dispatch.mu.Lock()
			terminal := b.dispatch.terminal
			b.dispatch.mu.Unlock()
			if !terminal {
				outcome = "stream_truncated"
				remoteTerminal = false
				observedErr = io.ErrUnexpectedEOF
				e = io.ErrUnexpectedEOF
			}
		}
		b.dispatch.Finish(outcome, remoteTerminal, observedErr)
	}
	return n, e
}
func eIfNotEOF(e error) error {
	if e == io.EOF {
		return nil
	}
	return e
}
func (b *controlledResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.dispatch.mu.Lock()
	terminal := b.dispatch.terminal
	b.dispatch.mu.Unlock()
	outcome := "body_closed"
	if terminal && b.success {
		outcome = "completed"
	}
	observedErr := err
	if !terminal && b.success && observedErr == nil {
		observedErr = context.Canceled
	}
	b.dispatch.Finish(outcome, terminal, observedErr)
	return err
}
