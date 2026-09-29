package scheduling

import (
	"context"
	"errors"
	"fmt"
)

const (
	DefaultFirstOutputTimeoutMS int64 = 120000
	DefaultTotalWaitTimeoutMS   int64 = 240000
	DefaultGroupMaxAttempts           = 3
	MaxGroupPolicyAccounts            = 10000
)

var ErrSchedulingGroupNotFound = errors.New("scheduling group not found")

// GroupPolicy is the single administrator-controlled policy for a group. Model
// capability is filtered by adapters; it never creates another policy scope.
type GroupPolicy struct {
	GroupID              int64         `json:"group_id"`
	Version              int64         `json:"version"`
	Accounts             []AccountRule `json:"accounts"`
	FirstOutputTimeoutMS int64         `json:"first_output_timeout_ms"`
	TotalWaitTimeoutMS   int64         `json:"total_wait_timeout_ms"`
	MaxAttempts          int           `json:"max_attempts"`
}

type GroupPolicyWarning struct {
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	Models     []string `json:"models,omitempty"`
	AccountIDs []int64  `json:"account_ids,omitempty"`
}

type GroupPolicyRecord struct {
	GroupID           int64                `json:"group_id"`
	Version           int64                `json:"version"`
	Configured        bool                 `json:"configured"`
	DefaultScope      string               `json:"default_scope"`
	Policy            GroupPolicy          `json:"policy"`
	MigrationWarnings []GroupPolicyWarning `json:"migration_warnings"`
}

type SchedulingGroupPolicyStore interface {
	GetGroupPolicy(context.Context, int64) (GroupPolicyRecord, error)
	PutGroupPolicy(context.Context, GroupPolicy, int64) (GroupPolicyRecord, error)
}

func DefaultGroupPolicy(groupID int64) GroupPolicy {
	return GroupPolicy{GroupID: groupID, Accounts: []AccountRule{}, FirstOutputTimeoutMS: DefaultFirstOutputTimeoutMS, TotalWaitTimeoutMS: DefaultTotalWaitTimeoutMS, MaxAttempts: DefaultGroupMaxAttempts}
}

// New rows must be explicit: a missing/null priority is not silently inherited
// during a save. Existing account priority is used only by the read projection.
func ValidateGroupPolicy(p GroupPolicy) error {
	invalid := func(message string) error { return fmt.Errorf("%w: %s", ErrInvalidControl, message) }
	if p.GroupID < 0 {
		return invalid("group_id must be nonnegative")
	}
	if p.FirstOutputTimeoutMS < 1000 || p.FirstOutputTimeoutMS > 3600000 {
		return invalid("first_output_timeout_ms must be between 1000 and 3600000")
	}
	if p.TotalWaitTimeoutMS < p.FirstOutputTimeoutMS || p.TotalWaitTimeoutMS > 7200000 {
		return invalid("total_wait_timeout_ms must be at least the first output timeout and at most 7200000")
	}
	if p.MaxAttempts < 1 || p.MaxAttempts > 10 {
		return invalid("max_attempts must be between 1 and 10")
	}
	if len(p.Accounts) > MaxGroupPolicyAccounts {
		return invalid("too many account rules")
	}
	seen := make(map[int64]bool, len(p.Accounts))
	for _, a := range p.Accounts {
		if a.AccountID <= 0 || seen[a.AccountID] {
			return invalid("account IDs must be positive and unique")
		}
		seen[a.AccountID] = true
		if a.Priority == nil || *a.Priority < -2147483648 || *a.Priority > 2147483647 {
			return invalid("each account requires an integer priority in the signed 32-bit range")
		}
		if a.Weight < 0 || a.Weight > 1000000 {
			return invalid("traffic_weight must be between 0 and 1000000")
		}
		if a.FillOrder != 0 {
			return invalid("fill_order is not supported by group account scheduling")
		}
	}
	return nil
}
