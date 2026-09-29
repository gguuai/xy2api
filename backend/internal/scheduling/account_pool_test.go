package scheduling

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func accountPoolRequest() SelectionRequest {
	return SelectionRequest{
		Policy: Policy{AccountPool: true, GroupID: 7, Model: "client-model", Version: 1, Enabled: true, Mode: ModeSWRR,
			Accounts: []AccountRule{{AccountID: 1, Weight: 7}, {AccountID: 2, Weight: 3}, {AccountID: 3, Weight: 1000}}},
		Candidates: []Candidate{candidate(1, 0), candidate(2, 0), candidate(3, 10)},
		Profile:    LatencyProfile{Name: AccountPoolProfileName, AttemptTimeoutMS: 120000, TotalBudgetMS: 240000, MinAttemptWindowMS: 1000},
		Now:        time.Now(),
	}
}

func TestAccountPoolAdmissionThenPriorityThenWeight(t *testing.T) {
	req := accountPoolRequest()
	ds, err := Evaluate(req, nil)
	require.NoError(t, err)
	require.Len(t, ds, 2)
	require.Equal(t, map[int64]float64{1: .7, 2: .3}, shares(ds))
	for _, d := range ds {
		require.Equal(t, HealthHealthy, d.HealthState)
		require.False(t, d.Probe, "new accounts must not inherit UNKNOWN throttling")
		require.NotNil(t, d.HealthFence)
	}

	for _, tc := range []struct {
		name    string
		change  func(*SelectionRequest)
		want    []int64
		wantErr error
	}{
		{"model unsupported at top", func(r *SelectionRequest) { r.Candidates[0].HardEligible = false; r.Candidates[1].HardEligible = false }, []int64{3}, nil},
		{"one model supporter at top", func(r *SelectionRequest) { r.Candidates[0].HardEligible = false }, []int64{2}, nil},
		{"weight zero is disabled", func(r *SelectionRequest) { r.Policy.Accounts[0].Weight = 0 }, []int64{2}, nil},
		{"all weights zero", func(r *SelectionRequest) {
			for i := range r.Policy.Accounts {
				r.Policy.Accounts[i].Weight = 0
			}
		}, nil, ErrNoCandidate},
		{"empty pool", func(r *SelectionRequest) { r.Candidates = nil }, nil, ErrNoCandidate},
		{"capacity skips busy peer", func(r *SelectionRequest) { r.Candidates[0].CapacityAvailable = false }, []int64{2}, nil},
		{"capacity immediately downgrades", func(r *SelectionRequest) {
			r.Candidates[0].CapacityAvailable = false
			r.Candidates[1].CapacityAvailable = false
			r.Policy.Overflow = OverflowWait
		}, []int64{3}, nil},
		{"all capacity busy", func(r *SelectionRequest) {
			for i := range r.Candidates {
				r.Candidates[i].CapacityAvailable = false
			}
		}, nil, ErrCapacity},
		{"attempted never reused", func(r *SelectionRequest) { r.Attempted = map[int64]bool{1: true} }, []int64{2}, nil},
		{"cannot upgrade retry tier", func(r *SelectionRequest) { n := 10; r.MinPriority = &n }, []int64{3}, nil},
		{"verified domain exclusion", func(r *SelectionRequest) {
			r.Candidates[0].FailureDomains = []string{"shared"}
			r.ExcludedFailureDomains = map[string]bool{"shared": true}
		}, []int64{2}, nil},
		{"protocol owner bound", func(r *SelectionRequest) {
			r.Policy.Mode = ModePin
			r.Policy.PinAccountID = 3
			r.Policy.PinFallback = false
		}, []int64{3}, nil},
		{"owner capacity cannot switch", func(r *SelectionRequest) {
			r.Policy.Mode = ModePin
			r.Policy.PinAccountID = 1
			r.Policy.PinFallback = false
			r.Candidates[0].CapacityAvailable = false
		}, nil, ErrCapacity},
		{"unsupported owner cannot switch", func(r *SelectionRequest) {
			r.Policy.Mode = ModePin
			r.Policy.PinAccountID = 1
			r.Policy.PinFallback = false
			r.Candidates[0].HardEligible = false
		}, nil, ErrNoCandidate},
		{"old fill first cannot bypass weights", func(r *SelectionRequest) { r.Policy.Mode = ModeFillFirst; r.Policy.Accounts[0].FillOrder = 99 }, []int64{1, 2}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := accountPoolRequest()
			tc.change(&r)
			ds, err := Evaluate(r, nil)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			ids := make([]int64, 0, len(ds))
			for _, d := range ds {
				ids = append(ids, d.AccountID)
			}
			require.Equal(t, tc.want, ids)
		})
	}
}

