package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"strings"
	"time"
)

type SchedulingExplainInput struct {
	GroupID       int64  `json:"group_id"`
	Model         string `json:"model"`
	Protocol      string `json:"protocol"`
	Platform      string `json:"platform,omitempty"`
	Reasoning     string `json:"reasoning_effort"`
	ContextTokens *int64 `json:"context_tokens"`
	PinAccountID  int64  `json:"pin_account_id"`
}
type SchedulingExplainEligibility struct {
	Eligible bool
	Reason   string
}
type SchedulingExplainEligibilityFunc func(context.Context, SchedulingExplainInput, []Account) (map[int64]SchedulingExplainEligibility, error)

func (s *ControlledSchedulingService) SetExplainEligibility(openai, gateway SchedulingExplainEligibilityFunc) {
	s.explainOpenAI = openai
	s.explainGateway = gateway
}

type schedulingExplainRow struct {
	ID                 int64      `json:"account_id"`
	Name               string     `json:"account_name"`
	Priority           int        `json:"priority"`
	Weight             int64      `json:"traffic_weight"`
	Eligible           bool       `json:"eligible"`
	Reason             string     `json:"reason"`
	Health             string     `json:"health"`
	Control            string     `json:"control_state"`
	FamilyControl      string     `json:"family_control_state"`
	Share              *float64   `json:"target_share"`
	EffectiveShare     *float64   `json:"effective_share"`
	RecoveryStage      *int       `json:"recovery_stage"`
	GoodStreak         int        `json:"good_streak"`
	CooldownUntil      *time.Time `json:"cooldown_until"`
	CurrentConcurrency int        `json:"current_concurrency"`
	ConcurrencyLimit   int        `json:"concurrency_limit"`
	CapacityAvailable  bool       `json:"capacity_available"`
}

