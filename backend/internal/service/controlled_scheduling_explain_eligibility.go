package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"math"
	"time"
)

func explainGroup(ctx context.Context, repo GroupRepository, id int64) (*Group, *int64, error) {
	if id == 0 {
		return nil, nil, nil
	}
	if repo == nil {
		return nil, nil, scheduling.ErrSharedState
	}
	g, e := repo.GetByIDLite(ctx, id)
	if e != nil {
		return nil, nil, e
	}
	if g == nil {
		return nil, nil, ErrGroupNotFound
	}
	return g, &id, nil
}
func explainPlatform(input SchedulingExplainInput, g *Group) string {
	if input.Platform != "" {
		return input.Platform
	}
	if g != nil && g.Platform != "" {
		return g.Platform
	}
	if input.Protocol == "messages" {
		return PlatformAnthropic
	}
	if input.Protocol == "gemini" {
		return PlatformGemini
	}
	return PlatformOpenAI
}
func explainProfitEligible(g *Group, a *Account, now time.Time) bool {
	if g == nil || !g.ProfitControlEnabled || !profitControlPlatformSupported(g.Platform) {
		return true
	}
	if a.RateMultiplier == nil || math.IsNaN(*a.RateMultiplier) || math.IsInf(*a.RateMultiplier, 0) || *a.RateMultiplier < 0 {
		return false
	}
	threshold := clampProfitControlThreshold(g.RateMultiplier * g.PeakMultiplierAt(now) * (1 - g.ProfitMinMargin - g.ProfitSafetyBuffer))
	return !profitControlOverThreshold(*a.RateMultiplier, threshold)
}
func explainThresholdBlocked(ctx context.Context, s *RateLimitService, a *Account, model string, now time.Time) bool {
	if s == nil || s.settingService == nil {
		return false
	}
	thresholds := explainThresholds(ctx, s.settingService)
	d := EvaluateAccountSchedulingThreshold(a, thresholds, now)
	if d.ShouldPause && d.Until != nil && d.Until.After(now) {
		return true
	}
	if isAnthropicFableModel(model) {
		d = evaluateAnthropicFableSchedulingThreshold(a, thresholds, now)
		return d.ShouldPause && d.Until != nil && d.Until.After(now)
	}
	return false
}

// Unlike production's cleanup helpers, these reads leave expired local entries
// intact. Fresh snapshots and normal scheduling own all cleanup and state changes.
func (s *OpenAIGatewayService) explainRuntimeBlocked(a *Account, model string, now time.Time) bool {
	outbound := s.openAICodexTicketOutboundModel(a, model, false)
	if s.openAICodexTicketBlocksAccount(a, outbound) {
		return true
	}
	if raw, ok := s.openaiAccountRuntimeBlockUntil.Load(a.ID); ok {
		if until, ok := raw.(time.Time); ok && now.Before(until) && accountPersistedSchedulingCooldownActive(a) {
			return true
		}
	}
	if state := s.openaiModelTransient; state != nil {
		key, ok := openAIAccountModelTransientKey(a.ID, openAIAccountModelTransientModel(canonicalOpenAIAccountSchedulingModel(a, model)))
		if ok {
			state.mu.Lock()
			entry := state.entries[key]
			state.mu.Unlock()
			if (entry.lastFailure.IsZero() || now.Sub(entry.lastFailure) <= openAIModelTransientStreakTTL) && now.Before(entry.blockUntil) {
				return true
			}
		}
	}
	return false
}
func (s *OpenAIGatewayService) explainProxyBlocked(a *Account, now time.Time) bool {
	id, ok := openAIProxyStreamCircuitProxyID(a)
	if !ok {
		return false
	}
	c := s.openaiProxyStreamCircuit
	if c == nil || c.settings.disabled {
		return false
	}
	c.mu.Lock()
	entry := c.entries[id]
	c.mu.Unlock()
	return now.Before(entry.blockedUntil)
}