func TestAccountPoolSparseRulesAndAuthoritativePriority(t *testing.T) {
	r := accountPoolRequest()
	r.Policy.Accounts = nil
	ds, err := Evaluate(r, nil)
	require.NoError(t, err)
	require.Equal(t, map[int64]float64{1: .5, 2: .5}, shares(ds))
	p := -1
	r.Policy.Accounts = []AccountRule{{AccountID: 3, Priority: &p, Weight: 1}}
	ds, err = Evaluate(r, nil)
	require.NoError(t, err)
	require.Len(t, ds, 1)
	require.EqualValues(t, 3, ds[0].AccountID)
	require.Equal(t, -1, ds[0].Priority)
	require.Equal(t, 10, r.Candidates[2].Priority, "evaluation cannot rewrite source data")
}

func TestAccountPoolExhaustsEveryEligibleAccountBeforeLowerTier(t *testing.T) {
	r, _ := testRuntime(t)
	ctx := context.Background()
	req := accountPoolRequest()
	req.Policy.Accounts = nil
	req.Policy.Retry = RetryPolicy{Mode: "exhaust_same_tier", MaxAttempts: 5, MaxPerAccount: 1}
	req.Candidates = []Candidate{candidate(1, 0), candidate(2, 0), candidate(3, 0), candidate(4, 0), candidate(5, 10), candidate(6, -1)}
	// Skips caused by a hard eligibility gate are not physical attempts.
	req.Candidates[5].HardEligible = false
	req.Attempted = map[int64]bool{}
	for i := 0; i < 4; i++ {
		d, err := r.Select(ctx, req)
		require.NoError(t, err)
		require.Equal(t, 0, d.Priority, "lower tier selected before same-tier exhaustion")
		require.False(t, req.Attempted[d.AccountID])
		require.NoError(t, r.CommitSelection(ctx, d))
		req.Attempted[d.AccountID] = true
	}
	d, err := r.Select(ctx, req)
	require.NoError(t, err)
	require.EqualValues(t, 5, d.AccountID)
	require.Equal(t, 10, d.Priority)
	require.NoError(t, r.CommitSelection(ctx, d))
	req.Attempted[d.AccountID] = true
	_, err = r.Select(ctx, req)
	require.ErrorIs(t, err, ErrNoCandidate)
}

func TestAccountPoolRulesDoNotCreateCandidatesOrChangeTieOrder(t *testing.T) {
	r := accountPoolRequest()
	priority := -100
	r.Policy.Accounts = append(r.Policy.Accounts, AccountRule{AccountID: 999, Priority: &priority, Weight: 1000000})
	a, err := Evaluate(r, nil)
	require.NoError(t, err)
	r.Candidates[0], r.Candidates[1] = r.Candidates[1], r.Candidates[0]
	r.Policy.Accounts[0].FillOrder = 99
	b, err := Evaluate(r, nil)
	require.NoError(t, err)
	require.Equal(t, a, b)
	require.Equal(t, accountPoolAllocationKey(r, a), accountPoolAllocationKey(r, b))
}

type accountPoolRejectEvalClient struct{ redis.UniversalClient }

func (c accountPoolRejectEvalClient) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	cmd := redis.NewCmd(ctx)
	cmd.SetErr(errors.New("forced Redis allocation failure"))
	return cmd
}

