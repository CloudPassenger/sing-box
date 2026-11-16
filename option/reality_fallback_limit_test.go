package option

import (
	"testing"

	"github.com/sagernet/sing/common/json"

	"github.com/stretchr/testify/require"
)

func TestRealityFallbackLimitUnmarshal(t *testing.T) {
	t.Parallel()

	var options InboundRealityOptions
	err := json.Unmarshal([]byte(`{"fallback_limit":{"sampling_period":"1s","down_mbps":8,"down_burst_mbps":10}}`), &options)
	require.NoError(t, err)
	require.NotNil(t, options.FallbackLimit)
	require.NotNil(t, options.FallbackLimit.DownMbps)
	require.Equal(t, float64(8), *options.FallbackLimit.DownMbps)
	require.NotNil(t, options.FallbackLimit.DownBurstMbps)
	require.Equal(t, float64(10), *options.FallbackLimit.DownBurstMbps)
}

func TestRealityFallbackLimitRejectsUnknownField(t *testing.T) {
	t.Parallel()

	// A silently ignored typo leaves the fallback unlimited while the
	// configuration looks like it applies a limit.
	var options InboundRealityOptions
	err := json.Unmarshal([]byte(`{"fallback_limit":{"max_connections":8}}`), &options)
	require.Error(t, err)
}
