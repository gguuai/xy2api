package scheduling

import "time"

// CanAttemptAfterDispatch evaluates a potential timeout fallback after accounting
// for the currently selected dispatch. The real ledger is never consumed here.
func (l *AttemptLedger) CanAttemptAfterDispatch(currentID int64, currentPriority int, nextID int64, nextPriority int, now time.Time, replaySafe bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	copy := &AttemptLedger{policy: l.policy, profile: l.profile, deadline: l.deadline, attempts: l.attempts, perTier: map[int]int{}, perAccount: map[int64]int{}, firstTier: l.firstTier, lastTier: l.lastTier, committed: l.committed, timeoutSeen: l.timeoutSeen, afterTimeout: l.afterTimeout}
	for k, v := range l.perTier {
		copy.perTier[k] = v
	}
	for k, v := range l.perAccount {
		copy.perAccount[k] = v
	}
	if err := copy.BeginAttempt(currentID, currentPriority, now, replaySafe); err != nil {
		return err
	}
	copy.MarkFirstOutputTimeout()
	return copy.CanAttempt(nextID, nextPriority, now, replaySafe)
}