func TestAccountPoolAtomicReservationErrorNeverReturnsAnAccount(t *testing.T) {
	r, m := testRuntime(t)
	r.store.client = accountPoolRejectEvalClient{r.store.client}
	d, err := r.Select(context.Background(), accountPoolRequest())
	require.ErrorIs(t, err, ErrSharedState)
	require.Zero(t, d.AccountID)
	require.Empty(t, m.Keys())
}

func TestAccountPoolIneligibleHealthCacheCannotPoisonEligiblePool(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*SelectionRequest)
	}{
		{"unsupported or paused", func(r *SelectionRequest) { r.Candidates[0].HardEligible = false }},
		{"weight zero", func(r *SelectionRequest) { r.Policy.Accounts[0].Weight = 0 }},
		{"capacity busy", func(r *SelectionRequest) { r.Candidates[0].CapacityAvailable = false }},
		{"already dispatched", func(r *SelectionRequest) { r.Attempted = map[int64]bool{1: true} }},
		{"failed domain", func(r *SelectionRequest) {
			r.Candidates[0].FailureDomain = "failed"
			r.ExcludedFailureDomains = map[string]bool{"failed": true}
		}},
		{"another protocol owner", func(r *SelectionRequest) {
			r.Policy.Mode = ModePin
			r.Policy.PinAccountID = 2
			r.Policy.PinFallback = false
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := testRuntime(t)
			ctx := context.Background()
			req := accountPoolRequest()
			key := HealthRedisKey(1, req.Policy.Model, req.Profile, "", "", "")
			require.NoError(t, r.store.client.HSet(ctx, key, "snapshot", "broken JSON").Err())
			tc.change(&req)
			_, err := r.Preview(ctx, req)
			require.NoError(t, err)
			x, err := r.InspectSelection(ctx, req)
			require.NoError(t, err)
			require.EqualValues(t, 2, x.Selected.AccountID)
			d, err := r.Select(ctx, req)
			require.NoError(t, err)
			require.EqualValues(t, 2, d.AccountID)
			require.NoError(t, r.ReleaseSelection(ctx, d))
		})
	}
}

func TestAccountPoolSWRRRatioAndTwoClientFairness(t *testing.T) {
	r1, m := testRuntime(t)
	c2 := redis.NewClient(&redis.Options{Addr: m.Addr(), MaxRetries: 0})
	t.Cleanup(func() { _ = c2.Close() })
	r2 := NewRuntime(NewRedisStore(c2))
	counts := map[int64]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, r := range []*Runtime{r1, r2} {
		wg.Add(1)
		go func(r *Runtime) {
			defer wg.Done()
			req := accountPoolRequest()
			for i := 0; i < 200; i++ {
				d, err := r.Select(context.Background(), req)
				if err != nil {
					errs <- err
					return
				}
				if err = r.CommitSelection(context.Background(), d); err != nil {
					errs <- err
					return
				}
				mu.Lock()
				counts[d.AccountID]++
				mu.Unlock()
			}
		}(r)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, map[int64]int{1: 280, 2: 120}, counts)
}

