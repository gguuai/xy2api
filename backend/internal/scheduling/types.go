// Package scheduling implements explicit, shared account scheduling policies.
package scheduling

import (
	"errors"
	"time"
)

const (
	ModeSWRR                  = "swrr"
	ModePin                   = "pin"
	ModeFillFirst             = "fill_first"
	OverflowImmediate         = "immediate"
	OverflowWait              = "wait"
	AllDegradedBestEffort     = "bounded_best_effort"
	AllDegradedStrictPriority = "strict_priority"
	AllDegradedFailFast       = "fail_fast"
)

var (
	ErrNoCandidate          = errors.New("no eligible scheduling candidate")
	ErrCapacity             = errors.New("eligible priority tier has no capacity")
	ErrSharedState          = errors.New("shared scheduling state unavailable")
	ErrAttemptBudget        = errors.New("request attempt budget exhausted")
	ErrDeadline             = errors.New("request first-output deadline exhausted")
	ErrCommitted            = errors.New("response already semantically committed")
	ErrUnsafeReplay         = errors.New("request cannot be replayed safely")
	ErrRetryBudget          = errors.New("shared retry budget exhausted")
	ErrHealthIdentity       = errors.New("invalid health observation identity")
	ErrHealthSelectionStale = errors.New("health selection no longer admits dispatch")
)

type Policy struct {
	// AccountPool selects the group account-pool kernel. It is internal transport
	// metadata, never a second administrator-configurable routing mode.
	AccountPool  bool             `json:"-"`
	GroupID      int64            `json:"group_id"`
	Model        string           `json:"model"`
	Version      int64            `json:"version"`
	Enabled      bool             `json:"enabled"`
	Mode         string           `json:"mode"`
	Accounts     []AccountRule    `json:"accounts"`
	Profiles     []LatencyProfile `json:"profiles"`
	Retry        RetryPolicy      `json:"retry"`
	Overflow     string           `json:"overflow"`
	QueueWaitMS  int64            `json:"queue_wait_ms"`
	AllDegraded  string           `json:"all_degraded"`
	PinAccountID int64            `json:"pin_account_id,omitempty"`
	PinFallback  bool             `json:"pin_fallback"`
}

type AccountRule struct {
	AccountID int64 `json:"account_id"`
	Priority  *int  `json:"priority,omitempty"`
	Weight    int64 `json:"traffic_weight"`
	FillOrder int   `json:"fill_order"`
}

type LatencyProfile struct {
	// HealthRevision is assigned by the policy store only when health semantics change.
	HealthRevision      int64  `json:"health_revision,omitempty"`
	Name                string `json:"name"`
	Reasoning           string `json:"reasoning,omitempty"`
	Transport           string `json:"transport,omitempty"`
	ContextMinTokens    int64  `json:"context_min_tokens,omitempty"`
	ContextMaxTokens    int64  `json:"context_max_tokens,omitempty"`
	HealthThresholdMS   int64  `json:"health_threshold_ms"`
	RecoveryThresholdMS int64  `json:"recovery_threshold_ms"`
	AttemptTimeoutMS    int64  `json:"attempt_timeout_ms"`
	TotalBudgetMS       int64  `json:"total_budget_ms"`
	MinAttemptWindowMS  int64  `json:"min_attempt_window_ms"`
}

type RetryPolicy struct {
	Mode            string `json:"mode"`
	MaxAttempts     int    `json:"max_attempts"`
	MaxPerTier      int    `json:"max_per_tier"`
	MaxPerAccount   int    `json:"max_per_account"`
	MaxAfterTimeout int    `json:"max_after_timeout"`
	InitialPerToken int    `json:"initial_per_token"`
	Burst           int    `json:"burst"`
	SwitchMarginMS  int64  `json:"switch_margin_ms"`
	ReserveFallback bool   `json:"reserve_fallback"`
	CrossTier       bool   `json:"cross_tier"`
}

type HealthState string

