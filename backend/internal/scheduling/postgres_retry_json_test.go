package scheduling

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPostgresRetryJSONRoundtripPreservesOmittedAndExplicit(t *testing.T) {
	s, _ := isolatedControlStore(t)
	ctx := context.Background()
	for _, raw := range []string{"{\"group_id\":7,\"model\":\"retry-defaults\",\"retry\":{\"max_attempts\":3}}", "{\"group_id\":7,\"model\":\"retry-explicit\",\"retry\":{\"reserve_fallback\":false,\"cross_tier\":false,\"max_after_timeout\":0,\"switch_margin_ms\":0}}"} {
		var p Policy
		require.NoError(t, json.Unmarshal([]byte(raw), &p))
		want := p.Retry
		saved, e := s.PutPolicy(ctx, p, 0)
		require.NoError(t, e)
		loaded, e := s.GetPolicy(ctx, p.GroupID, p.Model)
		require.NoError(t, e)
		require.Equal(t, saved.Version, loaded.Version)
		require.Equal(t, want, loaded.Policy.Retry)
		require.Equal(t, want, NormalizePolicy(*loaded.Policy).Retry)
	}
}