func TestAccountPoolModelSetsAndConfigChangesKeepFairness(t *testing.T) {
	r, _ := testRuntime(t)
	ctx := context.Background()
	counts := map[int64]int{}
	for i := 0; i < 100; i++ {
		req := accountPoolRequest()
		if i%2 == 1 {
			req.Policy.Model = "another-alias"
			req.Reasoning = "high"
			req.Transport = "ws"
			req.Profile.AttemptTimeoutMS = 90000
		}
		d, err := r.Select(ctx, req)
		require.NoError(t, err)
		require.NoError(t, r.CommitSelection(ctx, d))
		counts[d.AccountID]++
		// A model with a different support set cannot reset the shared pair.
		subset := accountPoolRequest()
		subset.Policy.Model = "only-first"
		subset.Candidates[1].HardEligible = false
		x, err := r.Select(ctx, subset)
		require.NoError(t, err)
		require.EqualValues(t, 1, x.AccountID)
		require.NoError(t, r.CommitSelection(ctx, x))
	}
	require.Equal(t, map[int64]int{1: 70, 2: 30}, counts)
	old := accountPoolRequest()
	a, err := r.Select(ctx, old)
	require.NoError(t, err)
	newReq := accountPoolRequest()
	newReq.Policy.Accounts[0].Weight = 1
	newReq.Policy.Accounts[1].Weight = 9
	b, err := r.Select(ctx, newReq)
	require.NoError(t, err)
	require.NotEqual(t, a.PoolKey, b.PoolKey)
	require.NoError(t, r.ReleaseSelection(ctx, a))
	require.NoError(t, r.CommitSelection(ctx, b))
	counts = map[int64]int{b.AccountID: 1}
	for i := 0; i < 9; i++ {
		d, err := r.Select(ctx, newReq)
		require.NoError(t, err)
		require.NoError(t, r.CommitSelection(ctx, d))
		counts[d.AccountID]++
	}
	require.Equal(t, map[int64]int{1: 1, 2: 9}, counts)
	opts, _ := Evaluate(newReq, nil)
	differentGroup := newReq
	differentGroup.Policy.GroupID++
	require.NotEqual(t, accountPoolAllocationKey(newReq, opts), accountPoolAllocationKey(differentGroup, opts))
}

func TestAccountPoolRefundAndReadOnlyExplainMatchActualSelection(t *testing.T) {
	r, m := testRuntime(t)
	ctx := context.Background()
	req := accountPoolRequest()
	for i := 0; i < 20; i++ {
		before := m.Dump()
		x, err := r.InspectSelection(ctx, req)
		require.NoError(t, err)
		require.Equal(t, before, m.Dump(), "explain mutated shared state")
		d, err := r.Select(ctx, req)
		require.NoError(t, err)
		require.Equal(t, x.Selected.AccountID, d.AccountID)
		require.NoError(t, r.ReleaseSelection(ctx, d))
		require.NoError(t, r.ReleaseSelection(ctx, d), "duplicate refund must be harmless")
		again, err := r.Select(ctx, req)
		require.NoError(t, err)
		require.Equal(t, d.AccountID, again.AccountID)
		require.NoError(t, r.CommitSelection(ctx, again))
	}
}

func TestAccountPoolSharedStateErrorsFailClosed(t *testing.T) {
	r, m := testRuntime(t)
	m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := r.Select(ctx, accountPoolRequest())
	require.ErrorIs(t, err, ErrSharedState)
	_, err = r.Preview(ctx, accountPoolRequest())
	require.ErrorIs(t, err, ErrSharedState)
	_, err = r.InspectSelection(ctx, accountPoolRequest())
	require.ErrorIs(t, err, ErrSharedState)
	var missing *Runtime
	_, err = missing.Select(ctx, accountPoolRequest())
	require.ErrorIs(t, err, ErrSharedState)
}

