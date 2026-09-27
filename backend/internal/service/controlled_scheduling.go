package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/redis/go-redis/v9"
)

// ControlledSchedulingService joins the shared allocator to the existing hard
// eligibility rules and the PostgreSQL dispatch linearization point.
type ControlledSchedulingService struct {
	Store          *scheduling.PostgresStore
	Runtime        *scheduling.Runtime
	redis          redis.UniversalClient
	accounts       AccountRepository
	concurrency    *ConcurrencyService
	node           string
	active         sync.Map
	explainOpenAI  SchedulingExplainEligibilityFunc
	explainGateway SchedulingExplainEligibilityFunc
	stop           context.CancelFunc
	stopped        chan struct{}
}

func NewControlledSchedulingService(db *sql.DB, rdb *redis.Client, accounts AccountRepository, concurrency *ConcurrencyService) *ControlledSchedulingService {
	s := &ControlledSchedulingService{Store: scheduling.NewPostgresStore(db), Runtime: scheduling.NewRuntime(scheduling.NewRedisStore(rdb)), redis: rdb, accounts: accounts, concurrency: concurrency, node: uuid.NewString()}
	s.Store.SetCancelHook(s.cancelControlled)
	return s
}

// Start reconciles abandoned tickets without guessing remote completion. Expired
// tickets remain capacity-bearing UNKNOWN until an explicit observed resolution.
func (s *ControlledSchedulingService) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.stop = cancel
	s.stopped = make(chan struct{})
	go func() {
		defer close(s.stopped)
		timer := time.NewTicker(15 * time.Second)
		defer timer.Stop()
		var pubsub *redis.PubSub
		var messages <-chan *redis.Message
		if s.redis != nil {
			pubsub = s.redis.Subscribe(ctx, "xy2:scheduling:control")
			defer pubsub.Close()
			messages = pubsub.Channel()
		}
		reconcile := func() {
			c, done := context.WithTimeout(ctx, 5*time.Second)
			defer done()
			if _, e := s.Store.ReconcileTerminalIntents(c); e != nil && ctx.Err() == nil {
				slog.Warn("scheduling terminal reconciliation unavailable", "error", e)
			}
			if _, e := s.Store.ReconcileExpired(c); e != nil && ctx.Err() == nil {
				slog.Warn("scheduling reconciliation unavailable", "error", e)
			}
		}
		reconcile()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				reconcile()
			case msg, ok := <-messages:
				if !ok {
					messages = nil
					continue
				}
				var control scheduling.ControlSnapshot
				if json.Unmarshal([]byte(msg.Payload), &control) == nil {
					s.cancelLocal(control)
				}
			}
		}
	}()
}
func (s *ControlledSchedulingService) Close() {
	if s != nil && s.stop != nil {
		s.stop()
		<-s.stopped
	}
}
func (s *ControlledSchedulingService) cancelLocal(c scheduling.ControlSnapshot) {
	s.active.Range(func(_, value any) bool {
		d := value.(*controlledDispatch)
		if scheduling.ForceStopApplies(c, d.ticket) {
			d.adminCancelled.Store(true)
			d.cancel()
		}
		return true
	})
}
func (s *ControlledSchedulingService) cancelControlled(ctx context.Context, c scheduling.ControlSnapshot) error {
	s.cancelLocal(c)
	// This is a notification, not the authority. Every node also polls the gate
	// epoch while it holds tickets; a Redis delivery failure cannot undo pause.
	if s.redis == nil {
		return scheduling.ErrSharedState
	}
	raw, _ := json.Marshal(c)
	return s.redis.Publish(ctx, "xy2:scheduling:control", raw).Err()
}
func ControlledSchedulingEnabled(ctx context.Context) bool {
	r := controlledRequest(ctx)
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.policyLoaded && r.Policy.Enabled
}
func (s *ControlledSchedulingService) loadPolicy(ctx context.Context, groupID *int64, model, sessionID string) (*ControlledRequest, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	r := controlledRequest(ctx)
	if r == nil {
		return nil, false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.control = s
	if r.policyLoaded {
		return r, r.Policy.Enabled, nil
	}
	rec, err := s.Store.GetPolicy(ctx, derefGroupID(groupID), model)
	if err != nil {
		return r, false, fmt.Errorf("%w: %v", scheduling.ErrSharedState, err)
	}
	var defaults *scheduling.Policy
	if derefGroupID(groupID) != 0 {
		global, e := s.Store.GetPolicy(ctx, 0, model)
		if e != nil {
			return r, false, fmt.Errorf("%w: %v", scheduling.ErrSharedState, e)
		}
		defaults = global.Policy
	}
	p := scheduling.Policy{GroupID: derefGroupID(groupID), Model: model}
	if rec.Policy != nil {
		p = *rec.Policy
	} else if defaults != nil {
		p = *defaults
		p.GroupID = derefGroupID(groupID)
	}
	p = scheduling.NormalizePolicy(p)
	r.Reasoning = scheduling.NormalizeReasoningLabel(r.Reasoning)
	// Profiles inherit independently of allocation overrides; never mix models.
	profile, ok := scheduling.ResolveProfileForTransport(p, r.Reasoning, r.ContextTokens, r.Protocol)
	if (!ok || (profile.Transport != "" && profile.Transport != r.Protocol)) && defaults != nil {
		profile, ok = scheduling.ResolveProfileForTransport(*defaults, r.Reasoning, r.ContextTokens, r.Protocol)
	}
	if !ok || (profile.Transport != "" && profile.Transport != r.Protocol) {
		profile = scheduling.LatencyProfile{Name: "unconfigured"}
	}
	r.Model = model
	r.SessionID = sessionID
	r.Policy = p
	r.Profile = profile
	r.policyLoaded = true
	if p.Enabled {
		r.Ledger = scheduling.NewAttemptLedger(p.Retry, profile, r.Started, r.ClientDeadline)
	}
	return r, p.Enabled, nil
}
func (s *ControlledSchedulingService) releaseDecision(d scheduling.Decision) {
	if s == nil || d.ReservationID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Runtime.ReleaseSelection(ctx, d); err != nil {
		slog.Warn("scheduling selection compensation pending", "error", err)
	}
}
func controlledBucket(r *ControlledRequest) string {
	if r.ContextTokens < 0 {
		return "unknown"
	}
	return scheduling.ContextBucket(r.ContextTokens)
}
func (s *ControlledSchedulingService) selectAccount(ctx context.Context, r *ControlledRequest, accounts []*Account, eligible func(*Account) (bool, string), excluded map[int64]struct{}, acquire bool) (*AccountSelectionResult, error) {
	r.mu.Lock()
	old := r.Decision
	pending := r.decisionPending
	r.decisionPending = false
	p := r.Policy
	profile := r.Profile
	ledger := r.Ledger
	reasoning := r.Reasoning
	transport := r.Protocol
	bucket := controlledBucket(r)
	safe := r.ReplaySafe
	ownerID := r.ownerAccountID
	sessionID := r.SessionID
	r.mu.Unlock()
	if ownerID > 0 {
		p.Mode = scheduling.ModePin
		p.PinAccountID = ownerID
		p.PinFallback = false
	}
	if pending {
		s.releaseDecision(old)
	}
	if ledger == nil {
		return nil, fmt.Errorf("%w: missing request ledger", scheduling.ErrSharedState)
	}
	snapshot := ledger.Snapshot()
	if snapshot.Committed {
		return nil, scheduling.ErrCommitted
	}
	if snapshot.Attempts >= p.Retry.MaxAttempts || (snapshot.TimeoutSeen && snapshot.AfterTimeout >= p.Retry.MaxAfterTimeout) {
		return nil, scheduling.ErrAttemptBudget
	}
	if snapshot.Attempts > 0 && !safe {
		return nil, scheduling.ErrUnsafeReplay
	}
	if !snapshot.Deadline.IsZero() {
		remaining := ledger.Remaining(time.Now())
		minimum := time.Duration(profile.MinAttemptWindowMS+p.Retry.SwitchMarginMS) * time.Millisecond
		if remaining <= 0 || (snapshot.Attempts > 0 && remaining < minimum) {
			return nil, scheduling.ErrDeadline
		}
	}
	r.mu.Lock()
	backoff := snapshot.Attempts > r.lastBackoffAttempt
	r.lastBackoffAttempt = snapshot.Attempts
	r.mu.Unlock()
	if backoff && snapshot.Attempts > 0 {
		capMS := 50 << min(snapshot.Attempts-1, 2)
		wait := time.Duration(rand.IntN(capMS+1)) * time.Millisecond
		if !snapshot.Deadline.IsZero() {
			available := ledger.Remaining(time.Now()) - time.Duration(profile.MinAttemptWindowMS+p.Retry.SwitchMarginMS)*time.Millisecond
			if available <= 0 {
				return nil, scheduling.ErrDeadline
			}
			if wait > available {
				wait = available
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	req := scheduling.SelectionRequest{Policy: p, Profile: profile, Reasoning: reasoning, ContextBucket: bucket, Transport: transport, Attempted: ledger.AttemptedAccounts(), ExcludedFailureDomains: snapshot.BlockedFailureDomains, MinPriority: snapshot.LastPriority, Retry: snapshot.Attempts > 0}
	byID := map[int64]*Account{}
	loads := make([]AccountWithConcurrency, 0, len(accounts))
	ids := make([]int64, 0, len(accounts))
	for _, a := range accounts {
		if a == nil {
			continue
		}
		byID[a.ID] = a
		loads = append(loads, AccountWithConcurrency{ID: a.ID, MaxConcurrency: a.Concurrency})
		ids = append(ids, a.ID)
	}
	active, err := s.Store.AccountActiveCounts(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", scheduling.ErrSharedState, err)
	}
	loadMap := map[int64]*AccountLoadInfo{}
	if s.concurrency != nil {
		loadMap, err = s.concurrency.GetAccountsLoadBatchFresh(ctx, loads)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", scheduling.ErrSharedState, err)
		}
	}
	for _, a := range accounts {
		if a == nil {
			continue
		}
		ok, _ := eligible(a)
		if _, skip := excluded[a.ID]; skip {
			ok = false
		}
		admit, e := s.Store.CanAdmitControl(ctx, a.ID, sessionID)
		if e != nil {
			return nil, e
		}
		if !admit {
			ok = false
		}
		priority := a.Priority
		for _, rule := range p.Accounts {
			if rule.AccountID == a.ID && rule.Priority != nil {
				priority = *rule.Priority
			}
		}
		if ledger.CanAttempt(a.ID, priority, time.Now(), safe) != nil {
			ok = false
		}
		used := active[a.ID]
		if loadMap[a.ID] != nil && loadMap[a.ID].CurrentConcurrency > used {
			used = loadMap[a.ID].CurrentConcurrency
		}
		family := a.ID
		if a.ParentAccountID != nil {
			family = *a.ParentAccountID
		}
		req.Candidates = append(req.Candidates, scheduling.Candidate{AccountID: a.ID, Priority: a.Priority, HardEligible: ok, CapacityAvailable: a.Concurrency <= 0 || used < a.Concurrency, FailureDomain: fmt.Sprint(family), FailureDomains: controlledAccountFailureDomains(a, p.Model)})
	}
	queueUntil := time.Now().Add(time.Duration(p.QueueWaitMS) * time.Millisecond)
	if !snapshot.Deadline.IsZero() && snapshot.Deadline.Before(queueUntil) {
		queueUntil = snapshot.Deadline
	}
	for iteration := 0; iteration < 128; iteration++ {
		req.Now = time.Now()
		d, e := s.Runtime.Select(ctx, req)
		if e != nil {
			if errors.Is(e, scheduling.ErrCapacity) && p.Overflow == scheduling.OverflowWait && time.Now().Before(queueUntil) {
				delay := time.Until(queueUntil)
				if delay > 50*time.Millisecond {
					delay = 50 * time.Millisecond
				}
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				case <-timer.C:
				}
				fresh, e := s.Store.AccountActiveCounts(ctx, ids)
				if e != nil {
					return nil, e
				}
				var lm map[int64]*AccountLoadInfo
				if s.concurrency != nil {
					lm, e = s.concurrency.GetAccountsLoadBatchFresh(ctx, loads)
					if e != nil {
						return nil, e
					}
				}
				for i := range req.Candidates {
					a := byID[req.Candidates[i].AccountID]
					used := fresh[a.ID]
					if lm[a.ID] != nil && lm[a.ID].CurrentConcurrency > used {
						used = lm[a.ID].CurrentConcurrency
					}
					req.Candidates[i].CapacityAvailable = a.Concurrency <= 0 || used < a.Concurrency
				}
				iteration--
				continue
			}
			if errors.Is(e, scheduling.ErrNoCandidate) && p.AllDegraded != scheduling.AllDegradedFailFast && !snapshot.Deadline.IsZero() {
				at, readErr := s.Runtime.NextRecovery(ctx, req)
				if readErr != nil {
					return nil, readErr
				}
				minimum := time.Duration(profile.MinAttemptWindowMS+p.Retry.SwitchMarginMS) * time.Millisecond
				if !at.IsZero() && at.After(time.Now()) && !at.Add(minimum).After(snapshot.Deadline) {
					timer := time.NewTimer(time.Until(at))
					select {
					case <-ctx.Done():
						timer.Stop()
						return nil, ctx.Err()
					case <-timer.C:
					}
					iteration--
					continue
				}
			}
			return nil, e
		}
		a := byID[d.AccountID]
		fresh, e := s.accounts.GetByID(ctx, a.ID)
		if e != nil {
			s.releaseDecision(d)
			return nil, e
		}
		if fresh == nil {
			s.releaseDecision(d)
			return nil, scheduling.ErrNoCandidate
		}
		ok, _ := eligible(fresh)
		if !ok {
			s.releaseDecision(d)
			for i := range req.Candidates {
				if req.Candidates[i].AccountID == a.ID {
					req.Candidates[i].HardEligible = false
				}
			}
			continue
		}
		var release func()
		if acquire {
			if s.concurrency == nil {
				s.releaseDecision(d)
				return nil, scheduling.ErrSharedState
			}
			slot, e := s.concurrency.AcquireAccountSlot(ctx, a.ID, fresh.Concurrency)
			if e != nil {
				s.releaseDecision(d)
				return nil, e
			}
			if slot == nil || !slot.Acquired {
				s.releaseDecision(d)
				for i := range req.Candidates {
					if req.Candidates[i].AccountID == a.ID {
						req.Candidates[i].CapacityAvailable = false
					}
				}
				continue
			}
			var once sync.Once
			release = func() { once.Do(slot.ReleaseFunc) }
		}
		fallback := false
		if safe && p.Retry.ReserveFallback && !(p.Mode == scheduling.ModePin && !p.PinFallback) {
			// Preview runs before BeginAttempt and before this dispatch's shared
			// budget reservation. Include both in the hypothetical next attempt.
			credit, readErr := s.Runtime.CanRetryAfterDispatch(ctx, p, r.ID, snapshot.Attempts > 0)
			if readErr == nil && credit {
				preview := req
				preview.Now = time.Now()
				preview.Retry = true
				minimumPriority := d.Priority
				preview.MinPriority = &minimumPriority
				preview.Candidates = append([]scheduling.Candidate(nil), req.Candidates...)
				for i := range preview.Candidates {
					c := &preview.Candidates[i]
					priority := c.Priority
					for _, rule := range p.Accounts {
						if rule.AccountID == c.AccountID && rule.Priority != nil {
							priority = *rule.Priority
						}
					}
					if c.AccountID == d.AccountID || ledger.CanAttemptAfterDispatch(d.AccountID, d.Priority, c.AccountID, priority, preview.Now, safe) != nil {
						c.HardEligible = false
					}
				}
				if inspection, e := s.Runtime.InspectSelection(ctx, preview); e == nil && inspection.Selected != nil {
					fallback = true
				}
			}
		}
		r.mu.Lock()
		r.Decision = d
		r.decisionPending = true
		r.fallback = fallback
		r.mu.Unlock()
		return &AccountSelectionResult{Account: fresh, Acquired: acquire, ReleaseFunc: release}, nil
	}
	return nil, fmt.Errorf("scheduler_selection_iteration_limit")
}

// CloseControlledScheduling follows the existing application cleanup lifecycle.
func (s *OpenAIGatewayService) CloseControlledScheduling() {
	if s != nil && s.controlledScheduling != nil {
		s.controlledScheduling.Close()
	}
}
