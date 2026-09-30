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
	service              *ControlledSchedulingService
	request              *ControlledRequest
	ticket               scheduling.DispatchTicket
	budget               scheduling.BudgetReservation
	decision             scheduling.Decision
	ctx                  context.Context
	cancel               context.CancelFunc
	mu                   sync.Mutex
	once                 sync.Once
	sent                 bool
	semantic             time.Time
	answer               time.Time
	firstEvent           time.Time
	started              time.Time
	attemptDeadline      time.Time
	terminal             bool
	upstreamFailure      bool
	timeout              bool
	clipped              bool
	status               int
	timer                *time.Timer
	done                 chan struct{}
	semanticReady        chan struct{}
	adminCancelled       atomic.Bool
	excluded             bool
	toolPending          bool
	retryAfter           time.Time
	parser               semanticEventParser
	semanticObservable   bool
	viableFallback       bool
	healthObservation    scheduling.Observation
	failureEvidence      scheduling.FailureEvidence
	commitToolPending    bool
	transportTerminal    bool
	responseDrainTimeout bool
}

func (s *ControlledSchedulingService) beginDispatch(ctx context.Context, accountID int64, concurrency int) (dispatch *controlledDispatch, dispatchErr error) {
	if s == nil || controlledRequest(ctx) == nil || Sub2APISchedulingEnabled(ctx) {
		return nil, nil
	}
	r := controlledRequest(ctx)
	liveParent := ctx
	ctx, stopPreparation := controlledPreparationContext(ctx, r)
	defer stopPreparation()
	defer func() { dispatchErr = controlledPreparationError(liveParent, ctx, dispatchErr) }()
	r.mu.Lock()
	p := r.Policy
	ledger := r.Ledger
	decision := r.Decision
	safe := r.ReplaySafe
	sessionID := r.SessionID
	owner := r.owner
	ownerAccountID := r.ownerAccountID
	r.mu.Unlock()
	// No transport can have been sent before beginDispatch returns. Any failed
	// preparation must release its unused selection, including owner readmission
	// whose recovery probe was acquired without the normal selection path.
	defer func() {
		if dispatchErr != nil && dispatch == nil && (decision.AccountID == 0 || decision.AccountID == accountID) && (!owner || ownerAccountID == 0 || ownerAccountID == accountID) {
			s.releaseDecisionContext(ctx, decision)
			r.mu.Lock()
			if r.Decision.ReservationID == decision.ReservationID && r.Decision.ProbeToken == decision.ProbeToken {
				r.decisionPending = false
			}
			r.mu.Unlock()
		}
	}()
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
	var healthObservation scheduling.Observation
	if p.Enabled {
		admission := controlledFailureAdmission(ctx)
		if admission == nil {
			a, err := s.accounts.GetByID(ctx, accountID)
			if err != nil {
				return nil, err
			}
			if a == nil {
				return nil, scheduling.ErrNoCandidate
			}
			frozen, err := s.Store.FreezeFailureAdmission(ctx, accountID, a.GetMappedModel(p.Model))
			if err != nil {
				return nil, err
			}
			admission = &frozen
			ctx = context.WithValue(ctx, controlledFailureAdmissionKey{}, admission)
		}
		r.mu.Lock()
		healthObservation = scheduling.Observation{AccountID: accountID, Model: admission.Model, Profile: r.Profile, Reasoning: r.Reasoning, Transport: r.Protocol, ContextBucket: controlledBucket(r), HealthIdentity: admission.HealthIdentity}
		r.mu.Unlock()
		var fence scheduling.HealthFence
		var err error
		if p.AccountPool && (decision.HealthFence == nil || decision.HealthFence.Model != healthObservation.Model || decision.HealthFence.HealthIdentity != healthObservation.HealthIdentity) {
			boundAccountID := int64(0)
			if owner && ownerAccountID == accountID {
				boundAccountID = ownerAccountID
			}
			readmitted, readmitErr := s.Runtime.ReadmitAccountPoolHealth(ctx, decision, healthObservation, boundAccountID)
			if readmitErr != nil {
				// Readmission owns compensation. An unsent rejection must not
				// be selected repeatedly through its still-healthy client alias.
				r.mu.Lock()
				r.decisionPending = false
				r.mu.Unlock()
				if !owner && !errors.Is(readmitErr, scheduling.ErrSharedState) && (errors.Is(readmitErr, scheduling.ErrHealthSelectionStale) || errors.Is(readmitErr, scheduling.ErrCapacity)) {
					return nil, s.invalidateControlledSelection(ctx, r, scheduling.Decision{AccountID: accountID})
				}
				return nil, readmitErr
			}
			decision = readmitted
			decision.Reason += "_actual_model_revalidated"
			fence = *decision.HealthFence
			r.mu.Lock()
			r.Decision = decision
			r.decisionPending = decision.ReservationID != "" || decision.ProbeToken != ""
			r.mu.Unlock()
		} else {
			if !p.AccountPool && decision.HealthFence != nil && decision.HealthFence.Model != healthObservation.Model {
				// Adapters may canonicalize after the candidate model mapping.
				// Re-admit the actual upstream model from its own health record;
				// never transfer predicted-model health to this model.
				decision.HealthFence = nil
				decision.Reason += "_actual_model_revalidated"
			}
			fence, err = s.Runtime.FreezeSelectedHealth(ctx, accountID, healthObservation.Model, healthObservation.Profile, healthObservation.Reasoning, healthObservation.ContextBucket, healthObservation.Transport, healthObservation.HealthIdentity, decision)
		}
		if err != nil {
			if errors.Is(err, scheduling.ErrHealthSelectionStale) && !owner && p.Mode != scheduling.ModePin {
				return nil, s.invalidateControlledSelection(ctx, r, decision)
			}
			return nil, err
		}
		healthObservation.Fence = &fence
		decision.Probe = fence.State == scheduling.HealthUnknown || fence.State == scheduling.HealthHalfOpen || fence.State == scheduling.HealthRecovering
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
	ticket, e := s.Store.BeginDispatch(ctx, scheduling.DispatchRequest{RequestID: r.ID, AccountID: accountID, SessionID: sessionID, NodeID: s.node, LeaseDuration: 2 * time.Minute, HardConcurrency: concurrency, Failure: controlledFailureAdmission(ctx)})
	if e != nil {
		if br.ID != "" {
			s.refundDispatchBudgetContext(ctx, br)
		}
		if p.Enabled && !owner && p.Mode != scheduling.ModePin && (errors.Is(e, scheduling.ErrControlBlocked) || errors.Is(e, scheduling.ErrCapacity) || errors.Is(e, scheduling.ErrFailureDomainBlocked) || errors.Is(e, scheduling.ErrVersionConflict)) {
			s.releaseDecisionContext(ctx, decision)
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
		if !errors.Is(e, scheduling.ErrControlBlocked) && !errors.Is(e, scheduling.ErrCapacity) && !errors.Is(e, scheduling.ErrAttemptIdentity) && !errors.Is(e, scheduling.ErrFailureDomainBlocked) && !errors.Is(e, scheduling.ErrVersionConflict) {
			return nil, fmt.Errorf("%w: %v", scheduling.ErrSharedState, e)
		}
		return nil, e
	}
	live, cancel := context.WithCancel(liveParent)
	d := &controlledDispatch{service: s, request: r, ticket: ticket, budget: br, decision: decision, cancel: cancel, done: make(chan struct{}), semanticReady: make(chan struct{}), semanticObservable: true, healthObservation: healthObservation}
	d.ctx = context.WithValue(live, controlledDispatchContextKey{}, d)
	if leaseErr := (scheduling.RedisFailureDomains{Client: s.redis}).Acquire(ctx, ticket); leaseErr != nil {
		d.finishPreparationFailure(leaseErr)
		if p.Enabled && !owner && p.Mode != scheduling.ModePin && errors.Is(leaseErr, scheduling.ErrFailureDomainBlocked) {
			r.mu.Lock()
			r.gateRejections++
			r.decisionPending = false
			rejections := r.gateRejections
			r.mu.Unlock()
			if rejections >= 128 {
				return nil, scheduling.ErrAttemptBudget
			}
			return nil, &UpstreamFailoverError{StatusCode: 503, PreDispatchSelectionInvalidated: true, ClientMessage: "domain recovery lease occupied"}
		}
		return nil, leaseErr
	}
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
func (d *controlledDispatch) MarkSent() (sendErr error) {
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
	prepare, stopPreparation := controlledPreparationContext(d.ctx, r)
	defer stopPreparation()
	defer func() { sendErr = controlledPreparationError(d.ctx, prepare, sendErr) }()
	r.mu.Lock()
	p, ledger, safe, owner, fallback := r.Policy, r.Ledger, r.ReplaySafe, r.owner, r.fallback
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
	if err := d.service.Store.RecordAttemptMetrics(prepare, d.ticket.TicketID, map[string]any{"sent_at": time.Now().UTC().Format(time.RFC3339Nano), "group_id": p.GroupID, "model": p.Model, "policy_version": p.Version, "dispatch_kind": kind, "attempt_number": attempt, "priority": d.decision.Priority, "reason": d.decision.Reason, "metric_version": SchedulingMetricVersion}); err != nil {
		return fmt.Errorf("%w: dispatch record: %v", scheduling.ErrSharedState, err)
	}
	if p.Enabled {
		if d.budget.ID != "" {
			if err := d.service.Runtime.CommitDispatchBudget(prepare, d.budget); err != nil {
				return err
			}
		}
		if d.decision.ReservationID != "" {
			if err := d.service.Runtime.CommitSelection(prepare, d.decision); err != nil {
				return err
			}
		}
		if safe && fallback {
			if allowed, err := d.service.Runtime.CanRetry(prepare, p); err == nil && allowed {
				d.viableFallback = true
			}
		}
		if err := prepare.Err(); err != nil {
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
	r.mu.Unlock()
	if p.Enabled {
		d.startFirstOutputTimerLocked()
	}
	return nil
}

// Called with d.mu held. Each account-pool attempt, including non-streaming
// work, is bounded by its waiting limit and the remaining request budget. A
// non-streaming completion never fabricates first-semantic-output evidence.
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
	if d.semanticObservable || p.AccountPool {
		window = ledger.AttemptWindow(now, d.viableFallback)
	}
	if window <= 0 && ledger.Snapshot().Deadline.IsZero() {
		return
	}
	deadline := now.Add(window)
	configured := time.Duration(profile.AttemptTimeoutMS) * time.Millisecond
	if (d.semanticObservable || p.AccountPool) && configured > 0 {
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
	if p.AccountPool {
		d.clipped = configured <= 0 || deadline.Before(d.started.Add(configured))
	}
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
	if !gjson.ValidBytes(frame) && !bytes.Equal(bytes.TrimSpace(frame), []byte("[DONE]")) {
		return // partial JSON cannot prove remote completion or provider failure
	}
	semantic, answer, terminal, tool := classifySemanticEvent(frame)
	v := gjson.ParseBytes(frame)
	kind := v.Get("type").String()
	status := controlledProtocolResponseStatus(v)
	failure := kind == "error" || kind == "response.failed" || status == "failed" ||
		v.Get("error").IsObject() || v.Get("response.error").IsObject()
	incomplete := kind == "response.incomplete" || kind == "response.cancelled" || kind == "response.canceled" ||
		status == "incomplete" || status == "cancelled" || status == "canceled"
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
	if failure || incomplete {
		terminal = true
		requestFailure := false
		// Anthropic error frames commonly carry only error.type. Responses
		// can nest the same classification; neither form is availability loss.
		for _, path := range []string{"error.code", "error.type", "response.error.code", "response.error.type"} {
			switch v.Get(path).String() {
			case "invalid_request_error", "invalid_request", "context_length_exceeded", "content_policy_violation", "cyber_policy":
				requestFailure = true
			}
		}
		if requestFailure {
			d.excluded = true
			d.request.mu.Lock()
			d.request.ReplaySafe = false
			d.request.mu.Unlock()
		} else {
			if incomplete {
				d.excluded = true
			} else {
				d.upstreamFailure = true
				d.observeProtocolFailureLocked(v)
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
	// Capacity may be reused after the persisted lease expires. Independently
	// cancel this local transport at that deadline, including while renewal is
	// blocked, rather than continuing an unleased stream indefinitely.
	leaseExpired := make(chan struct{})
	leaseTimer := time.AfterFunc(time.Until(d.ticket.LeaseUntil), func() {
		d.cancel()
		close(leaseExpired)
	})
	defer leaseTimer.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ticks := 0
	for {
		select {
		case <-d.done:
			return
		case <-leaseExpired:
			return
		case <-ticker.C:
			ticks++
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if ticks%30 == 0 {
				renewalStarted := time.Now()
				if err := d.service.Store.RenewAttempt(ctx, d.ticket.TicketID, 2*time.Minute); err != nil {
					slog.Warn("scheduling lease renewal uncertain", "attempt_id", d.ticket.TicketID, "error", err)
				} else if leaseTimer.Stop() {
					// Starting before the database call is conservative with respect
					// to the server's NOW(); response latency cannot extend our lease.
					leaseTimer.Reset(time.Until(renewalStarted.Add(2 * time.Minute)))
				} else {
					// An expired local holder cannot revive itself after losing its
					// slot, even if a delayed renewal response arrives successfully.
					d.cancel()
					cancel()
					return
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
		// Cancellation/timeout may rename the outcome below, but cannot erase
		// transport proof that no request was sent.
		provenNotSent := !sent || outcome == "not_sent"
		timeout, clipped, upstreamFailure, explicitExcluded, terminal, retryAfter, status := d.timeout, d.clipped, d.upstreamFailure, d.excluded, d.terminal, d.retryAfter, d.status
		semanticObservable := d.semanticObservable
		d.mu.Unlock()
		close(d.done)
		d.cancel()
		d.service.active.Delete(d.ticket.TicketID)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r := d.request
		r.mu.Lock()
		p, ledger := r.Policy, r.Ledger
		overallSemantic := r.semanticAt
		r.mu.Unlock()
		excluded := explicitExcluded || d.adminCancelled.Load() || (r.clientContext != nil && r.clientContext.Err() != nil) || (errors.Is(err, context.Canceled) && !timeout) || (timeout && clipped)
		if timeout {
			outcome = "first_output_timeout"
			if p.AccountPool && !semanticObservable {
				outcome = "attempt_timeout"
			}
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
			d.service.releaseDecisionContext(ctx, d.decision)
			if d.budget.ID != "" {
				_ = d.service.Runtime.RefundDispatchBudget(ctx, d.budget)
			}
			remoteTerminal = true
			outcome = "not_sent"
		}
		// A final HTTP error status is a known rejection even if its diagnostic
		// body stalls or is truncated. Waiting for diagnostic EOF must not leave
		// a rejected request holding UNKNOWN execution capacity indefinitely.
		knownTerminal := remoteTerminal || terminal || (sent && status >= http.StatusBadRequest)
		completed := sent && knownTerminal && status < 400 && outcome != "response_unvalidated" && !upstreamFailure && !excluded && err == nil && !timeout
		if upstreamFailure && outcome == "completed" {
			outcome = "upstream_error"
		}

		fd := d.classifyFailureDomains(outcome, err)
		if knownTerminal {
			certainty := "remote_terminal"
			if provenNotSent {
				certainty = "proven_not_sent"
			}
			usagePending := sent && status < 400 && certainty != "proven_not_sent"
			var intentErr error
			if d.ticket.Failure != nil {
				intentErr = d.service.Store.RecordTerminalFailureIntent(ctx, d.ticket.TicketID, outcome, certainty, usagePending, fd, completed)
			} else {
				intentErr = d.service.Store.RecordTerminalIntent(ctx, d.ticket.TicketID, outcome, certainty, usagePending)
			}
			if intentErr != nil {
				excluded = true
				slog.Warn("scheduling terminal and failure intent pending", "attempt_id", d.ticket.TicketID, "error", intentErr)
				if e := d.service.Store.MarkAttemptUnknown(ctx, d.ticket.TicketID); e != nil {
					slog.Warn("scheduling unknown persistence pending", "attempt_id", d.ticket.TicketID, "error", e)
				}
			} else if e := d.service.Store.SettleAttempt(ctx, d.ticket.TicketID, outcome, usagePending); e != nil {
				slog.Warn("scheduling settlement pending", "attempt_id", d.ticket.TicketID, "error", e)
			}
		} else {
			if d.ticket.Failure != nil {
				if e := d.service.Store.RecordFailureIntent(ctx, d.ticket.TicketID, fd, completed); e != nil {
					slog.Warn("scheduling failure intent pending", "attempt_id", d.ticket.TicketID, "error", e)
				}
			}
			if e := d.service.Store.MarkAttemptUnknown(ctx, d.ticket.TicketID); e != nil {
				slog.Warn("scheduling unknown attempt persistence failed", "attempt_id", d.ticket.TicketID, "error", e)
			}
		}
		if d.ticket.Failure != nil {
			if e := d.service.Store.ApplyFailureFeedback(ctx, d.ticket.TicketID, fd, completed); e != nil {
				slog.Warn("scheduling failure feedback pending", "attempt_id", d.ticket.TicketID, "error", e)
			}
		}

		// Redis coordinates a live local attempt. PG retains the bounded unknown
		// hold and authoritative cooldown, so cancelling a local attempt must not
		// leave a longer Redis lease delaying the next permitted recovery probe.
		if releaseErr := (scheduling.RedisFailureDomains{Client: d.service.redis}).Release(ctx, d.ticket); releaseErr != nil {
			slog.Warn("scheduling domain lease release pending", "attempt_id", d.ticket.TicketID, "error", releaseErr)
		}
		if fd.Class == "authentication" || fd.Class == "rate_limit" || fd.Class == "model_capability" || fd.Class == "invalid_request" {
			excluded = true
		}
		if sent && p.Enabled {
			if e := d.service.Runtime.ForgetDispatchReceipts(ctx, d.decision, d.budget); e != nil {
				slog.Warn("scheduling receipt cleanup pending", "attempt_id", d.ticket.TicketID, "error", e)
			}
			o := d.healthObservation
			o.AttemptID = d.ticket.TicketID
			o.At = time.Now()
			o.HasSemanticOutput = !semantic.IsZero()
			o.Completed = completed
			o.FirstOutputTimeout = timeout && !clipped && semanticObservable
			o.AttributableFailure = !excluded && (upstreamFailure || (err != nil && !timeout) || (p.AccountPool && timeout && !clipped && !semanticObservable))
			o.Excluded = excluded
			o.RetryAfter = retryAfter
			if !semantic.IsZero() {
				o.TTFT = semantic.Sub(started)
			} else if timeout && semanticObservable {
				o.TTFT = time.Since(started)
			}
			if current, e := d.service.Store.WithCurrentFailureIdentity(ctx, d.ticket.Failure, func() error { return d.service.Runtime.Observe(ctx, o) }); e != nil {
				slog.Warn("scheduling health observation pending", "attempt_id", d.ticket.TicketID, "error", e)
			} else if !current {
				slog.Info("scheduling stale terminal observation ignored", "attempt_id", d.ticket.TicketID)
			}
			_ = d.service.Runtime.ReleaseProbe(ctx, d.decision.ProbeToken)
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
	// Retry-After is timing evidence only. Shared exclusion belongs to the
	// structured, explicitly declared scope in classifyFailureDomains.
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
	if controlledAuxiliaryRequest(req.Context()) && ControlledSchedulingEnabled(req.Context()) {
		allowed, err := s.Store.CanAdmitControl(req.Context(), accountID, "")
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, scheduling.ErrControlBlocked
		}
		return send(req)
	}
	if req.Method != http.MethodPost && req.Method != http.MethodPut {
		return send(req)
	}
	if req.Context().Value(controlledDispatchContextKey{}) != nil {
		return send(req)
	}
	req, err := s.prepareControlledFailureRequest(req, accountID)
	if err != nil {
		return nil, err
	}
	d, err := s.beginDispatch(req.Context(), accountID, concurrency)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return send(req)
	}
	buffered, _ := req.Context().Value(controlledBufferedResponseContextKey{}).(bool)
	// Read a duplicate body only when the transport supplied a safe GetBody.
	// Unknown/multipart input has no observable TTFT until an SSE header proves it.
	d.semanticObservable = !buffered && (strings.Contains(req.URL.Path, "streamGenerateContent") || strings.HasSuffix(req.URL.Path, "/invoke-with-response-stream"))
	if !buffered && req.GetBody != nil {
		if duplicate, e := req.GetBody(); e == nil {
			_, stream, valid := ReadControlledOutboundMetadata(duplicate)
			_ = duplicate.Close()
			if valid {
				d.semanticObservable = d.semanticObservable || stream
			}
		}
	}
	if err = d.MarkSent(); err != nil {
		d.finishPreparationFailure(err)
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
	// Cancellation must close error bodies too, including while the bounded
	// evidence prefix is being read below. Capture the original body, not the
	// response field which the evidence and protocol wrappers replace.
	upstreamBody := response.Body
	go func() {
		select {
		case <-d.done:
		case <-d.ctx.Done():
			_ = upstreamBody.Close()
		}
	}()
	d.mu.Lock()
	d.status = response.StatusCode
	d.upstreamFailure = response.StatusCode == 429 || response.StatusCode >= 500
	d.excluded = response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != 401 && response.StatusCode != 403 && response.StatusCode != 429
	if response.StatusCode >= 400 {
		d.startErrorBodyTimerLocked()
	}
	d.mu.Unlock()
	if response.StatusCode == 429 {
		d.observeRateLimit(response.Header.Get("Retry-After"))
	}
	d.observeFailureResponse(response)
	d.noteEvent(false, false, false)
	d.mu.Lock()
	d.semanticObservable = !buffered && (strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") || strings.Contains(response.Header.Get("Content-Type"), "application/vnd.amazon.eventstream")) && response.StatusCode < 400
	if response.StatusCode < 400 {
		d.startFirstOutputTimerLocked()
	}
	d.mu.Unlock()
	d.parser.onFrame = d.ObserveFrame
	body := &controlledResponseBody{ReadCloser: response.Body, dispatch: d, buffered: buffered, sse: strings.Contains(response.Header.Get("Content-Type"), "text/event-stream"), success: response.StatusCode < 400, decoded: strings.Contains(response.Header.Get("Content-Type"), "application/vnd.amazon.eventstream")}
	response.Body = body
	if body.sse && !body.buffered && body.success && ControlledSchedulingEnabled(req.Context()) {
		var prefix bytes.Buffer
		buf := make([]byte, 16*1024)
		for {
			n, e := body.Read(buf)
			if n > 0 {
				_, _ = prefix.Write(buf[:n])
			}
			d.mu.Lock()
			ready := !d.semantic.IsZero() || d.terminal
			timedOut := d.timeout
			d.mu.Unlock()
			if ready {
				var tail io.Reader = body
				if e != nil {
					// Prefetch may receive content and its terminal error together.
					// Preserve that error after the prefix instead of reading a body
					// already closed by Finish and losing the original certainty.
					tail = controlledReadError{err: e}
				}
				response.Body = &prefixedControlledBody{Reader: io.MultiReader(bytes.NewReader(prefix.Bytes()), tail), closer: body}
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

type controlledReadError struct{ err error }

func (r controlledReadError) Read([]byte) (int, error) { return 0, r.err }

func (b *prefixedControlledBody) Close() error { return b.closer.Close() }

type controlledResponseBody struct {
	io.ReadCloser
	dispatch               *controlledDispatch
	sse, success           bool
	buffered               bool
	decoded                bool
	nonstreamValidated     bool
	nonstreamValidationErr error
}

func (b *controlledResponseBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	e = b.dispatch.responseReadError(e)
	if b.decoded {
		return n, e
	}
	if n > 0 && b.success && b.sse && !b.buffered {
		b.dispatch.parser.Feed(p[:n], nil)
	}
	if e == io.EOF && b.success && b.sse && !b.buffered {
		b.dispatch.parser.Feed([]byte("\n\n"), nil)
	}
	if e != nil {
		if e == io.EOF && b.success && (!b.sse || b.buffered) {
			// EOF proves remote completion, not a valid application response.
			// The adapter confirms its parsed result before health/recovery credit.
			b.dispatch.mu.Lock()
			b.dispatch.transportTerminal = true
			if b.dispatch.timer != nil {
				b.dispatch.timer.Stop()
			}
			b.dispatch.mu.Unlock()
			return n, e
		}
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
	transportTerminal := b.dispatch.transportTerminal
	validationErr := b.nonstreamValidationErr
	b.dispatch.mu.Unlock()
	outcome := "body_closed"
	if terminal && b.success {
		outcome = "completed"
	}
	observedErr := err
	if transportTerminal && (!b.sse || b.buffered) && !b.decoded {
		outcome = "response_unvalidated"
		terminal = true
		if validationErr != nil {
			outcome, observedErr = "invalid_response", validationErr
		}
	}
	if !terminal && b.success && observedErr == nil {
		observedErr = context.Canceled
	}
	b.dispatch.Finish(outcome, terminal, observedErr)
	return err
}