func TestAccountPoolColdStartAndUnavailableProbeDoNotBlockHealthyAccounts(t *testing.T) {
	r, _ := testRuntime(t)
	ctx := context.Background()
	req := accountPoolRequest()
	req.Policy.Accounts[0].Weight = 100
	req.Policy.Accounts[1].Weight = 1
	// A real open circuit, after its cooldown, needs one recovery attempt.
	state := HealthSnapshot{Generation: "generation", StageRevision: 1, State: HealthOpen, CooldownUntilMS: time.Now().Add(-time.Second).UnixMilli()}
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	key := HealthRedisKey(1, req.Policy.Model, req.Profile, "", "", "")
	require.NoError(t, r.store.client.HSet(ctx, key, "snapshot", raw).Err())
	first, err := r.Select(ctx, req)
	require.NoError(t, err)
	require.EqualValues(t, 1, first.AccountID)
	require.True(t, first.Probe)
	require.NotEmpty(t, first.ProbeToken)
	before := map[int64]bool{}
	req.Attempted = before
	x, err := r.InspectSelection(ctx, req)
	require.NoError(t, err)
	require.EqualValues(t, 2, x.Selected.AccountID)
	second, err := r.Select(ctx, req)
	require.NoError(t, err)
	require.EqualValues(t, 2, second.AccountID)
	require.False(t, second.Probe)
	require.Empty(t, before, "busy-probe reselection mutated caller's attempted set")
	require.NoError(t, r.ReleaseSelection(ctx, second))
	// Owner pin never falls through while its recovery probe is occupied.
	owner := req
	owner.Policy.Mode = ModePin
	owner.Policy.PinAccountID = 1
	owner.Policy.PinFallback = false
	_, err = r.Select(ctx, owner)
	require.ErrorIs(t, err, ErrCapacity)
	require.NoError(t, r.CommitSelection(ctx, first))
	fence, err := r.FreezeSelectedHealth(ctx, 1, req.Policy.Model, req.Profile, "", "", "", "", first)
	require.NoError(t, err)
	require.NoError(t, r.Observe(ctx, Observation{AccountID: 1, Model: req.Policy.Model, Profile: req.Profile, At: time.Now(), AttemptID: "probe-terminal", Fence: &fence, Completed: true}))
	states, err := r.store.Snapshots(ctx, req)
	require.NoError(t, err)
	require.Equal(t, HealthHealthy, states[1].State)
	require.NoError(t, r.ReleaseProbe(ctx, first.ProbeToken))
	third, err := r.Select(ctx, req)
	require.NoError(t, err)
	require.False(t, third.Probe)
	require.NoError(t, r.ReleaseSelection(ctx, third))
}

func TestAccountPoolHealthFailureRecoveryAndStaleFence(t *testing.T) {
	req := accountPoolRequest()
	now := req.Now
	initial := HealthSnapshot{Generation: "g", State: HealthHealthy}
	fence := HealthFence{Generation: "g", State: HealthHealthy}
	obs := Observation{Profile: req.Profile, At: now, Fence: &fence, FirstOutputTimeout: true}
	failed := AdvanceHealth(initial, obs)
	require.Equal(t, HealthOpen, failed.State)
	require.Equal(t, now.Add(30*time.Second).UnixMilli(), failed.CooldownUntilMS)
	require.EqualValues(t, 1, failed.StageRevision)
	// The ordinary success is not made 'slow' by the internal zero H/R fields.
	ok := Observation{Profile: req.Profile, At: now, Completed: true, HasSemanticOutput: true, TTFT: 100 * time.Second}
	require.Equal(t, HealthHealthy, AdvanceHealth(initial, ok).State)
	late := obs
	late.FirstOutputTimeout = false
	late.Completed = true
	late.At = now.Add(time.Second)
	require.Equal(t, HealthOpen, AdvanceHealth(failed, late).State)
	late.At = now.Add(31 * time.Second)
	require.Equal(t, HealthHalfOpen, AdvanceHealth(failed, late).State, "late pre-failure success cannot close recovery")
	probe := FreshHealth(failed, late.At, req.Profile)
	probeFence := HealthFence{Generation: probe.Generation, StageRevision: probe.StageRevision, State: probe.State}
	late.Fence = &probeFence
	recovered := AdvanceHealth(probe, late)
	require.Equal(t, HealthHealthy, recovered.State)
	require.Zero(t, recovered.CooldownUntilMS)
	obs.RetryAfter = now.Add(2 * time.Minute)
	require.Equal(t, obs.RetryAfter.UnixMilli(), AdvanceHealth(initial, obs).CooldownUntilMS)
	obs.Excluded = true
	require.Equal(t, initial, AdvanceHealth(initial, obs))
	next, _ := (&Runtime{}).NextRecovery(context.Background(), req)
	require.True(t, next.IsZero(), "requests must not wait on a cooling account")
}

