package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const DefaultIQModel = "gpt-6-astra"
const DefaultIQEffort = "low"

type IQProfile struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	OutputMode      string `json:"output_mode"`
}

func (p IQProfile) Defaults() IQProfile {
	if p.Model == "" {
		p.Model = DefaultIQModel
	}
	if p.ReasoningEffort == "" {
		p.ReasoningEffort = DefaultIQEffort
	}
	if p.OutputMode == "" {
		p.OutputMode = "compat"
	}
	return p
}

type IQCheckSettings struct {
	TimeoutSeconds  *int    `json:"timeout_seconds,omitempty"`
	Enabled         *bool   `json:"enabled,omitempty"`
	IntervalMinutes *int    `json:"interval_minutes,omitempty"`
	Model           *string `json:"model,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	OutputMode      *string `json:"output_mode,omitempty"`
}

var iqEffortPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

func (s *IQCheckSettings) Validate() error {
	if s == nil {
		return nil
	}

	if s.TimeoutSeconds != nil && (*s.TimeoutSeconds < 30 || *s.TimeoutSeconds > 300) {
		return fmt.Errorf("timeout_seconds must be between 30 and 300")
	}
	if s.IntervalMinutes != nil && (*s.IntervalMinutes < 1 || *s.IntervalMinutes > 1440) {
		return fmt.Errorf("interval_minutes must be between 1 and 1440")
	}
	if s.Model != nil {
		*s.Model = strings.TrimSpace(*s.Model)
		if *s.Model == "" || len(*s.Model) > 256 || strings.IndexFunc(*s.Model, unicode.IsControl) >= 0 {
			return fmt.Errorf("model must contain 1 to 256 bytes without control characters")
		}
	}
	if s.ReasoningEffort != nil {
		*s.ReasoningEffort = strings.TrimSpace(*s.ReasoningEffort)
		if !iqEffortPattern.MatchString(*s.ReasoningEffort) {
			return fmt.Errorf("invalid reasoning_effort")
		}
	}
	if s.OutputMode != nil && *s.OutputMode != "compat" && *s.OutputMode != "strict" {
		return fmt.Errorf("output_mode must be compat or strict")
	}
	return nil
}

func (s IQCheck) Profile() IQProfile {
	return (IQProfile{s.Model, s.ReasoningEffort, s.OutputMode}).Defaults()
}

func (s IQCheck) WithSettings(p *IQCheckSettings) IQCheck {
	profile := s.Profile()
	s.Model, s.ReasoningEffort, s.OutputMode = profile.Model, profile.ReasoningEffort, profile.OutputMode
	if s.IntervalMinutes == 0 {
		s.IntervalMinutes = 15
	}
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = 120
	}
	if p == nil {
		return s
	}
	if p.TimeoutSeconds != nil {
		s.TimeoutSeconds = *p.TimeoutSeconds
	}
	if p.Enabled != nil {
		s.Enabled = *p.Enabled
	}
	if p.IntervalMinutes != nil {
		s.IntervalMinutes = *p.IntervalMinutes
	}
	if p.Model != nil {
		s.Model = *p.Model
	}
	if p.ReasoningEffort != nil {
		s.ReasoningEffort = *p.ReasoningEffort
	}
	if p.OutputMode != nil {
		s.OutputMode = *p.OutputMode
	}
	return s
}

// IQCheck keeps the independent quality gate separate from manual scheduling.
type IQCheck struct {
	LastValidStatus   string     `json:"last_valid_status,omitempty"`
	LastValidReason   string     `json:"last_valid_reason,omitempty"`
	SchedulingBlocked bool       `json:"scheduling_blocked,omitempty"`
	RoundStartedAt    *time.Time `json:"round_started_at,omitempty"`
	LastValidAt       *time.Time `json:"last_valid_at,omitempty"`
	LastRunStatus     string     `json:"last_run_status,omitempty"`
	LastRunReason     string     `json:"last_run_reason,omitempty"`
	RoundID           string     `json:"round_id,omitempty"`
	RoundDeadline     *time.Time `json:"round_deadline,omitempty"`
	AttemptCount      int        `json:"attempt_count,omitempty"`
	RetryAt           *time.Time `json:"retry_at,omitempty"`
	BusyDeferrals     int        `json:"busy_deferrals,omitempty"`
	LastAttemptAt     *time.Time `json:"last_attempt_at,omitempty"`
	ExecutionState    string     `json:"execution_state,omitempty"`
	ExecutionReason   string     `json:"execution_reason,omitempty"`
	TaskID            string     `json:"task_id,omitempty"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	NotBefore         *time.Time `json:"not_before,omitempty"`
	FailureStreak     int        `json:"failure_streak,omitempty"`
	ProtocolFailures  int        `json:"protocol_failures,omitempty"`
	Freshness         string     `json:"freshness,omitempty"`
	NextEligibleAt    *time.Time `json:"next_eligible_at,omitempty"`

	TimeoutSeconds  int        `json:"timeout_seconds"`
	Enabled         bool       `json:"enabled"`
	IntervalMinutes int        `json:"interval_minutes"`
	Model           string     `json:"model"`
	ReasoningEffort string     `json:"reasoning_effort"`
	OutputMode      string     `json:"output_mode"`
	Status          string     `json:"status"`
	Reason          string     `json:"reason,omitempty"`
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`
	NextRunAt       *time.Time `json:"next_run_at,omitempty"`
	Revision        string     `json:"revision,omitempty"`
	LeaseToken      string     `json:"lease_token,omitempty"`
	LeaseUntil      *time.Time `json:"lease_until,omitempty"`
}

func DefaultIQCheck() IQCheck {
	return (IQCheck{IntervalMinutes: 5, Status: "unknown"}).WithSettings(nil)
}

func (s IQCheck) Summary() IQCheck {
	s = s.WithSettings(nil).CurrentAssessment()
	s.SchedulingBlocked = s.BlocksScheduling()
	if s.IntervalMinutes == 0 {
		s.IntervalMinutes = 15
	}
	if s.Status == "" || !s.Enabled {
		s.Status = "unknown"
	}

	now := time.Now().UTC()
	s.Freshness = "never_checked"
	if s.LastValidAt != nil {
		s.Freshness = "fresh"
		if now.After(s.LastValidAt.Add(s.EffectiveInterval() + time.Duration(s.TimeoutSeconds)*time.Second + time.Minute)) {
			s.Freshness = "stale"
		}
	}
	s.NextEligibleAt = s.NextRunAt
	if s.ExecutionState == "" {
		s.ExecutionState = "idle"
	}
	s.Revision, s.LeaseToken, s.LeaseUntil = "", "", nil
	s.RoundID = ""
	return s
}

// CurrentAssessment upgrades legacy last-valid-only state without losing history.
func (s IQCheck) CurrentAssessment() IQCheck {
	if s.LastValidStatus == "" && (s.Status == "smart" || s.Status == "degraded") {
		s.LastValidStatus, s.LastValidReason = s.Status, s.Reason
	}
	if s.LastRunStatus == "unknown" {
		s.Status, s.Reason = "unknown", s.LastRunReason
	}
	return s
}

// ObserveResult updates current health on every finished attempt, including retries.
// Only a valid candy answer restores a failed account's quality gate.
func (s *IQCheck) ObserveResult(status, reason string, now time.Time) {
	*s = s.CurrentAssessment()
	if status != "smart" && status != "degraded" {
		status = "unknown"
	}
	s.Status, s.Reason = status, reason
	s.LastRunStatus, s.LastRunReason, s.LastRunAt = status, reason, &now
	if status == "smart" || status == "degraded" {
		s.LastValidStatus, s.LastValidReason, s.LastValidAt = status, reason, &now
	}
}

func (s IQCheck) BlocksScheduling() bool {
	return s.Enabled && (s.Status == "degraded" || s.LastRunStatus == "unknown")
}

// CopySettings carries only editable configuration; duplicates and imports start disabled.
func (s IQCheck) CopySettings() *IQCheckSettings {
	s = s.WithSettings(nil)
	enabled := false
	return &IQCheckSettings{TimeoutSeconds: &s.TimeoutSeconds, Enabled: &enabled, IntervalMinutes: &s.IntervalMinutes, Model: &s.Model, ReasoningEffort: &s.ReasoningEffort, OutputMode: &s.OutputMode}
}

func (s IQCheck) EffectiveInterval() time.Duration {
	return time.Duration(s.WithSettings(nil).IntervalMinutes) * time.Minute
}

// Eligibility is independent of the assessment and survives toggles and profile edits.
func (s IQCheck) Eligibility(now time.Time) (time.Time, string) {
	next := now
	reason := ""
	if s.RetryAt == nil && s.LastRunAt != nil {
		t := s.LastRunAt.Add(time.Duration(max(1, s.IntervalMinutes)) * time.Minute)
		if t.After(next) {
			next = t
			reason = "minimum_interval"
		}
	}
	if s.RetryAt == nil && s.LastAttemptAt != nil {
		t := s.LastAttemptAt.Add(time.Duration(max(1, s.IntervalMinutes)) * time.Minute)
		if t.After(next) {
			next, reason = t, "minimum_interval"
		}
	}
	if s.RetryAt != nil && s.RetryAt.After(next) {
		next = *s.RetryAt
		reason = "retry_wait"
	}
	if s.NotBefore != nil && s.NotBefore.After(next) {
		next = *s.NotBefore
		reason = "upstream_cooldown"
	}
	return next, reason
}
