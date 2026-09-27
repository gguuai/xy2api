package scheduling

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func profile() LatencyProfile {
	return LatencyProfile{Name: "chat", HealthThresholdMS: 8000, RecoveryThresholdMS: 6000, AttemptTimeoutMS: 12000, TotalBudgetMS: 30000, MinAttemptWindowMS: 6000}
}
func candidate(id int64, priority int) Candidate {
	return Candidate{AccountID: id, Priority: priority, HardEligible: true, CapacityAvailable: true}
}
func healthy(now time.Time) HealthSnapshot {
	return HealthSnapshot{State: HealthHealthy, UpdatedAtMS: now.UnixMilli()}
}
func testRuntime(t *testing.T) (*Runtime, *miniredis.Miniredis) {
	t.Helper()
	m := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: m.Addr(), MaxRetries: 0})
	t.Cleanup(func() { _ = client.Close() })
	return NewRuntime(NewRedisStore(client)), m
}
func shares(ds []Decision) map[int64]float64 {
	m := map[int64]float64{}
	for _, d := range ds {
		m[d.AccountID] = d.EffectiveShare
	}
	return m
}
func closeTo(t *testing.T, a, b float64) {
	t.Helper()
	if math.Abs(a-b) > 1e-8 {
		t.Fatalf("got %v want %v", a, b)
	}
}

func TestPolicyValidationAndSpecificProfiles(t *testing.T) {
	p := Policy{Model: "m", Profiles: []LatencyProfile{profile()}}
	if e := ValidatePolicy(p); e != nil {
		t.Fatal(e)
	}
	variants := []LatencyProfile{profile(), profile(), profile(), profile(), profile()}
	variants[0].HealthThresholdMS = 0
	variants[1].RecoveryThresholdMS = 8000
	variants[2].AttemptTimeoutMS = 8000
	variants[3].TotalBudgetMS = 11000
	variants[4].MinAttemptWindowMS = 13000
	for _, v := range variants {
		p.Profiles = []LatencyProfile{v}
		if ValidatePolicy(p) == nil {
			t.Errorf("invalid profile accepted: %+v", v)
		}
	}
	if e := ValidatePolicy(Policy{Model: "m", Accounts: []AccountRule{{AccountID: 1, Weight: 0}}}); e != nil {
		t.Fatal(e)
	}
	base := profile()
	special := profile()
	special.Name = "thinking"
	special.Reasoning = "high"
	special.Transport = "ws"
	special.ContextMinTokens = 8192
	p.Profiles = []LatencyProfile{base, special}
	v, ok := ResolveProfileForTransport(p, "high", 16000, "ws")
	if !ok || v.Name != "thinking" {
		t.Fatal(v, ok)
	}
	v, _ = ResolveProfileForTransport(p, "high", 16000, "http")
	if v.Name != "chat" {
		t.Fatal(v)
	}
	if HealthRedisKey(1, "m", base, "high", "8k_32k", "ws") == HealthRedisKey(1, "m", base, "low", "8k_32k", "ws") {
		t.Fatal("reasoning health keys collide")
	}
	if HealthRedisKey(1, "m", base, "high", "8k_32k", "ws") == HealthRedisKey(1, "m", base, "high", "8k_32k", "http") {
		t.Fatal("transport health keys collide")
	}
}
func TestPriorityWeightsModesAndCapacity(t *testing.T) {
	req := SelectionRequest{Policy: Policy{Model: "m", Accounts: []AccountRule{{AccountID: 1, Weight: 7}, {AccountID: 2, Weight: 3}, {AccountID: 3, Weight: 1000}}}, Candidates: []Candidate{candidate(1, 0), candidate(2, 0), candidate(3, 10)}, Now: time.Now()}
	ds, e := Evaluate(req, nil)
	if e != nil {
		t.Fatal(e)
	}
	m := shares(ds)
	closeTo(t, m[1], .7)
	closeTo(t, m[2], .3)
	if m[3] != 0 {
		t.Fatal("lower priority received healthy traffic")
	}
	req.Policy.Accounts[0].Weight = 0
	ds, _ = Evaluate(req, nil)
	if len(ds) != 1 || ds[0].AccountID != 2 {
		t.Fatal(ds)
	}
	req.Policy.Accounts[0].Weight = 7
	req.Candidates[0].CapacityAvailable = false
	req.Candidates[1].CapacityAvailable = false
	ds, _ = Evaluate(req, nil)
	if len(ds) != 1 || ds[0].AccountID != 3 || ds[0].Reason != "capacity_overflow" {
		t.Fatal(ds)
	}
	req.Policy.Overflow = OverflowWait
	if _, e = Evaluate(req, nil); !errors.Is(e, ErrCapacity) {
		t.Fatal(e)
	}
	req.Policy.Overflow = OverflowImmediate
	req.Candidates[0].CapacityAvailable = true
	req.Candidates[1].CapacityAvailable = true
	req.Policy.Mode = ModePin
	req.Policy.PinAccountID = 3
	ds, _ = Evaluate(req, nil)
	if len(ds) != 1 || ds[0].AccountID != 3 {
		t.Fatal(ds)
	}
	req.Candidates[2].HardEligible = false
	if _, e = Evaluate(req, nil); !errors.Is(e, ErrNoCandidate) {
		t.Fatal(e)
	}
	req.Policy.PinFallback = true
	ds, _ = Evaluate(req, nil)
	if len(ds) != 2 {
		t.Fatal(ds)
	}
	closeTo(t, shares(ds)[1], .7)
	req.Policy.Mode = ModeFillFirst
	req.Policy.Accounts[0].FillOrder = 2
	req.Policy.Accounts[1].FillOrder = 1
	ds, _ = Evaluate(req, nil)
	if len(ds) != 1 || ds[0].AccountID != 2 {
		t.Fatal(ds)
	}
	req.Attempted = map[int64]bool{2: true}
	ds, _ = Evaluate(req, nil)
	if len(ds) != 1 || ds[0].AccountID != 1 {
		t.Fatal(ds)
	}
	p := 10
	req.MinPriority = &p
	if _, e = Evaluate(req, nil); !errors.Is(e, ErrNoCandidate) {
		t.Fatal("request escalated backwards", e)
	}
}
func TestRecoveryShareUsesConfiguredTarget(t *testing.T) {
	now := time.Now()
	req := SelectionRequest{Policy: Policy{Model: "m", Accounts: []AccountRule{{AccountID: 1, Weight: 1}, {AccountID: 2, Weight: 99}}}, Candidates: []Candidate{candidate(1, 0), candidate(2, 0), candidate(3, 10)}, Profile: profile(), Now: now}
	states := map[int64]HealthSnapshot{1: {State: HealthRecovering, RecoveryStage: 0, UpdatedAtMS: now.UnixMilli()}, 2: healthy(now), 3: healthy(now)}
	ds, e := Evaluate(req, states)
	if e != nil {
		t.Fatal(e)
	}
	m := shares(ds)
	closeTo(t, m[1], .001)
	closeTo(t, m[2], .999)
	req.Candidates = req.Candidates[:1]
	states[1] = HealthSnapshot{State: HealthRecovering, RecoveryStage: 0, UpdatedAtMS: now.UnixMilli()}
	ds, e = Evaluate(req, states)
	if e != nil {
		t.Fatal(e)
	}
	closeTo(t, ds[0].EffectiveShare, 1)
	if ds[0].Reason != "degraded_best_effort" {
		t.Fatal(ds)
	}
}