func TestAccountPoolFreezeAndObservationKeepReceiptFences(t *testing.T) {
	r, _ := testRuntime(t)
	ctx := context.Background()
	req := accountPoolRequest()
	d, err := r.Select(ctx, req)
	require.NoError(t, err)
	nilFence := d
	nilFence.HealthFence = nil
	_, err = r.FreezeSelectedHealth(ctx, d.AccountID, req.Policy.Model, req.Profile, "", "", "", "", nilFence)
	require.ErrorIs(t, err, ErrHealthSelectionStale)
	fence, err := r.FreezeSelectedHealth(ctx, d.AccountID, req.Policy.Model, req.Profile, "", "", "", "", d)
	require.NoError(t, err)
	obs := Observation{AccountID: d.AccountID, Model: req.Policy.Model, Profile: req.Profile, At: time.Now(), AttemptID: "failed-once", Fence: &fence, FirstOutputTimeout: true}
	require.NoError(t, r.Observe(ctx, obs))
	s1, err := r.store.Snapshots(ctx, req)
	require.NoError(t, err)
	require.Equal(t, HealthOpen, s1[d.AccountID].State)
	obs.At = obs.At.Add(time.Minute)
	require.NoError(t, r.Observe(ctx, obs))
	s2, err := r.store.Snapshots(ctx, req)
	require.NoError(t, err)
	require.Equal(t, s1, s2, "duplicate completion changed cooldown")
	_, err = r.FreezeSelectedHealth(ctx, d.AccountID, req.Policy.Model, req.Profile, "", "", "", "", d)
	require.ErrorIs(t, err, ErrHealthSelectionStale)
	require.NoError(t, r.ReleaseSelection(ctx, d))
}

func TestAccountPoolHealthAndProbeKeysTrackActualCapability(t *testing.T) {
	a := accountPoolRequest()
	a.Candidates[0].HealthModel = "upstream-m"
	a.Candidates[0].HealthIdentity = "credential-owner"
	b := a
	b.Policy.Model = "other-alias"
	b.Policy.GroupID = 99
	b.Profile.AttemptTimeoutMS = 300000
	b.Reasoning = "high"
	b.ContextBucket = "32k"
	b.Transport = "ws"
	require.Equal(t, probePoolKey(a, 1), probePoolKey(b, 1))
	require.Equal(t, HealthRedisKey(1, "upstream-m", a.Profile, "", "", "", "credential-owner"), HealthRedisKey(1, "upstream-m", b.Profile, "high", "32k", "ws", "credential-owner"))
	require.NotEqual(t, HealthRedisKey(1, "upstream-m", a.Profile, "", "", "", "credential-owner"), HealthRedisKey(1, "upstream-m", a.Profile, "", "", "", "replacement"))
	require.NotEqual(t, HealthRedisKey(1, "upstream-m", a.Profile, "", "", ""), HealthRedisKey(1, "different-model", a.Profile, "", "", ""))
}

func TestAccountPoolFailureClassifierKeepsAuthoritativeScopeAndReplayStops(t *testing.T) {
	now := time.Now()
	a := FailureAdmission{AccountID: 1, Model: "m", Credential: "c", HealthIdentity: "i"}
	for _, e := range []FailureEvidence{{Trusted: true, Status: 503, ReplaySafe: true}, {FirstSemanticTimeout: true, ReplaySafe: true}, {NotSent: true, ReplaySafe: true}} {
		d := ClassifyAccountPoolFailure(e, a, now)
		require.Equal(t, "cooldown", d.Effect)
		require.Equal(t, a.ModelKey(), d.Key)
		require.Equal(t, now.Add(accountPoolCooldown), d.Until)
	}
	for _, e := range []FailureEvidence{{ClientCancelled: true, FirstSemanticTimeout: true, ReplaySafe: true}, {GlobalInput: true, ReplaySafe: true}} {
		d := ClassifyAccountPoolFailure(e, a, now)
		require.Equal(t, "none", d.Effect)
		require.Equal(t, "stop", d.Retry)
	}
	d := ClassifyAccountPoolFailure(FailureEvidence{Trusted: true, Status: 401, Code: "invalid_api_key", ReplaySafe: true}, a, now)
	require.Equal(t, "auth_block", d.Effect)
	require.True(t, d.Hard)
	require.Equal(t, a.AccountKey(), d.Key)
	d = ClassifyAccountPoolFailure(FailureEvidence{FirstSemanticTimeout: true, OwnerPinned: true, ReplaySafe: true}, a, now)
	require.Equal(t, "stop", d.Retry)
	require.Equal(t, "cooldown", d.Effect)
}