// Explain reads current allocation balances without lease cleanup, reservations,
// probes, session registration or dispatch. Admission still rechecks every gate.
func (s *ControlledSchedulingService) Explain(ctx context.Context, raw json.RawMessage) (any, error) {
	var input SchedulingExplainInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	input.Model = strings.TrimSpace(input.Model)
	input.Protocol = scheduling.CanonicalTransport(input.Protocol)
	if input.Protocol == "" {
		input.Protocol = "http"
	}
	if input.GroupID < 0 || input.Model == "" || input.PinAccountID < 0 || (input.ContextTokens != nil && *input.ContextTokens < 0) {
		return nil, scheduling.ErrInvalidControl
	}
	rec, err := s.Store.GetPolicy(ctx, input.GroupID, input.Model)
	if err != nil {
		return nil, err
	}
	var defaults *scheduling.Policy
	if input.GroupID != 0 {
		global, e := s.Store.GetPolicy(ctx, 0, input.Model)
		if e != nil {
			return nil, e
		}
		defaults = global.Policy
	}
	p := scheduling.Policy{GroupID: input.GroupID, Model: input.Model}
	policySource := "legacy"
	if rec.Policy != nil {
		p = *rec.Policy
		policySource = "scope"
	} else if defaults != nil {
		p = *defaults
		p.GroupID = input.GroupID
		policySource = "global"
	}
	p = scheduling.NormalizePolicy(p)
	tokens := int64(-1)
	if input.ContextTokens != nil {
		tokens = *input.ContextTokens
	}
	profile, ok := scheduling.ResolveProfileForTransport(p, input.Reasoning, tokens, input.Protocol)
	profileSource := policySource
	if !ok && defaults != nil {
		profile, ok = scheduling.ResolveProfileForTransport(*defaults, input.Reasoning, tokens, input.Protocol)
		profileSource = "global"
	}
	if !ok {
		profile = scheduling.LatencyProfile{Name: "unconfigured"}
		profileSource = "observation_only"
	}
	if input.PinAccountID > 0 {
		p.Mode = scheduling.ModePin
		p.PinAccountID = input.PinAccountID
		p.PinFallback = false
	}
	var accounts []Account
	if input.GroupID > 0 {
		accounts, err = s.accounts.ListByGroup(ctx, input.GroupID)
	} else {
		accounts, err = s.accounts.ListAllWithFilters(ctx, "", "", "", "", 0, "")
	}
	if err != nil {
		return nil, err
	}
	facts := map[int64]SchedulingExplainEligibility{}
	for _, provider := range []SchedulingExplainEligibilityFunc{s.explainOpenAI, s.explainGateway} {
		if provider != nil {
			part, e := provider(ctx, input, accounts)
			if e != nil {
				return nil, e
			}
			for id, v := range part {
				facts[id] = v
			}
		}
	}
	now := time.Now()
	req := scheduling.SelectionRequest{Policy: p, Profile: profile, Reasoning: input.Reasoning, Transport: input.Protocol, ContextBucket: scheduling.ContextBucket(tokens), Now: now}
	ids := make([]int64, 0, len(accounts))
	loads := make([]AccountWithConcurrency, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
		loads = append(loads, AccountWithConcurrency{ID: a.ID, MaxConcurrency: a.Concurrency})
	}
	counts, err := s.Store.AccountActiveCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	load, err := s.concurrency.PeekAccountsLoadBatch(ctx, loads)
	if err != nil {
		return nil, err
	}
	rows := make([]schedulingExplainRow, 0, len(accounts))
	for _, a := range accounts {
		state, e := s.Store.GetControl(ctx, a.ID, scheduling.ScopeAccount)
		if e != nil {
			return nil, e
		}
		family, e := s.Store.GetControl(ctx, a.ID, scheduling.ScopeFamily)
		if e != nil {
			return nil, e
		}
		eligible, reason := false, "eligibility_provider_unavailable"
		if fact, present := facts[a.ID]; present {
			eligible, reason = fact.Eligible, fact.Reason
		}
		if state.State != scheduling.ControlRunning {
			eligible = false
			reason = "account_" + state.State
		} else if family.State != scheduling.ControlRunning {
			eligible = false
			reason = "family_" + family.State
		}
		priority, weight := a.Priority, int64(1)
		for _, rule := range p.Accounts {
			if rule.AccountID == a.ID {
				weight = rule.Weight
				if rule.Priority != nil {
					priority = *rule.Priority
				}
				break
			}
		}
		if weight == 0 {
			eligible = false
			reason = "weight_zero"
		}
		used := counts[a.ID]
		if info := load[a.ID]; info != nil {
			if info.CurrentConcurrency > used {
				used = info.CurrentConcurrency
			}
		} else {
			return nil, scheduling.ErrSharedState
		}
		capacity := a.Concurrency <= 0 || used < a.Concurrency
		req.Candidates = append(req.Candidates, scheduling.Candidate{AccountID: a.ID, Priority: a.Priority, HardEligible: eligible, CapacityAvailable: capacity})
		rows = append(rows, schedulingExplainRow{ID: a.ID, Name: a.Name, Priority: priority, Weight: weight, Eligible: eligible, Reason: reason, Control: state.State, FamilyControl: family.State, CurrentConcurrency: used, ConcurrencyLimit: a.Concurrency, CapacityAvailable: capacity})
	}
	mode, reason := "legacy", "legacy_mode_observation"
	var selected *int64
	snapshot, e := s.Runtime.InspectSelection(ctx, req)
	if e != nil && !errors.Is(e, scheduling.ErrNoCandidate) && !errors.Is(e, scheduling.ErrCapacity) {
		return nil, e
	}
	options := map[int64]scheduling.Decision{}
	for _, d := range snapshot.Options {
		options[d.AccountID] = d
	}
	if p.Enabled {
		mode = p.Mode
		reason = "snapshot_selection"
		if e != nil {
			reason = e.Error()
		} else if snapshot.Selected != nil {
			id := snapshot.Selected.AccountID
			selected = &id
		}
	}
	for i := range rows {
		row := &rows[i]
		h := snapshot.Health[row.ID]
		row.Health = string(h.State)
		row.GoodStreak = h.GoodStreak
		if h.State == scheduling.HealthRecovering {
			stage := h.RecoveryStage
			row.RecoveryStage = &stage
		}
		if h.CooldownUntilMS > 0 {
			v := time.UnixMilli(h.CooldownUntilMS).UTC()
			row.CooldownUntil = &v
		}
		if p.Enabled {
			zeroTarget, zeroEffective := float64(0), float64(0)
			row.Share = &zeroTarget
			row.EffectiveShare = &zeroEffective
		}
		if d, yes := options[row.ID]; yes && p.Enabled {
			row.Share = &d.TargetShare
			row.EffectiveShare = &d.EffectiveShare
			if row.Eligible {
				row.Reason = d.Reason
			}
		}
		if row.Eligible && !row.CapacityAvailable {
			row.Reason = "capacity_full"
		}
		if row.Eligible && h.State == scheduling.HealthOpen {
			row.Reason = "health_cooldown"
		}
	}
	return map[string]any{"policy_version": p.Version, "policy_source": policySource, "mode": mode, "reason": reason, "profile": profile, "profile_source": profileSource, "context_bucket": req.ContextBucket, "protocol": input.Protocol, "candidates": rows, "readonly": true, "selected_account_id": selected, "snapshot_at": snapshot.SnapshotAt, "scope": "ordinary_first_request_group_defaults", "scope_notes": []string{"No reservation is made; final dispatch rechecks control and capacity.", "Owner/session continuation, API-key authorization and user billing overrides require the real request context."}}, nil
}
