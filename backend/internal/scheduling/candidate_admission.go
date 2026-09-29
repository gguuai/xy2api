package scheduling

import "context"

// CandidateAdmissionInput contains only locally eligible accounts. Model is the
// actual mapped upstream model; priority and weight remain account settings.
type CandidateAdmissionInput struct {
	AccountID int64
	Model     string
}

// CandidateAdmission is a fresh read-only preflight, never permission to send.
// BeginDispatch must still lock and recheck control, identity, capacity and gates.
type CandidateAdmission struct {
	FamilyID        int64
	ControlAllowed  bool
	FailureEligible bool
	Domains         AccountFailureDomains
	HealthIdentity  string
}

// BatchCandidateAdmissionStore is optional so alternate stores retain the
// individual read path. No preflight result is cached across requests.
type BatchCandidateAdmissionStore interface {
	ReadCandidateAdmissions(context.Context, []CandidateAdmissionInput, string) (map[int64]CandidateAdmission, error)
}
