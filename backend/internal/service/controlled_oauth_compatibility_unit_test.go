//go:build unit

package service

import (
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

func TestControlledOAuth401KeepsCredentialRefreshProtection(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformGemini, PlatformAntigravity} {
		t.Run(platform, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			invalidator := &tokenCacheInvalidatorRecorder{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			svc.SetTokenCacheInvalidator(invalidator)
			account := &Account{ID: 734, Platform: platform, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "test-refresh-token"}}
			require.True(t, svc.HandleUpstreamError(controlledLegacyTestContext(), account, http.StatusUnauthorized, http.Header{}, []byte("expired access token")))
			require.Len(t, invalidator.accounts, 1)
			require.Equal(t, 1, repo.tempCalls)
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.updateCredentialsCalls, "old snapshots must not overwrite a refreshed token")
			if platform == PlatformAntigravity {
				require.Equal(t, 1, repo.updateExtraCalls)
			}
		})
	}
}
