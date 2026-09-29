package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

// Match the actual credential owner used by forwarding. Logical account/family
// capacity stays keyed by the selected account; only health identity follows the
// validated OpenAI OAuth parent for a credential-less Spark shadow.
func (s *ControlledSchedulingService) controlledHealthIdentity(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", scheduling.ErrNoCandidate
	}
	if s == nil || (account.IsShadow() && s.accounts == nil) {
		return "", scheduling.ErrSharedState
	}
	owner, err := resolveCredentialAccount(ctx, s.accounts, account)
	if err != nil {
		return "", err
	}
	if owner == nil {
		return "", scheduling.ErrNoCandidate
	}
	return scheduling.StableHealthIdentity(owner.Platform, owner.Type, owner.Credentials, owner.Extra), nil
}
