//go:build with_utls

package tls

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"

	utls "github.com/metacubex/utls"
	"github.com/stretchr/testify/require"
)

func TestRealityFallbackLimitConfig(t *testing.T) {
	t.Parallel()

	var tlsOptions option.InboundTLSOptions
	err := json.Unmarshal([]byte(`{
		"enabled": true,
		"server_name": "google.com",
		"reality": {
			"enabled": true,
			"handshake": {
				"server": "google.com",
				"server_port": 443
			},
			"private_key": "UuMBgl7MXTPx9inmQp2UC7Jcnwc6XYbwDNebonM-FCc",
			"short_id": ["0123456789abcdef"],
			"max_time_difference": "30s",
			"fallback_limit": {
				"sampling_period": "1s",
				"down_mbps": 4,
				"down_burst_mbps": 6,
				"up_mbps": 4,
				"up_burst_mbps": 6
			}
		}
	}`), &tlsOptions)
	require.NoError(t, err)

	require.Equal(t, utls.RealityLimitFallback{
		BytesPerSec:      4 * 125000,
		BurstBytesPerSec: 6 * 125000,
	}, buildRealityLimitFallback(tlsOptions.Reality.FallbackLimit.UpMbps, tlsOptions.Reality.FallbackLimit.UpBurstMbps, tlsOptions.Reality.FallbackLimit.SamplingPeriod.Build()))
	require.Equal(t, utls.RealityLimitFallback{
		BytesPerSec:      4 * 125000,
		BurstBytesPerSec: 6 * 125000,
	}, buildRealityLimitFallback(tlsOptions.Reality.FallbackLimit.DownMbps, tlsOptions.Reality.FallbackLimit.DownBurstMbps, tlsOptions.Reality.FallbackLimit.SamplingPeriod.Build()))
	require.Equal(t, 30*time.Second, tlsOptions.Reality.MaxTimeDifference.Build())
}

func TestRealityFallbackLimitDefaultBurst(t *testing.T) {
	t.Parallel()

	rate := 4.0
	require.Equal(t, utls.RealityLimitFallback{
		BytesPerSec:      4 * 125000,
		BurstBytesPerSec: 2 * 4 * 125000,
	}, buildRealityLimitFallback(&rate, nil, 2*time.Second))
}