func TestRecoveryDenominatorIncludesTemporarilyDegradedPeer(t *testing.T) {
	now := time.Now()
	req := SelectionRequest{
		Policy:     Policy{Model: "m", Accounts: []AccountRule{{AccountID: 1, Weight: 1}, {AccountID: 2, Weight: 99}, {AccountID: 3, Weight: 1}}},
		Candidates: []Candidate{candidate(1, 0), candidate(2, 0), candidate(3, 10)},
		Profile:    profile(), Now: now,
	}
	states := map[int64]HealthSnapshot{
		1: {State: HealthRecovering, RecoveryStage: 0, UpdatedAtMS: now.UnixMilli()},
		2: {State: HealthDegraded, UpdatedAtMS: now.UnixMilli(), CooldownUntilMS: now.Add(time.Minute).UnixMilli()},
		3: healthy(now),
	}
	ds, err := Evaluate(req, states)
	if err != nil {
		t.Fatal(err)
	}
	m := shares(ds)
	closeTo(t, m[1], .001)
	closeTo(t, m[3], .999)
}

func TestUnknownProbeCompetesWithDegradedPool(t *testing.T) {
	now := time.Now()
	req := SelectionRequest{
		Policy:     Policy{Model: "m", Accounts: []AccountRule{{AccountID: 1, Weight: 1}, {AccountID: 2, Weight: 1}}},
		Candidates: []Candidate{candidate(1, 0), candidate(2, 10)},
		Profile:    profile(), Now: now,
	}
	states := map[int64]HealthSnapshot{
		1: {State: HealthUnknown, UpdatedAtMS: now.UnixMilli()},
		2: {State: HealthDegraded, UpdatedAtMS: now.UnixMilli(), CooldownUntilMS: now.Add(time.Minute).UnixMilli()},
	}
	ds, err := Evaluate(req, states)
	if err != nil || len(ds) != 2 {
		t.Fatal(ds, err)
	}
	m := shares(ds)
	closeTo(t, m[1], .1)
	closeTo(t, m[2], .9)
	for _, d := range ds {
		if d.AccountID == 1 && (!d.Probe || d.Reason != "unknown_probe") {
			t.Fatal(ds)
		}
	}
}