// ExplainSchedulingEligibility reads the same hard constraints as controlled
// OpenAI selection, with pure counterparts for threshold/runtime cleanup gates.
func (s *OpenAIGatewayService) ExplainSchedulingEligibility(ctx context.Context, in SchedulingExplainInput, accounts []Account) (map[int64]SchedulingExplainEligibility, error) {
	out := map[int64]SchedulingExplainEligibility{}
	var repo GroupRepository
	if s.schedulerSnapshot != nil {
		repo = s.schedulerSnapshot.groupRepo
	}
	g, gid, err := explainGroup(ctx, repo, in.GroupID)
	if err != nil {
		return nil, err
	}
	platform := explainPlatform(in, g)
	if s.settingService != nil {
		if cached, _ := s.settingService.openAIQuotaAutoPauseSettingsCache.Load().(*cachedOpenAIQuotaAutoPauseSettings); cached != nil {
			ctx = withOpenAIQuotaAutoPauseSettings(ctx, cached.settings)
		}
	}
	now := time.Now()
	channelBlocked := s.checkChannelPricingRestriction(ctx, gid, in.Model)
	checker := &defaultOpenAIAccountScheduler{service: s}
	for i := range accounts {
		a := &accounts[i]
		if !a.IsOpenAICompatible() {
			continue
		}
		check := func() (bool, string, error) {
			if !a.IsSchedulable() {
				return false, "hard_unavailable", nil
			}
			if !s.openAIAccountMatchesSchedulingGroup(a, gid) {
				return false, "unauthorized_group", nil
			}
			if a.Platform != platform {
				return false, "platform_mismatch", nil
			}
			if in.Protocol == "ws" && !checker.isAccountTransportCompatible(a, OpenAIUpstreamTransportResponsesWebsocketV2Ingress) {
				return false, "transport_mismatch", nil
			}
			if g != nil && g.RequirePrivacySet && !a.IsPrivacySet() {
				return false, "privacy_not_set", nil
			}
			if s.explainRuntimeBlocked(a, in.Model, now) {
				return false, "runtime_blocked", nil
			}
			if s.explainProxyBlocked(a, now) {
				return false, "proxy_stream_quarantined", nil
			}
			if paused, _ := shouldAutoPauseOpenAIAccountByQuota(ctx, a); paused {
				return false, "quota_auto_pause", nil
			}
			if explainThresholdBlocked(ctx, s.rateLimitService, a, in.Model, now) {
				return false, "quota_threshold", nil
			}
			var parentErr error
			if !parentHealthyForShadow(a, func(id int64) *Account { var p *Account; p, parentErr = s.accountRepo.GetByID(ctx, id); return p }) {
				if parentErr != nil {
					return false, "", parentErr
				}
				return false, "shadow_parent_unhealthy", nil
			}
			if !a.IsModelSupported(in.Model) {
				return false, "model_not_supported", nil
			}
			if a.Platform == PlatformGrok && (len(filterGrokTeamModelRateLimitedAccounts([]Account{*a}, in.Model, now)) == 0 || len(filterGrokModelQuotaBlockedAccounts([]Account{*a}, in.Model, now)) == 0) {
				return false, "grok_model_quota", nil
			}
			if channelBlocked || (gid != nil && s.needsUpstreamChannelRestrictionCheck(ctx, gid) && s.isUpstreamModelRestrictedByChannel(ctx, *gid, a, in.Model, false)) {
				return false, "channel_upstream_restricted", nil
			}
			if !explainProfitEligible(g, a, now) {
				return false, "profit_gate", nil
			}
			return true, "eligible", nil
		}
		ok, reason, e := check()
		if e != nil {
			return nil, e
		}
		out[a.ID] = SchedulingExplainEligibility{ok, reason}
	}
	return out, nil
}
func (s *GatewayService) explainWindowEligible(ctx context.Context, a *Account) (bool, error) {
	if !a.IsAnthropicOAuthOrSetupToken() || a.GetWindowCostLimit() <= 0 {
		return true, nil
	}
	if s.sessionLimitCache != nil {
		if cost, hit, e := s.sessionLimitCache.GetWindowCost(ctx, a.ID); e == nil && hit {
			return a.CheckWindowCostSchedulability(cost) == WindowCostSchedulable, nil
		}
	}
	if s.usageLogRepo == nil {
		return false, scheduling.ErrSharedState
	}
	stats, err := s.usageLogRepo.GetAccountWindowStats(ctx, a.ID, a.GetCurrentWindowStartTime())
	if err != nil {
		return false, err
	}
	if stats == nil {
		return false, scheduling.ErrSharedState
	}
	return a.CheckWindowCostSchedulability(stats.StandardCost) == WindowCostSchedulable, nil
}
func (s *GatewayService) ExplainSchedulingEligibility(ctx context.Context, in SchedulingExplainInput, accounts []Account) (map[int64]SchedulingExplainEligibility, error) {
	out := map[int64]SchedulingExplainEligibility{}
	g, gid, err := explainGroup(ctx, s.groupRepo, in.GroupID)
	if err != nil {
		return nil, err
	}
	platform := explainPlatform(in, g)
	useMixed := platform == PlatformAnthropic || platform == PlatformGemini
	now := time.Now()
	for i := range accounts {
		a := &accounts[i]
		if a.IsOpenAICompatible() {
			continue
		}
		check := func() (bool, string, error) {
			if !a.IsSchedulable() {
				return false, "account_unavailable", nil
			}
			simple := gid == nil && s.cfg != nil && s.cfg.RunMode == config.RunModeSimple
			if !simple && !openAIStickyAccountMatchesGroup(a, gid) {
				return false, "group_mismatch", nil
			}
			if !s.isAccountAllowedForPlatform(a, platform, useMixed) {
				return false, "platform_mismatch", nil
			}
			if g != nil && g.RequirePrivacySet && !a.IsPrivacySet() {
				return false, "privacy_required", nil
			}
			if !explainProfitEligible(g, a, now) {
				return false, "profit_gate", nil
			}
			if !s.isModelSupportedByAccountWithContext(ctx, a, in.Model) {
				return false, "model_unsupported", nil
			}
			if !s.isAccountSchedulableForModelSelection(ctx, a, in.Model) {
				return false, "model_unavailable", nil
			}
			if !s.isAccountSchedulableForQuota(a) {
				return false, "quota_exhausted", nil
			}
			if explainThresholdBlocked(ctx, s.rateLimitService, a, in.Model, now) {
				return false, "scheduling_threshold", nil
			}
			ok, e := s.explainWindowEligible(ctx, a)
			if e != nil {
				return false, "", e
			}
			if !ok {
				return false, "window_cost", nil
			}
			if !s.isAccountSchedulableForRPM(ctx, a, false) {
				return false, "rpm_limit", nil
			}
			if s.checkChannelPricingRestriction(ctx, gid, in.Model) || s.isStickyAccountUpstreamRestricted(ctx, gid, a, in.Model) {
				return false, "channel_model_restricted", nil
			}
			return true, "eligible", nil
		}
		ok, reason, e := check()
		if e != nil {
			return nil, e
		}
		out[a.ID] = SchedulingExplainEligibility{ok, reason}
	}
	return out, nil
}

// Settings are read without invoking refresh or filling shared caches. A stale
// auto-pause snapshot above matches the value a real request currently receives.
func explainThresholds(ctx context.Context, s *SettingService) map[string]int {
	if s == nil || s.settingRepo == nil {
		return defaultAccountSchedulingThresholds()
	}
	if cached, ok := accountSchedulingThresholdsCache.Load().(*cachedAccountSchedulingThresholds); ok && cached != nil && len(cached.thresholds) > 0 && time.Now().UnixNano() < cached.expiresAt {
		return cloneAccountSchedulingThresholds(cached.thresholds)
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyAccountSchedulingThresholds)
	if err != nil {
		return defaultAccountSchedulingThresholds()
	}
	parsed, err := parseAccountSchedulingThresholdsSetting(raw)
	if err != nil {
		return defaultAccountSchedulingThresholds()
	}
	return parsed
}
