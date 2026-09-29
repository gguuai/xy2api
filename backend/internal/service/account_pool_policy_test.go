//go:build unit

package service

import (
	"reflect"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

func TestAccountPoolGroupConfigurationCoversEveryModel(t *testing.T) {
	priority := 1
	group := scheduling.GroupPolicy{GroupID: 7, Version: 3, MaxAttempts: 3, FirstOutputTimeoutMS: 120000, TotalWaitTimeoutMS: 240000, Accounts: []scheduling.AccountRule{{AccountID: 11, Priority: &priority, Weight: 70}, {AccountID: 12, Priority: &priority, Weight: 30}}}
	first, timing := accountPoolPolicy(group, "model-a")
	second, _ := accountPoolPolicy(group, "new-model-b")
	if !first.Enabled || !second.Enabled || !first.AccountPool || !second.AccountPool || !reflect.DeepEqual(first.Accounts, second.Accounts) {
		t.Fatal("models must share the group account configuration without opting into a model policy")
	}
	if first.Model != "model-a" || second.Model != "new-model-b" || first.Version != 3 || len(first.Profiles) != 0 {
		t.Fatal("request model remains capability metadata, not a configuration dimension")
	}
	if timing.Name != "account_pool" || timing.HealthThresholdMS != 0 || timing.RecoveryThresholdMS != 0 || timing.TotalBudgetMS != 240000 {
		t.Fatal("new account pool must not create the old latency profiles")
	}
}

func TestAccountPoolRetriesExhaustSameTierBeforeDescending(t *testing.T) {
	p, profile := accountPoolPolicy(scheduling.GroupPolicy{GroupID: 7, MaxAttempts: 4, FirstOutputTimeoutMS: 120000, TotalWaitTimeoutMS: 240000}, "m")
	now := time.Now()
	ledger := scheduling.NewAttemptLedger(p.Retry, profile, now, time.Time{})
	candidates := []scheduling.Candidate{
		{AccountID: 11, Priority: 1, HardEligible: true, CapacityAvailable: true},
		{AccountID: 12, Priority: 1, HardEligible: true, CapacityAvailable: true},
		{AccountID: 13, Priority: 1, HardEligible: true, CapacityAvailable: true},
		{AccountID: 21, Priority: 2, HardEligible: true, CapacityAvailable: true},
	}
	for i, id := range []int64{11, 12, 13} {
		options, err := scheduling.Evaluate(scheduling.SelectionRequest{Policy: p, Profile: profile, Candidates: candidates, Attempted: ledger.AttemptedAccounts(), Now: now}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, option := range options {
			if option.Priority != 1 {
				t.Fatal("must not skip an untried eligible same-tier account")
			}
		}
		if err := ledger.BeginAttempt(id, 1, now.Add(time.Duration(i)*time.Second), true); err != nil {
			t.Fatal(err)
		}
	}
	options, err := scheduling.Evaluate(scheduling.SelectionRequest{Policy: p, Profile: profile, Candidates: candidates, Attempted: ledger.AttemptedAccounts(), Now: now}, nil)
	if err != nil || len(options) != 1 || options[0].AccountID != 21 {
		t.Fatalf("expected next tier only after exhausting current tier: %+v %v", options, err)
	}
	if err := ledger.BeginAttempt(21, 2, now.Add(3*time.Second), true); err != nil {
		t.Fatal(err)
	}
	if err := ledger.CanAttempt(31, 3, now.Add(4*time.Second), true); err == nil {
		t.Fatal("total request budget must still stop further attempts")
	}
}

func TestAccountPoolBudgetCanExpireBeforeReachingLowerTier(t *testing.T) {
	p, profile := accountPoolPolicy(scheduling.GroupPolicy{GroupID: 7, MaxAttempts: 3, FirstOutputTimeoutMS: 120000, TotalWaitTimeoutMS: 240000}, "m")
	now := time.Now()
	ledger := scheduling.NewAttemptLedger(p.Retry, profile, now, time.Time{})
	for _, id := range []int64{11, 12, 13} {
		if err := ledger.BeginAttempt(id, 1, now, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := ledger.CanAttempt(21, 2, now, true); err != scheduling.ErrAttemptBudget {
		t.Fatalf("budget must not be reset when descending: %v", err)
	}
}

func TestAccountPoolWaitBudgetDoesNotResetAcrossRetries(t *testing.T) {
	p, profile := accountPoolPolicy(scheduling.GroupPolicy{GroupID: 7, MaxAttempts: 3, FirstOutputTimeoutMS: 120000, TotalWaitTimeoutMS: 240000}, "m")
	now := time.Now()
	ledger := scheduling.NewAttemptLedger(p.Retry, profile, now, now.Add(130*time.Second))
	if err := ledger.BeginAttempt(11, 1, now, true); err != nil {
		t.Fatal(err)
	}
	ledger.MarkFirstOutputTimeout()
	if err := ledger.BeginAttempt(12, 1, now.Add(120*time.Second), true); err != nil {
		t.Fatal(err)
	}
	if got := ledger.AttemptWindow(now.Add(120*time.Second), false); got != 10*time.Second {
		t.Fatalf("remaining wait = %v, want 10s", got)
	}
	if err := ledger.CanAttempt(21, 2, now.Add(121*time.Second), true); err != nil {
		t.Fatalf("remaining time and attempts must not be overridden by the old post-timeout cap: %v", err)
	}
	ledger.MarkSemanticCommit()
	if err := ledger.CanAttempt(21, 2, now.Add(122*time.Second), true); err != scheduling.ErrCommitted {
		t.Fatalf("semantic output must prevent replay: %v", err)
	}
}