func TestStrictPriorityAllowsSameTierRecoveryProbeOnly(t *testing.T) {
	now := time.Now()
	req := SelectionRequest{
		Policy:     Policy{Model: "strict-probe", AllDegraded: AllDegradedStrictPriority, Accounts: []AccountRule{{AccountID: 1, Weight: 9}, {AccountID: 2, Weight: 1}}},
		Candidates: []Candidate{candidate(1, 0), candidate(2, 0), candidate(3, 10)}, Profile: profile(), Now: now,
	}
	states := map[int64]HealthSnapshot{
		1: {State: HealthDegraded, UpdatedAtMS: now.UnixMilli(), CooldownUntilMS: now.Add(time.Minute).UnixMilli()},
		2: {State: HealthHalfOpen, UpdatedAtMS: now.UnixMilli()},
		3: {State: HealthUnknown, UpdatedAtMS: now.UnixMilli()},
	}
	ds, err := Evaluate(req, states)
	if err != nil || len(ds) != 2 {
		t.Fatal(ds, err)
	}
	m := shares(ds)
	closeTo(t, m[1], .9)
	closeTo(t, m[2], .1)
	for _, d := range ds {
		if d.AccountID == 3 {
			t.Fatal("strict priority crossed into a lower-tier probe", ds)
		}
	}
}

