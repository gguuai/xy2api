package scheduling

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRetryJSONPartialConfigPreservesDefaults(t *testing.T) {
	var p Policy
	require.NoError(t, json.Unmarshal([]byte("{\"model\":\"m\",\"retry\":{\"max_attempts\":3}}"), &p))
	p = NormalizePolicy(p)
	require.Equal(t, DefaultRetryPolicy(), p.Retry, "omitted retry fields must preserve default recovery behavior")
}

func TestRetryJSONOmittedEmptyPartialAndExplicitValues(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		explicit  bool
	}{
		{"omitted", "{\"model\":\"m\"}", false},
		{"empty", "{\"model\":\"m\",\"retry\":{}}", false},
		{"null", "{\"model\":\"m\",\"retry\":null}", false},
		{"partial", "{\"model\":\"m\",\"retry\":{\"max_attempts\":3}}", false},
		{"explicit_false_zero", "{\"model\":\"m\",\"retry\":{\"reserve_fallback\":false,\"cross_tier\":false,\"max_after_timeout\":0,\"switch_margin_ms\":0}}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p Policy
			require.NoError(t, json.Unmarshal([]byte(tc.raw), &p))
			p = NormalizePolicy(p)
			want := DefaultRetryPolicy()
			if tc.explicit {
				want.ReserveFallback = false
				want.CrossTier = false
				want.MaxAfterTimeout = 0
				want.SwitchMarginMS = 0
			}
			require.Equal(t, want, p.Retry)
			raw, e := json.Marshal(p)
			require.NoError(t, e)
			var roundtrip Policy
			require.NoError(t, json.Unmarshal(raw, &roundtrip))
			require.Equal(t, p, roundtrip)
		})
	}
}
func TestRetryJSONDoesNotChangeGoConstructionOrMalformedReceiver(t *testing.T) {
	goPolicy := NormalizePolicy(Policy{Retry: RetryPolicy{MaxAttempts: 3}})
	require.False(t, goPolicy.Retry.ReserveFallback)
	require.False(t, goPolicy.Retry.CrossTier)
	require.Zero(t, goPolicy.Retry.MaxAfterTimeout)
	require.Zero(t, goPolicy.Retry.SwitchMarginMS)
	original := DefaultRetryPolicy()
	retry := original
	require.Error(t, json.Unmarshal([]byte("{\"max_attempts\":\"bad\"}"), &retry))
	require.Equal(t, original, retry)
}
