package scheduling

import (
	"context"
	"database/sql"
	"time"

	"github.com/lib/pq"
)

type candidateAdmissionRecord struct {
	admission             FailureAdmission
	familyID              int64
	schedulable           bool
	status                string
	owner                 sql.NullInt64
	credentials, extra    []byte
	platform, accountType string
}

// ReadCandidateAdmissions uses two bounded database round trips regardless of
// candidate count: account identity and account-local failure gates.
// Reads are synchronous and request-scoped. Dispatch revalidates under its locks.
func (s *PostgresStore) ReadCandidateAdmissions(ctx context.Context, inputs []CandidateAdmissionInput, sessionID string) (map[int64]CandidateAdmission, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return readCandidateAdmissions(ctx, s.db, inputs, sessionID)
}

func readCandidateAdmissions(ctx context.Context, q sqlQueryer, inputs []CandidateAdmissionInput, sessionID string) (map[int64]CandidateAdmission, error) {
	result := make(map[int64]CandidateAdmission, len(inputs))
	if len(inputs) == 0 {
		return result, nil
	}
	models := make(map[int64]string, len(inputs))
	ids := make([]int64, 0, len(inputs))
	for _, input := range inputs {
		if input.AccountID <= 0 {
			return nil, ErrInvalidControl
		}
		if model, exists := models[input.AccountID]; exists {
			if model != input.Model {
				return nil, ErrInvalidControl
			}
			continue
		}
		models[input.AccountID] = input.Model
		ids = append(ids, input.AccountID)
	}
	rows, err := q.QueryContext(ctx, "SELECT a.id,COALESCE(a.parent_account_id,a.id),a.schedulable,a.status,c.id,c.credentials,COALESCE(c.extra,'{}'::jsonb),COALESCE(c.platform,''),COALESCE(c.type,''),0,''::text,''::text FROM accounts a LEFT JOIN accounts c ON c.id=COALESCE(a.parent_account_id,a.id) AND c.deleted_at IS NULL AND c.parent_account_id IS NULL AND (a.parent_account_id IS NULL OR (c.platform='openai' AND c.type='oauth')) WHERE a.id=ANY($1) AND a.deleted_at IS NULL ORDER BY a.id", pq.Array(ids))
	if err != nil {
		return nil, err
	}
	records := make(map[int64]*candidateAdmissionRecord, len(ids))
	for rows.Next() {
		record := &candidateAdmissionRecord{}
		a := &record.admission
		if err = rows.Scan(&a.AccountID, &record.familyID, &record.schedulable, &record.status, &record.owner,
			&record.credentials, &record.extra, &record.platform, &record.accountType,
			&a.Domains.Version, &a.Domains.QuotaPoolID, &a.Domains.AvailabilityPoolID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		a.Model, a.Domains.AccountID = models[a.AccountID], a.AccountID
		records[a.AccountID] = record

	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if len(records) != len(ids) {
		return nil, ErrControlNotFound
	}

	keys := make([]string, 0, len(ids)*2)
	keySeen := make(map[string]bool, len(ids)*2)
	for _, id := range ids {
		record := records[id]
		allowed := record.schedulable && record.status == "active"
		item := CandidateAdmission{FamilyID: record.familyID, ControlAllowed: allowed}
		if allowed {
			if !record.owner.Valid {
				return nil, ErrControlNotFound
			}
			a := record.admission
			a.CredentialOwnerID = record.owner.Int64
			a, err = decodeFailureAdmission(a, record.credentials, record.extra, record.platform, record.accountType)
			if err != nil {
				return nil, err
			}
			record.admission = a
			item.FailureEligible, item.Domains, item.HealthIdentity = true, a.Domains, a.HealthIdentity
			for _, key := range a.Keys {
				if !keySeen[key] {
					keySeen[key] = true
					keys = append(keys, key)
				}
			}
		}
		result[id] = item
	}
	if len(keys) == 0 {
		return result, nil
	}
	gates, err := failureGates(ctx, q, keys)
	if err != nil {
		return nil, err
	}
	blocked := make(map[string]bool, len(gates))
	now := time.Now()
	for _, gate := range gates {
		blocked[gate.Key] = gate.Blocked || gate.ProbeActive || (gate.ReadyAfter != nil && gate.ReadyAfter.After(now))
	}
	for _, id := range ids {
		item := result[id]
		if !item.ControlAllowed {
			continue
		}
		for _, key := range records[id].admission.Keys {
			if blocked[key] {
				item.FailureEligible = false
				break
			}
		}
		result[id] = item
	}
	return result, nil
}