func TestHealthCooldownAndRecovery(t *testing.T) {
	p := profile()
	now := time.Now()
	var s HealthSnapshot
	for i, ms := range []int64{1000, 10000, 10000, 1000, 10000} {
		s = AdvanceHealth(s, Observation{Profile: p, At: now.Add(time.Duration(i) * time.Second), TTFT: time.Duration(ms) * time.Millisecond, HasSemanticOutput: true, Completed: true})
	}
	if s.State != HealthDegraded {
		t.Fatal(s)
	}
	before := s
	s = AdvanceHealth(s, Observation{Profile: p, At: now.Add(5 * time.Second), Excluded: true, AttributableFailure: true})
	if len(s.Samples) != len(before.Samples) || s.CooldownUntilMS != before.CooldownUntilMS {
		t.Fatal("excluded cancellation changed health")
	}
	start := time.UnixMilli(s.CooldownUntilMS).Add(time.Millisecond)
	for i := 0; i < 3; i++ {
		s = AdvanceHealth(s, Observation{Profile: p, At: start.Add(time.Duration(i) * time.Second), TTFT: time.Second, HasSemanticOutput: true, Completed: true})
	}
	if s.State != HealthRecovering || s.RecoveryStage != 0 {
		t.Fatal(s)
	}
	for stage := 1; stage <= 2; stage++ {
		start = time.UnixMilli(s.ChangedAtMS).Add(31 * time.Second)
		for i := 0; i < 3; i++ {
			s = AdvanceHealth(s, Observation{Profile: p, At: start.Add(time.Duration(i) * time.Second), TTFT: time.Second, HasSemanticOutput: true, Completed: true})
		}
		if stage == 1 && s.RecoveryStage != 1 {
			t.Fatal(s)
		}
	}
	if s.State != HealthHealthy {
		t.Fatal(s)
	}
	if FreshHealth(s, time.UnixMilli(s.UpdatedAtMS).Add(121*time.Second), p).State != HealthUnknown {
		t.Fatal("stale healthy account not unknown")
	}
}
func TestAllSlowCensoredEvidence(t *testing.T) {
	p := profile()
	now := time.Now()
	makeState := func(ms int64, timeout bool) HealthSnapshot {
		s := HealthSnapshot{State: HealthDegraded, CooldownUntilMS: now.Add(time.Minute).UnixMilli(), UpdatedAtMS: now.UnixMilli()}
		for i := 0; i < 5; i++ {
			s.Samples = append(s.Samples, HealthSample{AtMS: now.UnixMilli(), TTFTMS: ms, Timeout: timeout, HasSemanticOutput: !timeout})
		}
		return s
	}
	short := makeState(1000, true)
	long := makeState(11000, true)
	a, _ := ConservativeWaitScore(short, p, now)
	b, _ := ConservativeWaitScore(long, p, now)
	closeTo(t, a, b)
	req := SelectionRequest{Policy: Policy{Model: "m"}, Candidates: []Candidate{candidate(1, 0), candidate(2, 10)}, Profile: p, Now: now}
	states := map[int64]HealthSnapshot{1: short, 2: makeState(9000, false)}
	ds, e := Evaluate(req, states)
	if e != nil || len(ds) != 1 || ds[0].AccountID != 2 {
		t.Fatal(ds, e)
	}
	states[1] = makeState(10000, false)
	ds, _ = Evaluate(req, states)
	if len(ds) != 1 || ds[0].AccountID != 1 {
		t.Fatal("priority tolerance failed", ds)
	}
	req.Policy.AllDegraded = AllDegradedFailFast
	if _, e = Evaluate(req, states); !errors.Is(e, ErrNoCandidate) {
		t.Fatal(e)
	}
	req.Policy.AllDegraded = AllDegradedBestEffort
	states[1] = HealthSnapshot{State: HealthOpen, CooldownUntilMS: now.Add(time.Hour).UnixMilli()}
	states[2] = states[1]
	if _, e = Evaluate(req, states); !errors.Is(e, ErrNoCandidate) {
		t.Fatal("open accounts bypassed", e)
	}
}
func TestLedgerGlobalTierDeadlineAndCommit(t *testing.T) {
	now := time.Now()
	p := profile()
	l := NewAttemptLedger(DefaultRetryPolicy(), p, now, time.Time{})
	if e := l.BeginAttempt(1, 0, now, true); e != nil {
		t.Fatal(e)
	}
	if e := l.BeginAttempt(1, 0, now, true); !errors.Is(e, ErrAttemptBudget) {
		t.Fatal(e)
	}
	if e := l.BeginAttempt(2, 0, now.Add(time.Second), true); e != nil {
		t.Fatal(e)
	}
	if e := l.BeginAttempt(3, 0, now.Add(2*time.Second), true); !errors.Is(e, ErrAttemptBudget) {
		t.Fatal(e)
	}
	if e := l.BeginAttempt(3, 10, now.Add(2*time.Second), true); e != nil {
		t.Fatal(e)
	}
	if e := l.BeginAttempt(4, 20, now.Add(3*time.Second), true); !errors.Is(e, ErrAttemptBudget) {
		t.Fatal(e)
	}
	l = NewAttemptLedger(DefaultRetryPolicy(), p, now, time.Time{})
	_ = l.BeginAttempt(1, 0, now, true)
	l.MarkFirstOutputTimeout()
	_ = l.BeginAttempt(2, 0, now.Add(12*time.Second), true)
	if e := l.BeginAttempt(3, 10, now.Add(13*time.Second), true); !errors.Is(e, ErrAttemptBudget) {
		t.Fatal(e)
	}
	l = NewAttemptLedger(DefaultRetryPolicy(), p, now, time.Time{})
	_ = l.BeginAttempt(1, 0, now, true)
	if e := l.BeginAttempt(2, 0, now.Add(26*time.Second), true); !errors.Is(e, ErrDeadline) {
		t.Fatal(e)
	}
	if e := l.BeginAttempt(2, 0, now.Add(time.Second), false); !errors.Is(e, ErrUnsafeReplay) {
		t.Fatal(e)
	}
	l.MarkSemanticCommit()
	if e := l.BeginAttempt(2, 0, now, true); !errors.Is(e, ErrCommitted) {
		t.Fatal(e)
	}
	l = NewAttemptLedger(DefaultRetryPolicy(), p, now, now.Add(8*time.Second))
	_ = l.BeginAttempt(1, 0, now, true)
	if got := l.AttemptWindow(now, true); got != 8*time.Second {
		t.Fatal("invalid fallback reserve", got)
	}
	r := DefaultRetryPolicy()
	r.Mode = "exhaust_same_tier"
	l = NewAttemptLedger(r, p, now, time.Time{})
	for i := int64(1); i <= 3; i++ {
		if e := l.BeginAttempt(i, 0, now, true); e != nil {
			t.Fatal(e)
		}
	}
}
func TestRedisSWRRIsSharedAndReservationsRefund(t *testing.T) {
	rt, m := testRuntime(t)
	rt2 := NewRuntime(NewRedisStore(redis.NewClient(&redis.Options{Addr: m.Addr()})))
	ctx := context.Background()
	req := SelectionRequest{Policy: Policy{Model: "m", Accounts: []AccountRule{{AccountID: 1, Weight: 7}, {AccountID: 2, Weight: 3}}}, Candidates: []Candidate{candidate(1, 0), candidate(2, 0)}}
	first, e := rt.Select(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	if e = rt.ReleaseSelection(ctx, first); e != nil {
		t.Fatal(e)
	}
	again, e := rt2.Select(ctx, req)
	if e != nil || first.AccountID != again.AccountID {
		t.Fatal("rollback changed next allocation", first, again, e)
	}
	_ = rt2.ReleaseSelection(ctx, again)
	counts := map[int64]int{}
	for i := 0; i < 100; i++ {
		runtime := rt
		if i%2 == 1 {
			runtime = rt2
		}
		d, e := runtime.Select(ctx, req)
		if e != nil {
			t.Fatal(e)
		}
		if e = runtime.CommitSelection(ctx, d); e != nil {
			t.Fatal(e)
		}
		counts[d.AccountID]++
	}
	if counts[1] != 70 || counts[2] != 30 {
		t.Fatal(counts)
	}
	before := m.Dump()
	if _, e = rt.Preview(ctx, req); e != nil {
		t.Fatal(e)
	}
	if after := m.Dump(); before != after {
		t.Fatal("preview mutated shared state")
	}
}
func TestRedisConcurrentSWRRAndNoLocalFallback(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()
	req := SelectionRequest{Policy: Policy{Model: "concurrent"}, Candidates: []Candidate{candidate(1, 0), candidate(2, 0)}}
	counts := map[int64]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, e := rt.Select(ctx, req)
			if e != nil {
				t.Error(e)
				return
			}
			if e = rt.CommitSelection(ctx, d); e != nil {
				t.Error(e)
				return
			}
			mu.Lock()
			counts[d.AccountID]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if counts[1] != 10 || counts[2] != 10 {
		t.Fatal(counts)
	}
	if _, e := NewRuntime(NewRedisStore(nil)).Select(ctx, req); !errors.Is(e, ErrSharedState) {
		t.Fatal("missing Redis used local fallback", e)
	}
}
func TestSharedRetryBudgetCountsActualDispatch(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()
	p := NormalizePolicy(Policy{Model: "budget"})
	for i := 0; i < 2; i++ {
		b, e := rt.AcquireDispatchBudget(ctx, p, "logical", true)
		if e != nil {
			t.Fatal(e)
		}
		if e = rt.CommitDispatchBudget(ctx, b); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := rt.AcquireDispatchBudget(ctx, p, "logical", true); !errors.Is(e, ErrRetryBudget) {
		t.Fatal(e)
	}
	for i := 0; i < 10; i++ {
		b, e := rt.AcquireDispatchBudget(ctx, p, string(rune('a'+i)), false)
		if e != nil {
			t.Fatal(e)
		}
		_ = rt.CommitDispatchBudget(ctx, b)
	}
	b, e := rt.AcquireDispatchBudget(ctx, p, "logical", true)
	if e != nil {
		t.Fatal(e)
	}
	if e = rt.RefundDispatchBudget(ctx, b); e != nil {
		t.Fatal(e)
	}
	b, e = rt.AcquireDispatchBudget(ctx, p, "logical", true)
	if e != nil {
		t.Fatal(e)
	}
	_ = rt.CommitDispatchBudget(ctx, b)
	for i := 0; i < 10; i++ {
		b, e := rt.AcquireDispatchBudget(ctx, p, "a", false)
		if e != nil {
			t.Fatal(e)
		}
		_ = rt.CommitDispatchBudget(ctx, b)
	}
	if _, e = rt.AcquireDispatchBudget(ctx, p, "logical", true); !errors.Is(e, ErrRetryBudget) {
		t.Fatal("duplicate logical initial minted retry tokens", e)
	}
}
func TestUnknownProbeAndRedisHealth(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()
	p := profile()
	req := SelectionRequest{Policy: Policy{Model: "m"}, Candidates: []Candidate{candidate(1, 0), candidate(2, 0)}, Profile: p}
	d, e := rt.Select(ctx, req)
	if e != nil || !d.Probe {
		t.Fatal(d, e)
	}
	d2, e := rt.Select(ctx, req)
	if e != nil || d2.AccountID == d.AccountID {
		t.Fatal("all-unknown pool should admit one probe per account", d, d2, e)
	}
	if _, e = rt.Select(ctx, req); !errors.Is(e, ErrCapacity) {
		t.Fatal("account probe concurrency exceeded one", e)
	}
	if e = rt.ReleaseSelection(ctx, d2); e != nil {
		t.Fatal(e)
	}
	if e = rt.ReleaseSelection(ctx, d); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 5; i++ {
		if e = rt.Observe(ctx, Observation{AccountID: 1, Model: "m", Profile: p, At: time.Now(), TTFT: time.Second, HasSemanticOutput: true, Completed: true}); e != nil {
			t.Fatal(e)
		}
	}
	states, e := rt.store.Snapshots(ctx, req)
	if e != nil || states[1].State != HealthHealthy {
		t.Fatal(states, e)
	}
	ds, e := rt.Preview(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	m := shares(ds)
	closeTo(t, m[2], .05)
	closeTo(t, m[1], .95)
}

func TestBusyHighTierProbeOverflowsToLowerTier(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()
	req := SelectionRequest{
		Policy: Policy{Model: "probe-overflow"}, Profile: profile(),
		Candidates: []Candidate{candidate(1, 0), candidate(2, 10)},
	}
	first, err := rt.Select(ctx, req)
	if err != nil || first.AccountID != 1 || !first.Probe {
		t.Fatal(first, err)
	}
	second, err := rt.Select(ctx, req)
	if err != nil || second.AccountID != 2 {
		t.Fatal(second, err)
	}
	if err = rt.ReleaseSelection(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err = rt.ReleaseSelection(ctx, second); err != nil {
		t.Fatal(err)
	}
}

func TestBusyProbeHonorsOverflowWait(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()
	req := SelectionRequest{
		Policy: Policy{Model: "probe-overflow-wait", Overflow: OverflowWait}, Profile: profile(),
		Candidates: []Candidate{candidate(1, 0), candidate(2, 10)},
	}
	first, err := rt.Select(ctx, req)
	if err != nil || first.AccountID != 1 || !first.Probe {
		t.Fatal(first, err)
	}
	defer func() { require.NoError(t, rt.ReleaseSelection(ctx, first)) }()
	if _, err = rt.Select(ctx, req); !errors.Is(err, ErrCapacity) {
		t.Fatal("busy probe crossed an overflow-wait boundary", err)
	}
}

func TestBusyProbeTriesSameTierBeforeOverflowWait(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()
	req := SelectionRequest{
		Policy: Policy{Model: "probe-same-tier-wait", Overflow: OverflowWait}, Profile: profile(),
		Candidates: []Candidate{candidate(1, 0), candidate(2, 0), candidate(3, 10)},
	}
	first, err := rt.Select(ctx, req)
	if err != nil || first.AccountID != 1 || !first.Probe {
		t.Fatal(first, err)
	}
	defer func() { require.NoError(t, rt.ReleaseSelection(ctx, first)) }()
	second, err := rt.Select(ctx, req)
	if err != nil || second.AccountID != 2 || !second.Probe {
		t.Fatal(second, err)
	}
	defer func() { require.NoError(t, rt.ReleaseSelection(ctx, second)) }()
	if _, err = rt.Select(ctx, req); !errors.Is(err, ErrCapacity) {
		t.Fatal("busy same-tier probes crossed the wait boundary", err)
	}
}

func TestRecoveringSelectionUsesProbeLease(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()
	req := SelectionRequest{Policy: Policy{Model: "recovering-probe"}, Profile: profile(), Candidates: []Candidate{candidate(1, 0)}}
	now := time.Now()
	state := HealthSnapshot{State: HealthRecovering, RecoveryStage: 0, UpdatedAtMS: now.UnixMilli()}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = rt.store.client.Set(ctx, HealthRedisKey(1, req.Policy.Model, req.Profile, "", "", ""), raw, 0).Err(); err != nil {
		t.Fatal(err)
	}
	d, err := rt.Select(ctx, req)
	if err != nil || d.AccountID != 1 || !d.Probe {
		t.Fatal(d, err)
	}
	if err = rt.ReleaseSelection(ctx, d); err != nil {
		t.Fatal(err)
	}
}

func TestCanRetryIsReadOnlyAndNoProfileDoesNotThrottle(t *testing.T) {
	rt, m := testRuntime(t)
	ctx := context.Background()
	p := Policy{Model: "readonly"}
	before := m.Dump()
	ok, e := rt.CanRetry(ctx, p)
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	if before != m.Dump() {
		t.Fatal("CanRetry created state")
	}
	for i := 0; i < 2; i++ {
		b, e := rt.AcquireDispatchBudget(ctx, p, "req", true)
		if e != nil {
			t.Fatal(e)
		}
		if e = rt.CommitDispatchBudget(ctx, b); e != nil {
			t.Fatal(e)
		}
	}
	before = m.Dump()
	ok, e = rt.CanRetry(ctx, p)
	if e != nil || ok {
		t.Fatal(ok, e)
	}
	if before != m.Dump() {
		t.Fatal("CanRetry replenished credit")
	}
	req := SelectionRequest{Policy: Policy{Model: "observe"}, Candidates: []Candidate{candidate(1, 0)}}
	a, e := rt.Select(ctx, req)
	if e != nil || a.Probe {
		t.Fatal(a, e)
	}
	b, e := rt.Select(ctx, req)
	if e != nil || b.Probe {
		t.Fatal(b, e)
	}
	_ = rt.ReleaseSelection(ctx, a)
	_ = rt.ReleaseSelection(ctx, b)
	base := profile()
	changed := base
	changed.HealthThresholdMS = 9000
	if HealthRedisKey(1, "m", base, "", "", "") == HealthRedisKey(1, "m", changed, "", "", "") {
		t.Fatal("threshold overrides share health state")
	}
	fallback := profile()
	p.Profiles = []LatencyProfile{fallback}
	if v, ok := ResolveProfile(p, "", -1); !ok || v.Name != "chat" {
		t.Fatal("unknown context lost base model profile", v, ok)
	}
}

func TestSelectionExpiryCompensatesAndCannotCommit(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()
	req := SelectionRequest{Policy: Policy{Model: "expiry"}, Candidates: []Candidate{candidate(1, 0), candidate(2, 0)}, Now: time.Now().Add(-time.Minute)}
	old, e := rt.Select(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	req.Now = time.Now()
	next, e := rt.Select(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	if old.AccountID != next.AccountID {
		t.Fatal("abandoned selection was not compensated", old, next)
	}
	if e = rt.CommitSelection(ctx, old); !errors.Is(e, ErrSharedState) {
		t.Fatal("expired selection was committed", e)
	}
	if e = rt.CommitSelection(ctx, next); e != nil {
		t.Fatal(e)
	}
}

func TestPinAndRetryDoNotConsumeOrdinaryAllocation(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()
	req := SelectionRequest{Policy: Policy{Model: "separate"}, Candidates: []Candidate{candidate(1, 0), candidate(2, 0)}}
	first, e := rt.Select(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	if e = rt.CommitSelection(ctx, first); e != nil {
		t.Fatal(e)
	}
	pin := req
	pin.Policy.Mode = ModePin
	pin.Policy.PinAccountID = 2
	for i := 0; i < 3; i++ {
		d, e := rt.Select(ctx, pin)
		if e != nil || d.AccountID != 2 {
			t.Fatal(d, e)
		}
		if e = rt.CommitSelection(ctx, d); e != nil {
			t.Fatal(e)
		}
	}
	retry := req
	retry.Retry = true
	d, e := rt.Select(ctx, retry)
	if e != nil {
		t.Fatal(e)
	}
	_ = rt.CommitSelection(ctx, d)
	next, e := rt.Select(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	if next.AccountID == first.AccountID {
		t.Fatal("pin/retry reset normal SWRR", first, next)
	}
	_ = rt.CommitSelection(ctx, next)
}

func TestFailureDomainExclusionAndFreshRecoveryEvidence(t *testing.T) {
	now := time.Now()
	l := NewAttemptLedger(DefaultRetryPolicy(), profile(), now, time.Time{})
	l.BlockFailureDomain("credential-family-1")
	a, b, c := candidate(1, 0), candidate(2, 0), candidate(3, 10)
	a.FailureDomain = "credential-family-1"
	b.FailureDomain = a.FailureDomain
	c.FailureDomain = "independent"
	req := SelectionRequest{Policy: Policy{Model: "m"}, Candidates: []Candidate{a, b, c}, ExcludedFailureDomains: l.Snapshot().BlockedFailureDomains}
	ds, e := Evaluate(req, nil)
	if e != nil || len(ds) != 1 || ds[0].AccountID != 3 {
		t.Fatal("shared 429 domain retried", ds, e)
	}
	copied := l.Snapshot()
	delete(copied.BlockedFailureDomains, "credential-family-1")
	if !l.Snapshot().BlockedFailureDomains["credential-family-1"] {
		t.Fatal("snapshot exposed mutable ledger state")
	}
	old := HealthSnapshot{State: HealthRecovering, GoodStreak: 2, UpdatedAtMS: now.Add(-121 * time.Second).UnixMilli(), ChangedAtMS: now.Add(-2 * time.Minute).UnixMilli()}
	next := AdvanceHealth(old, Observation{Profile: profile(), At: now, TTFT: time.Second, HasSemanticOutput: true, Completed: true})
	if next.GoodStreak != 1 || next.RecoveryStage != 0 {
		t.Fatal("stale evidence advanced recovery", next)
	}
}

func TestHealthSeparatesSlowAndFailureWindows(t *testing.T) {
	now := time.Now()
	p := profile()
	var s HealthSnapshot
	emit := func(ms int64, failed bool) {
		now = now.Add(time.Second)
		s = AdvanceHealth(s, Observation{Profile: p, At: now, TTFT: time.Duration(ms) * time.Millisecond, HasSemanticOutput: !failed, Completed: !failed, AttributableFailure: failed})
	}
	emit(9000, false)
	emit(9000, false)
	emit(0, true)
	if s.State == HealthDegraded || s.State == HealthOpen {
		t.Fatal("two slow plus one failure incorrectly combined", s)
	}
	emit(9000, false)
	if s.State != HealthDegraded {
		t.Fatal("three slow within fewer than five comparables did not degrade", s)
	}
	s = HealthSnapshot{}
	for _, failed := range []bool{true, false, true, false, true} {
		emit(1000, failed)
	}
	if s.State != HealthOpen {
		t.Fatal("three nonconsecutive failures in five did not open", s)
	}
	req := SelectionRequest{Policy: Policy{Model: "m"}, Profile: p, Now: now, Candidates: []Candidate{candidate(1, 0)}}
	if _, e := Evaluate(req, map[int64]HealthSnapshot{1: s}); !errors.Is(e, ErrNoCandidate) {
		t.Fatal("all-slow bypassed failure breaker", e)
	}
}

func TestRecoveryRequiresSuccessfulTerminalAndBacksOff(t *testing.T) {
	p := profile()
	now := time.Now()
	s := HealthSnapshot{State: HealthHalfOpen, ChangedAtMS: now.UnixMilli()}
	for i := 0; i < 3; i++ {
		s = AdvanceHealth(s, Observation{Profile: p, At: now, TTFT: time.Second, HasSemanticOutput: true})
	}
	if s.GoodStreak != 0 || s.State != HealthHalfOpen {
		t.Fatal("progress events promoted recovery", s)
	}
	s = AdvanceHealth(s, Observation{Profile: p, At: now, TTFT: time.Second, HasSemanticOutput: true, Completed: false, AttributableFailure: true})
	if s.State != HealthOpen || s.CooldownUntilMS-now.UnixMilli() != 60000 || s.RecoveryFailures != 1 {
		t.Fatal(s)
	}
	for _, wait := range []int64{120000, 240000, 300000, 300000} {
		now = time.UnixMilli(s.CooldownUntilMS).Add(time.Millisecond)
		s = AdvanceHealth(s, Observation{Profile: p, At: now, AttributableFailure: true})
		if s.CooldownUntilMS-now.UnixMilli() != wait {
			t.Fatal("unexpected recovery backoff", s, wait)
		}
	}
	now = time.UnixMilli(s.CooldownUntilMS).Add(time.Millisecond)
	retryAfter := now.Add(10 * time.Minute)
	s = AdvanceHealth(s, Observation{Profile: p, At: now, AttributableFailure: true, RetryAfter: retryAfter})
	if s.CooldownUntilMS != retryAfter.UnixMilli() {
		t.Fatal("provider Retry-After ignored", s)
	}
	scoreState := HealthSnapshot{}
	for i := 0; i < 5; i++ {
		scoreState.Samples = append(scoreState.Samples, HealthSample{AtMS: now.UnixMilli(), AttributableFailure: true})
	}
	if _, ok := ConservativeWaitScore(scoreState, p, now); ok {
		t.Fatal("ordinary failures fabricated TTFT samples")
	}
}

func TestObserveOnlyStillProtectsAgainstFailures(t *testing.T) {
	rt, _ := testRuntime(t)
	ctx := context.Background()
	now := time.Now()
	p := LatencyProfile{Name: "unconfigured"}
	for _, failed := range []bool{true, false, true, false, true} {
		if e := rt.Observe(ctx, Observation{AccountID: 1, Model: "unconfigured", Profile: p, At: now, Completed: !failed, AttributableFailure: failed}); e != nil {
			t.Fatal(e)
		}
		now = now.Add(time.Millisecond)
	}
	req := SelectionRequest{Policy: Policy{Model: "unconfigured"}, Candidates: []Candidate{candidate(1, 0)}, Profile: p, Now: now}
	states, e := rt.store.Snapshots(ctx, req)
	if e != nil || states[1].State != HealthOpen {
		t.Fatal("observe-only discarded error protection", states, e)
	}
	if _, e = rt.Preview(ctx, req); !errors.Is(e, ErrNoCandidate) {
		t.Fatal(e)
	}
	s := HealthSnapshot{}
	for i := 0; i < 5; i++ {
		s = AdvanceHealth(s, Observation{Profile: p, At: now, TTFT: time.Hour, HasSemanticOutput: true, Completed: true})
	}
	if s.State != HealthHealthy {
		t.Fatal("observe-only used TTFT threshold", s)
	}
}

func TestAdditionalFailureDomainsExcludeOnlyVerifiedSiblings(t *testing.T) {
	a, b, c := candidate(1, 0), candidate(2, 0), candidate(3, 0)
	a.FailureDomain = "family-a"
	b.FailureDomain = "family-b"
	c.FailureDomain = "family-c"
	a.FailureDomains = []string{"team-t:model-m"}
	b.FailureDomains = []string{"team-t:model-m"}
	c.FailureDomains = []string{"team-u:model-m"}
	ds, err := Evaluate(SelectionRequest{Policy: Policy{Model: "m"}, Candidates: []Candidate{a, b, c}, ExcludedFailureDomains: map[string]bool{"team-t:model-m": true}}, nil)
	if err != nil || len(ds) != 1 || ds[0].AccountID != 3 {
		t.Fatal("verified team exclusion did not preserve independent account", ds, err)
	}
}
