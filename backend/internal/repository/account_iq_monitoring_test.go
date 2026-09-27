package repository

import (
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestIQMonitoringSchedule(t *testing.T) {
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	s := domain.DefaultIQCheck()
	s.IntervalMinutes = 15
	s.Enabled = true
	for i := 1; i <= 6; i++ {
		finishIQSchedule(&s, iqcheck.Grade("21"), now)
		want := 15 * time.Minute
		require.Equal(t, want, s.NextRunAt.Sub(now))
		now = *s.NextRunAt
	}
	finishIQSchedule(&s, iqcheck.Grade("29"), now)
	require.True(t, s.BlocksScheduling())
	require.Equal(t, 15*time.Minute, s.NextRunAt.Sub(now))
	for i := 1; i <= 3; i++ {
		finishIQSchedule(&s, iqcheck.Unknown("timeout"), now)
		require.Equal(t, time.Duration(1<<(i-1))*15*time.Minute, s.NextRunAt.Sub(now))
		require.True(t, s.BlocksScheduling())
	}
	after := now.Add(24 * time.Hour)
	r := iqcheck.Unknown("http_429")
	r.Diagnostic = &iqcheck.Diagnostic{RetryAfter: &after}
	finishIQSchedule(&s, r, now)
	require.Equal(t, after, *s.NextRunAt)
	finishIQSchedule(&s, iqcheck.Unknown("quota_exhausted"), now)
	require.Equal(t, "deferred", s.ExecutionState)
	require.NotNil(t, s.NextRunAt)
	queueIQ(&s, now)
	require.Equal(t, "deferred", s.ExecutionState)
	require.Equal(t, after, *s.NextRunAt)
	r = iqcheck.Unknown("invalid_event_json")
	r.Diagnostic = &iqcheck.Diagnostic{Stage: "parse"}
	s.NotBefore = nil
	for i := 0; i < 3; i++ {
		finishIQSchedule(&s, r, now)
	}
	require.Equal(t, "deferred", s.ExecutionState)
	require.Equal(t, "protocol_cooldown", s.ExecutionReason)
}
func TestIQMonitoringSettingsPreserveGateAndCooldown(t *testing.T) {
	now := time.Now().UTC()
	s := domain.DefaultIQCheck()
	s.Enabled = true
	s.Status = "degraded"
	s.LastRunAt = &now
	delay := now.Add(2 * time.Hour)
	s.NotBefore = &delay
	n := 30
	u := applyIQSettings(s, &domain.IQCheckSettings{IntervalMinutes: &n}, false, now)
	require.True(t, u.BlocksScheduling())
	timeout := 300
	u = applyIQSettings(s, &domain.IQCheckSettings{TimeoutSeconds: &timeout}, false, now)
	require.Equal(t, "unknown", u.Status)
	require.False(t, u.NextRunAt.Before(delay))
}

func TestIQProtocolCooldownRetainsEffectiveAssessment(t *testing.T) {
	now := time.Now().UTC()
	s := domain.DefaultIQCheck()
	s.Enabled = true
	finishIQSchedule(&s, iqcheck.Grade("29"), now)
	valid := *s.LastValidAt
	for i := 1; i <= 4; i++ {
		now = now.Add(time.Hour)
		finishIQSchedule(&s, iqcheck.Unknown("missing_final_message"), now)
		require.True(t, s.BlocksScheduling())
		require.Equal(t, valid, *s.LastValidAt)
		if i == 3 {
			require.Equal(t, 30*time.Minute, s.NextRunAt.Sub(now))
		}
		if i == 4 {
			require.Equal(t, time.Hour, s.NextRunAt.Sub(now))
		}
	}
	now = now.Add(time.Hour)
	finishIQSchedule(&s, iqcheck.Grade("21"), now)
	require.False(t, s.BlocksScheduling())
	require.Zero(t, s.ProtocolFailures)
}

func TestIQForbiddenAutomaticallyResumes(t *testing.T) {
	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	s := domain.DefaultIQCheck()
	s.Enabled = true
	s.IntervalMinutes = 1
	finishIQSchedule(&s, iqcheck.Grade("29"), now)
	for i := 0; i < 4; i++ {
		now = now.Add(time.Hour)
		finishIQSchedule(&s, iqcheck.Unknown("http_403"), now)
		require.NotEqual(t, "paused", s.ExecutionState)
		require.Equal(t, time.Duration(1<<min(i, 2))*time.Minute, s.NextRunAt.Sub(now))
		require.True(t, s.BlocksScheduling())
	}
	finishIQSchedule(&s, iqcheck.Grade("21"), now)
	require.Equal(t, time.Minute, s.NextRunAt.Sub(now))
	require.False(t, s.BlocksScheduling())
	for _, reason := range []string{"permission_denied", "authentication_unavailable"} {
		finishIQSchedule(&s, iqcheck.Unknown(reason), now)
		require.Equal(t, "paused", s.ExecutionState)
		require.Nil(t, s.NextRunAt)
	}
}
