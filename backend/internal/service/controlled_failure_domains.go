package service

import "context"

// Only provider scopes already authenticated by existing account metadata are
// added. A shared project_id alone does not prove a project-wide quota failure.
func controlledAccountFailureDomains(account *Account, model string) []string {
	if account == nil || !account.IsGrokOAuth() {
		return nil
	}
	fingerprint := grokTeamFingerprint(accountGrokTeamID(account))
	model = canonicalOpenAIAccountSchedulingModel(account, model)
	if fingerprint == "" || model == "" {
		return nil
	}
	return []string{"grok_team_model:" + grokTeamModelRateLimitKey(fingerprint, model)}
}

func blockControlledGrokTeamModelLimit(ctx context.Context, account *Account, model string) {
	if !ControlledSchedulingEnabled(ctx) {
		return
	}
	r := controlledRequest(ctx)
	r.mu.Lock()
	ledger := r.Ledger
	r.mu.Unlock()
	if ledger == nil {
		return
	}
	for _, domain := range controlledAccountFailureDomains(account, model) {
		ledger.BlockFailureDomain(domain)
	}
}
