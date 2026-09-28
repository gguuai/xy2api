package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestControlledHealthIdentityUsesValidatedCredentialOwner(t *testing.T) {
	pid := int64(100)
	parent := &Account{ID: pid, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "upstream-a", "access_token": "refresh-old"}}
	shadow := &Account{ID: 200, ParentAccountID: &pid, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	service := &ControlledSchedulingService{accounts: newStubCredRepo(parent)}
	identity, err := service.controlledHealthIdentity(context.Background(), shadow)
	require.NoError(t, err)
	require.Equal(t, scheduling.StableHealthIdentity(parent.Platform, parent.Type, parent.Credentials, parent.Extra), identity)
	parent.Credentials["access_token"] = "refresh-new"
	refreshed, err := service.controlledHealthIdentity(context.Background(), shadow)
	require.NoError(t, err)
	require.Equal(t, identity, refreshed)
	parent.Credentials["chatgpt_account_id"] = "upstream-b"
	changed, err := service.controlledHealthIdentity(context.Background(), shadow)
	require.NoError(t, err)
	require.NotEqual(t, identity, changed)
	parent.Type = AccountTypeAPIKey
	_, err = service.controlledHealthIdentity(context.Background(), shadow)
	require.Error(t, err)
	require.Equal(t, int64(200), shadow.ID)
	require.Equal(t, pid, *shadow.ParentAccountID)
}

func TestControlledHealthIdentityNormalAccountDoesNotFetchParent(t *testing.T) {
	service := &ControlledSchedulingService{}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "isolated-test-key"}}
	identity, err := service.controlledHealthIdentity(context.Background(), account)
	require.NoError(t, err)
	require.NotEmpty(t, identity)
	_, err = service.controlledHealthIdentity(context.Background(), nil)
	require.ErrorIs(t, err, scheduling.ErrNoCandidate)
}
