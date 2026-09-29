package service

import "github.com/liulixin-lex/xy2api/internal/scheduling"

// accountPoolPolicy adapts a group account configuration to the transport
// ledger. Model identifies capabilities and observations, never configuration.
func accountPoolPolicy(group scheduling.GroupPolicy, model string) (scheduling.Policy, scheduling.LatencyProfile) {
	retry := scheduling.DefaultRetryPolicy()
	retry.MaxAttempts = group.MaxAttempts
	retry.Mode = "exhaust_same_tier"
	retry.MaxPerTier = group.MaxAttempts
	retry.MaxPerAccount = 1
	retry.MaxAfterTimeout = max(0, group.MaxAttempts-1)
	// Independent alternatives should start immediately; failed-account
	// cooldown is enforced at admission and is not a request sleep.
	retry.SwitchMarginMS = 0
	retry.ReserveFallback = false
	policy := scheduling.Policy{
		GroupID: group.GroupID, Model: model, Version: group.Version,
		Enabled: true, AccountPool: true, Mode: scheduling.ModeSWRR,
		Accounts: group.Accounts, Retry: retry,
		Overflow: scheduling.OverflowImmediate, AllDegraded: scheduling.AllDegradedFailFast,
	}
	profile := scheduling.LatencyProfile{
		Name: "account_pool", AttemptTimeoutMS: group.FirstOutputTimeoutMS,
		TotalBudgetMS: group.TotalWaitTimeoutMS, MinAttemptWindowMS: 1000,
	}
	return policy, profile
}
