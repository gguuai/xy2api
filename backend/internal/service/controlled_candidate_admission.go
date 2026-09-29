package service

import (
	"context"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

type controlledCandidateAdmission struct {
	controlAllowed  bool
	failureEligible bool
	failureDomains  []string
	healthIdentity  string
}

// The batch path is only preflight. It shares the authoritative identity decoder
// and gate predicate; the selected account is still validated before every send.
func (s *ControlledSchedulingService) candidateAdmissions(ctx context.Context, accounts []*Account, model, sessionID string, accountPool bool) (map[int64]controlledCandidateAdmission, error) {
	result := make(map[int64]controlledCandidateAdmission, len(accounts))
	if len(accounts) == 0 {
		return result, nil
	}
	if batch, ok := any(s.Store).(scheduling.BatchCandidateAdmissionStore); accountPool && ok {
		inputs := make([]scheduling.CandidateAdmissionInput, 0, len(accounts))
		for _, account := range accounts {
			inputs = append(inputs, scheduling.CandidateAdmissionInput{AccountID: account.ID, Model: account.GetMappedModel(model)})
		}
		snapshots, err := batch.ReadCandidateAdmissions(ctx, inputs, sessionID)
		if err != nil {
			return nil, err
		}
		for _, account := range accounts {
			state, exists := snapshots[account.ID]
			if !exists {
				return nil, scheduling.ErrControlNotFound
			}
			value := controlledCandidateAdmission{controlAllowed: state.ControlAllowed, failureEligible: state.FailureEligible, healthIdentity: state.HealthIdentity}
			if state.ControlAllowed {
				frozen := scheduling.FailureAdmission{AccountID: account.ID, Model: account.GetMappedModel(model), Domains: state.Domains}
				value.failureDomains = controlledAccountFailureDomains(account, model)
				for _, pool := range []struct{ kind, id string }{{"quota_pool", state.Domains.QuotaPoolID}, {"availability_pool", state.Domains.AvailabilityPoolID}} {
					if pool.id != "" {
						value.failureDomains = append(value.failureDomains, frozen.SharedKey(pool.kind, pool.id, ""), frozen.SharedKey(pool.kind, pool.id, frozen.Model))
					}
				}
			}
			result[account.ID] = value
		}
		return result, nil
	}
	// Compatibility stores and the older non-pool evaluator keep their individual
	// reads. There is no speculative retry on a real batch database error.
	for _, account := range accounts {
		value := controlledCandidateAdmission{}
		var err error
		value.controlAllowed, err = s.Store.CanAdmitControl(ctx, account.ID, sessionID)
		if err != nil {
			return nil, err
		}
		if !accountPool || value.controlAllowed {
			value.failureEligible, value.failureDomains, err = s.controlledFailureCandidate(ctx, account, model)
			if err != nil {
				return nil, err
			}
			value.healthIdentity, err = s.controlledHealthIdentity(ctx, account)
			if err != nil {
				return nil, err
			}
		}
		result[account.ID] = value
	}
	return result, nil
}
