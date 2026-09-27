package domain

import (
	"testing"
	"time"
)

func TestIQCurrentHealthTransitions(t *testing.T) {
	now := time.Now().UTC()
	for _, previous := range []string{"smart", "degraded"} {
		for _, reason := range []string{"response_too_large", "http_503", "quota_exhausted", "timeout", "unparseable_answer"} {
			s := DefaultIQCheck()
			s.Enabled = true
			s.ObserveResult(previous, "fixture", now)
			s.ObserveResult("unknown", reason, now.Add(time.Second))
			if s.Status != "unknown" || s.Summary().Status != "unknown" || s.Reason != reason || !s.BlocksScheduling() {
				t.Fatalf("%s -> %s: %+v", previous, reason, s)
			}
			if s.LastValidStatus != previous || !s.LastValidAt.Equal(now) {
				t.Fatalf("lost historical evidence: %+v", s)
			}
			s.ObserveResult("smart", "correct_answer", now.Add(2*time.Second))
			if s.Status != "smart" || s.BlocksScheduling() {
				t.Fatal(s)
			}
			s.ObserveResult("degraded", "wrong_answer", now.Add(3*time.Second))
			if !s.BlocksScheduling() {
				t.Fatal(s)
			}
			s.Enabled = false
			if s.BlocksScheduling() || s.Summary().Status != "unknown" {
				t.Fatal(s)
			}
		}
	}
	legacy := IQCheck{Enabled: true, Status: "smart", Reason: "correct_answer", LastRunStatus: "unknown", LastRunReason: "http_503", LastValidAt: &now}
	if s := legacy.Summary(); s.Status != "unknown" || s.LastValidStatus != "smart" || !s.SchedulingBlocked {
		t.Fatal(s)
	}
	if s := (IQCheck{Enabled: true, Status: "unknown"}); s.BlocksScheduling() {
		t.Fatal("never-tested account must not be mistaken for a failed probe")
	}
}
