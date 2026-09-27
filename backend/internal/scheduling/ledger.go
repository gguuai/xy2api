package scheduling

import (
	"sync"
	"time"
)

// AttemptLedger is shared by all adapters for one logical request. Invoke
// BeginAttempt only at the actual dispatch gate, never during candidate selection.
type AttemptLedger struct {
	mu                    sync.Mutex
	policy                RetryPolicy
	profile               LatencyProfile
	deadline              time.Time
	attempts              int
	perTier               map[int]int
	perAccount            map[int64]int
	blockedFailureDomains map[string]bool
	firstTier             *int
	lastTier              *int
	committed             bool
	timeoutSeen           bool
	afterTimeout          int
}
type LedgerSnapshot struct {
	Attempts              int
	PerTier               map[int]int
	PerAccount            map[int64]int
	Deadline              time.Time
	Committed             bool
	TimeoutSeen           bool
	AfterTimeout          int
	LastPriority          *int
	BlockedFailureDomains map[string]bool
}

func NewAttemptLedger(policy RetryPolicy, profile LatencyProfile, started, clientDeadline time.Time) *AttemptLedger {
	policy = NormalizePolicy(Policy{Retry: policy}).Retry
	var deadline time.Time
	if profile.TotalBudgetMS > 0 {
		deadline = started.Add(time.Duration(profile.TotalBudgetMS) * time.Millisecond)
	}
	if !clientDeadline.IsZero() && (deadline.IsZero() || clientDeadline.Before(deadline)) {
		deadline = clientDeadline
	}
	return &AttemptLedger{policy: policy, profile: profile, deadline: deadline, perTier: map[int]int{}, perAccount: map[int64]int{}, blockedFailureDomains: map[string]bool{}}
}
func (l *AttemptLedger) check(accountID int64, priority int, now time.Time, replaySafe bool) error {
	if l.committed {
		return ErrCommitted
	}
	if !l.deadline.IsZero() && !now.Before(l.deadline) {
		return ErrDeadline
	}
	if l.attempts >= l.policy.MaxAttempts || l.perAccount[accountID] >= l.policy.MaxPerAccount {
		return ErrAttemptBudget
	}
	if l.policy.Mode != "exhaust_same_tier" && l.perTier[priority] >= l.policy.MaxPerTier {
		return ErrAttemptBudget
	}
	if l.lastTier != nil && priority < *l.lastTier {
		return ErrAttemptBudget
	}
	if !l.policy.CrossTier && l.firstTier != nil && priority != *l.firstTier {
		return ErrAttemptBudget
	}
	if l.timeoutSeen && l.afterTimeout >= l.policy.MaxAfterTimeout {
		return ErrAttemptBudget
	}
	if l.attempts > 0 {
		if !replaySafe {
			return ErrUnsafeReplay
		}
		minimum := time.Duration(l.profile.MinAttemptWindowMS+l.policy.SwitchMarginMS) * time.Millisecond
		if !l.deadline.IsZero() && l.deadline.Sub(now) < minimum {
			return ErrDeadline
		}
	}
	return nil
}
func (l *AttemptLedger) CanAttempt(accountID int64, priority int, now time.Time, replaySafe bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.check(accountID, priority, now, replaySafe)
}
func (l *AttemptLedger) BeginAttempt(accountID int64, priority int, now time.Time, replaySafe bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.check(accountID, priority, now, replaySafe); err != nil {
		return err
	}
	l.attempts++
	l.perTier[priority]++
	l.perAccount[accountID]++
	if l.timeoutSeen {
		l.afterTimeout++
	}
	if l.firstTier == nil {
		p := priority
		l.firstTier = &p
	}
	p := priority
	l.lastTier = &p
	return nil
}
func (l *AttemptLedger) MarkSemanticCommit() { l.mu.Lock(); defer l.mu.Unlock(); l.committed = true }
func (l *AttemptLedger) MarkFirstOutputTimeout() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.timeoutSeen = true
}
func (l *AttemptLedger) Remaining(now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.deadline.IsZero() {
		return 0
	}
	d := l.deadline.Sub(now)
	if d < 0 {
		return 0
	}
	return d
}
func (l *AttemptLedger) Snapshot() LedgerSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := LedgerSnapshot{Attempts: l.attempts, PerTier: map[int]int{}, PerAccount: map[int64]int{}, Deadline: l.deadline, Committed: l.committed, TimeoutSeen: l.timeoutSeen, AfterTimeout: l.afterTimeout}
	s.BlockedFailureDomains = make(map[string]bool, len(l.blockedFailureDomains))
	for k, v := range l.blockedFailureDomains {
		s.BlockedFailureDomains[k] = v
	}
	for k, v := range l.perTier {
		s.PerTier[k] = v
	}
	for k, v := range l.perAccount {
		s.PerAccount[k] = v
	}
	if l.lastTier != nil {
		p := *l.lastTier
		s.LastPriority = &p
	}
	return s
}
func (l *AttemptLedger) AttemptedAccounts() map[int64]bool {
	s := l.Snapshot()
	out := map[int64]bool{}
	for id, n := range s.PerAccount {
		if n >= l.policy.MaxPerAccount {
			out[id] = true
		}
	}
	return out
}

// AttemptWindow reserves one viable fallback only when the caller has verified
// replay eligibility and shared retry quota. A clipped window is not account TTFT.
func (l *AttemptLedger) AttemptWindow(now time.Time, viableFallback bool) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	cap := time.Duration(l.profile.AttemptTimeoutMS) * time.Millisecond
	var remaining time.Duration
	if !l.deadline.IsZero() {
		remaining = l.deadline.Sub(now)
		if remaining <= 0 {
			return 0
		}
		if cap == 0 || remaining < cap {
			cap = remaining
		}
	}
	if viableFallback && l.policy.ReserveFallback && !l.committed && l.attempts < l.policy.MaxAttempts && (!l.timeoutSeen || l.afterTimeout < l.policy.MaxAfterTimeout) && remaining > 0 {
		minimum := time.Duration(l.profile.MinAttemptWindowMS) * time.Millisecond
		reserve := minimum + time.Duration(l.policy.SwitchMarginMS)*time.Millisecond
		if minimum > 0 && remaining >= minimum+reserve && cap > remaining-reserve {
			cap = remaining - reserve
		}
	}
	return cap
}

// BlockFailureDomain applies only to a classified shared-limit failure in this
// request. The caller remains responsible for persisting an upstream Retry-After
// cooldown at the provider's actual account/credential/project scope.
func (l *AttemptLedger) BlockFailureDomain(domain string) {
	if domain == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.blockedFailureDomains == nil {
		l.blockedFailureDomains = map[string]bool{}
	}
	l.blockedFailureDomains[domain] = true
}