func TestAccountPoolDoesNotConsultOrMutateLegacyRetryCredit(t *testing.T) {
	r, m := testRuntime(t)
	ctx := context.Background()
	req := accountPoolRequest()
	key := "xy2:scheduling:{" + digest(scopeKey(req.Policy)+":retry_budget") + "}:credit"
	require.NoError(t, r.store.client.Set(ctx, key, 0, 0).Err())
	legacy := req.Policy
	legacy.AccountPool = false
	allowed, err := r.CanRetry(ctx, legacy)
	require.NoError(t, err)
	require.False(t, allowed, "legacy behavior must still honor its exhausted pool")
	before := m.Dump()
	for i := 0; i < 20; i++ {
		allowed, err = r.CanRetry(ctx, req.Policy)
		require.NoError(t, err)
		require.True(t, allowed)
		for _, retry := range []bool{false, true} {
			allowed, err = r.CanRetryAfterDispatch(ctx, req.Policy, "request", retry)
			require.NoError(t, err)
			require.True(t, allowed)
			b, err := r.AcquireDispatchBudget(ctx, req.Policy, "request", retry)
			require.NoError(t, err)
			require.Equal(t, BudgetReservation{}, b)
			require.NoError(t, r.CommitDispatchBudget(ctx, b))
			require.NoError(t, r.RefundDispatchBudget(ctx, b))
		}
	}
	require.Equal(t, before, m.Dump(), "account pool touched legacy retry keys")
	// Removing this quota must not permit a dispatch when selection's atomic
	// reservation is unavailable. The dispatch receipt remains mandatory.
	r.store.client = accountPoolRejectEvalClient{r.store.client}
	b, err := r.AcquireDispatchBudget(ctx, req.Policy, "next", true)
	require.NoError(t, err)
	require.NoError(t, r.CommitDispatchBudget(ctx, b))
	require.NoError(t, r.RefundDispatchBudget(ctx, b))
	d, err := r.Select(ctx, req)
	require.ErrorIs(t, err, ErrSharedState)
	require.Zero(t, d.AccountID)
}

func TestAccountPoolEmptyBudgetPreservesSelectionReceiptSemantics(t *testing.T) {
	r, _ := testRuntime(t)
	ctx := context.Background()
	req := accountPoolRequest()
	d, err := r.Select(ctx, req)
	require.NoError(t, err)
	require.EqualValues(t, 1, d.AccountID)
	b, err := r.AcquireDispatchBudget(ctx, req.Policy, "sent", false)
	require.NoError(t, err)
	require.Empty(t, b.ID)
	require.NoError(t, r.CommitSelection(ctx, d))
	require.NoError(t, r.CommitDispatchBudget(ctx, b))
	require.NoError(t, r.ForgetDispatchReceipts(ctx, d, b))
	require.NoError(t, r.RefundDispatchBudget(ctx, b))
	require.NoError(t, r.ReleaseSelection(ctx, d))
	// The sent receipt was forgotten, so a late release cannot grant free traffic.
	next, err := r.Select(ctx, req)
	require.NoError(t, err)
	require.EqualValues(t, 2, next.AccountID)
	require.NoError(t, r.ReleaseSelection(ctx, next))
	forged := d
	forged.ReservationID = "missing-receipt"
	require.ErrorIs(t, r.CommitSelection(ctx, forged), ErrSharedState)
}
