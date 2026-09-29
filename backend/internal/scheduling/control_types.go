package scheduling

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const (
	ScopeAccount     = "logical_account"
	ScopeFamily      = "credential_family"
	ControlRunning   = "RUNNING"
	ControlDraining  = "DRAINING"
	ControlPaused    = "PAUSED"
	ControlUncertain = "DRAIN_UNCERTAIN"
)

var (
	ErrVersionConflict = errors.New("scheduling version conflict")
	ErrControlBlocked  = errors.New("account dispatch blocked by administrative control")
	ErrControlNotFound = errors.New("scheduling account not found")
	ErrInvalidControl  = errors.New("invalid scheduling control operation")
	ErrAttemptIdentity = errors.New("dispatch ticket identity conflict")
)

type PolicyRecord struct {
	GroupID     int64               `json:"group_id"`
	Model       string              `json:"model"`
	Version     int64               `json:"version"`
	Policy      *Policy             `json:"policy"`
	Diagnostics []ProfileDiagnostic `json:"diagnostics,omitempty"`
}
type ControlSnapshot struct {
	AccountID           int64      `json:"account_id"`
	SubjectID           int64      `json:"subject_id"`
	Scope               string     `json:"scope"`
	State               string     `json:"state"`
	Epoch               int64      `json:"epoch"`
	Mode                string     `json:"mode"`
	ActiveAttempts      int64      `json:"active_attempts"`
	UnknownAttempts     int64      `json:"unknown_attempts"`
	AllowedSessions     int64      `json:"allowed_sessions"`
	PendingSettlements  int64      `json:"pending_settlements"`
	UpdatedAt           time.Time  `json:"updated_at"`
	SessionDeadline     *time.Time `json:"session_deadline"`
	SessionTurnLimit    int        `json:"session_turn_limit"`
	NotificationWarning string     `json:"notification_warning,omitempty"`
}
type ControlCommand struct {
	AccountID              int64  `json:"-"`
	Action                 string `json:"action"`
	Scope                  string `json:"scope"`
	ExpectedEpoch          *int64 `json:"expected_epoch,omitempty"`
	SessionDurationSeconds int    `json:"session_duration_seconds,omitempty"`
	SessionMaxTurns        int    `json:"session_max_turns,omitempty"`
}
type DispatchRequest struct {
	Failure         *FailureAdmission
	HardConcurrency int
	TicketID        string
	RequestID       string
	AccountID       int64
	// FamilyID is verified against the persisted parent.
	FamilyID      int64
	SessionID     string
	NodeID        string
	LeaseDuration time.Duration
}
type DispatchTicket struct {
	Failure      *FailureAdmission
	TicketID     string
	RequestID    string
	AccountID    int64
	FamilyID     int64
	AccountEpoch int64
	FamilyEpoch  int64
	LeaseUntil   time.Time
}
type SchedulingAdminStore interface {
	GetPolicy(context.Context, int64, string) (PolicyRecord, error)
	PutPolicy(context.Context, Policy, int64) (PolicyRecord, error)
	GetControl(context.Context, int64, string) (ControlSnapshot, error)
	Control(context.Context, ControlCommand) (ControlSnapshot, error)
}

// Hook receives the committed epoch. Success is not proof of upstream cancellation.
type CancelHook func(context.Context, ControlSnapshot) error

// AttemptRecord exposes identities and timing only; prompts, credentials, and raw errors
// are never part of its metrics whitelist.
type AttemptRecord struct {
	TicketID        string          `json:"ticket_id"`
	RequestID       string          `json:"request_id"`
	AccountID       int64           `json:"account_id"`
	FamilyID        int64           `json:"family_id"`
	State           string          `json:"state"`
	Outcome         string          `json:"outcome"`
	CancelRequested bool            `json:"cancel_requested"`
	UsagePending    bool            `json:"usage_pending"`
	DispatchedAt    time.Time       `json:"dispatched_at"`
	SettledAt       *time.Time      `json:"settled_at"`
	Metrics         json.RawMessage `json:"metrics"`
}
type SchedulingAttemptReader interface {
	ListRequestAttempts(context.Context, string) ([]AttemptRecord, error)
}

type AccountDispatchStats struct {
	AccountID          int64            `json:"account_id"`
	OrdinaryFirst      int64            `json:"ordinary_first"`
	OrdinaryFirstShare *float64         `json:"ordinary_first_share"`
	ByKind             map[string]int64 `json:"by_kind"`
}
type DispatchStats struct {
	GroupID            int64                  `json:"group_id"`
	Model              string                 `json:"model"`
	Since              time.Time              `json:"since"`
	Until              time.Time              `json:"until"`
	OrdinaryFirstTotal int64                  `json:"ordinary_first_total"`
	Accounts           []AccountDispatchStats `json:"accounts"`
}
type SchedulingStatsReader interface {
	DispatchStatistics(context.Context, int64, string, time.Time, time.Time) (DispatchStats, error)
}
