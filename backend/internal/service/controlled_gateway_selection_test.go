package service

import (
	"context"
	"testing"
)

func TestControlledGatewayEligibilityPreservesHardGates(t *testing.T) {
	s := &GatewayService{}
	groupID := int64(7)
	group := &Group{ID: groupID}
	base := Account{ID: 1, Status: StatusActive, Schedulable: true, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, GroupIDs: []int64{groupID}}
	cases := []struct {
		name     string
		mutate   func(*Account)
		platform string
		group    *Group
		allowed  bool
		reason   string
	}{
		{"eligible", func(*Account) {}, PlatformAnthropic, group, true, "eligible"},
		{"manual_pause", func(a *Account) { a.Schedulable = false }, PlatformAnthropic, group, false, "account_unavailable"},
		{"different_group", func(a *Account) { a.GroupIDs = []int64{9} }, PlatformAnthropic, group, false, "group_mismatch"},
		{"different_platform", func(a *Account) { a.Platform = PlatformGemini }, PlatformAnthropic, group, false, "platform_mismatch"},
		{"privacy_required", func(a *Account) { a.Platform = PlatformAntigravity }, PlatformAntigravity, &Group{ID: groupID, RequirePrivacySet: true}, false, "privacy_required"},
		{"quota_exhausted", func(a *Account) { a.Extra = map[string]any{"quota_limit": float64(1), "quota_used": float64(1)} }, PlatformAnthropic, group, false, "account_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := base
			tc.mutate(&a)
			ok, reason := s.controlledGatewayEligibility(context.Background(), &a, &groupID, tc.group, "", tc.platform, false)
			if ok != tc.allowed || reason != tc.reason {
				t.Fatalf("got %v %q want %v %q", ok, reason, tc.allowed, tc.reason)
			}
		})
	}
}

func TestControlledGatewayEligibilityIgnoresTPSMetadata(t *testing.T) {
	s := &GatewayService{}
	a := Account{ID: 1, Status: StatusActive, Schedulable: true, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	for _, speed := range []float64{0, .01, 10, 100000} {
		a.Extra = map[string]any{"tps": speed, "token_speed": speed, "tokens_per_second": speed}
		ok, reason := s.controlledGatewayEligibility(context.Background(), &a, nil, nil, "", PlatformAnthropic, false)
		if !ok {
			t.Fatalf("TPS %v affected eligibility: %s", speed, reason)
		}
	}
}
