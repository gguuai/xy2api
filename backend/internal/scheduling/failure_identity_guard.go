package scheduling

import "context"

// WithCurrentFailureIdentity makes terminal observation linearizable against
// credential and membership writes. The callback must only use its bounded
// context for the Redis observation; it must not update PG account/control rows.
func (s *PostgresStore) WithCurrentFailureIdentity(ctx context.Context, frozen *FailureAdmission, observe func() error) (bool, error) {
	if frozen == nil {
		return true, observe()
	}
	if err := s.ready(); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockFailureIdentity(ctx, tx, frozen.AccountID, frozen.CredentialOwnerID); err != nil {
		return false, err
	}
	current, err := readFailureAdmission(ctx, tx, frozen.AccountID, frozen.Model)
	if err != nil {
		return false, err
	}
	if !sameFailureIdentity(current, *frozen) {
		return false, nil
	}
	if err = observe(); err != nil {
		return true, err
	}
	return true, tx.Commit()
}