const (
	HealthUnknown    HealthState = "unknown"
	HealthHealthy    HealthState = "healthy"
	HealthDegraded   HealthState = "degraded"
	HealthOpen       HealthState = "open"
	HealthHalfOpen   HealthState = "half_open"
	HealthRecovering HealthState = "recovering"
)

// Candidate contains only source-of-truth eligibility; no TPS or soft score.
type Candidate struct {
	HealthModel       string // actual predicted upstream model; empty falls back to policy model
	HealthIdentity    string // stable upstream identity, excluding ordinary OAuth token rotation
	AccountID         int64
	Priority          int
	HardEligible      bool
	CapacityAvailable bool
	FailureDomain     string
	FailureDomains    []string // additional verified provider scopes, never inferred from missing metadata
}

type HealthSample struct {
	HasSemanticOutput   bool  `json:"has_semantic_output"`
	Completed           bool  `json:"completed"`
	AtMS                int64 `json:"at_ms"`
	TTFTMS              int64 `json:"ttft_ms"`
	Timeout             bool  `json:"timeout"`
	AttributableFailure bool  `json:"attributable_failure"`
}

// RecoveryRequirement chooses the evidence required by the single recovery ladder.
// Latency recovery always requires a successful terminal with real semantic TTFT.
type RecoveryRequirement string

const (
	RecoveryAvailability RecoveryRequirement = "availability"
	RecoveryLatency      RecoveryRequirement = "latency"
)

// HealthFence is frozen at dispatch. Generation changes if the Redis record is
// rebuilt; StageRevision changes on every breaker or recovery-stage transition.
// Neither value is the administrative pause/resume epoch.
type HealthFence struct {
	Model          string      `json:"model,omitempty"`
	HealthIdentity string      `json:"health_identity,omitempty"`
	State          HealthState `json:"state"`
	Generation     string      `json:"generation"`
	StageRevision  uint64      `json:"stage_revision"`
	HealthRevision int64       `json:"health_revision"`
}

type HealthSnapshot struct {
	Generation          string              `json:"generation"`
	StageRevision       uint64              `json:"stage_revision"`
	RecoveryRequirement RecoveryRequirement `json:"recovery_requirement,omitempty"`
	RecoveryFailures    int                 `json:"recovery_failures"`
	State               HealthState         `json:"state"`
	Samples             []HealthSample      `json:"samples"`
	ChangedAtMS         int64               `json:"changed_at_ms"`
	CooldownUntilMS     int64               `json:"cooldown_until_ms"`
	GoodStreak          int                 `json:"good_streak"`
	RecoveryStage       int                 `json:"recovery_stage"`
	UpdatedAtMS         int64               `json:"updated_at_ms"`
}
type Observation struct {
	HealthIdentity      string
	AttemptID           string
	Fence               *HealthFence
	Completed           bool
	RetryAfter          time.Time
	AccountID           int64
	Model               string
	Reasoning           string
	ContextBucket       string
	Transport           string
	Profile             LatencyProfile
	At                  time.Time
	TTFT                time.Duration
	HasSemanticOutput   bool
	FirstOutputTimeout  bool
	AttributableFailure bool
	Excluded            bool // User cancellation, local queue/budget, or administrative action.
}
type SelectionRequest struct {
	Policy                 Policy
	Candidates             []Candidate
	Profile                LatencyProfile
	Reasoning              string
	ContextBucket          string
	Transport              string
	Now                    time.Time
	Attempted              map[int64]bool
	ExcludedFailureDomains map[string]bool
	MinPriority            *int
	Retry                  bool
}
type Decision struct {
	HealthFence    *HealthFence `json:"-"`
	AccountID      int64        `json:"account_id"`
	Priority       int          `json:"priority"`
	PolicyVersion  int64        `json:"policy_version"`
	Reason         string       `json:"reason"`
	HealthState    HealthState  `json:"health_state"`
	TargetShare    float64      `json:"target_share"`
	EffectiveShare float64      `json:"effective_share"`
	Probe          bool         `json:"probe"`
	ProbeToken     string       `json:"probe_token,omitempty"`
	ReservationID  string       `json:"reservation_id,omitempty"`
	PoolKey        string       `json:"-"`
}
