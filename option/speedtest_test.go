package option

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// speedTestOptionsCase pairs a zero-value inbound options struct with a
// setter/getter so every embedding struct can be exercised identically.
type speedTestOptionsCase struct {
	name  string
	value any
	get   func(value any) string
}

func TestInboundSpeedTestOptionsJSONRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []speedTestOptionsCase{
		{"SocksInboundOptions", &SocksInboundOptions{InboundSpeedTestOptions: InboundSpeedTestOptions{SpeedTest: "allow"}}, func(v any) string { return v.(*SocksInboundOptions).SpeedTest }},
		{"HTTPMixedInboundOptions", &HTTPMixedInboundOptions{InboundSpeedTestOptions: InboundSpeedTestOptions{SpeedTest: "allow"}}, func(v any) string { return v.(*HTTPMixedInboundOptions).SpeedTest }},
		{"ShadowsocksInboundOptions", &ShadowsocksInboundOptions{InboundSpeedTestOptions: InboundSpeedTestOptions{SpeedTest: "allow"}}, func(v any) string { return v.(*ShadowsocksInboundOptions).SpeedTest }},
		{"VMessInboundOptions", &VMessInboundOptions{InboundSpeedTestOptions: InboundSpeedTestOptions{SpeedTest: "allow"}}, func(v any) string { return v.(*VMessInboundOptions).SpeedTest }},
		{"TrojanInboundOptions", &TrojanInboundOptions{InboundSpeedTestOptions: InboundSpeedTestOptions{SpeedTest: "allow"}}, func(v any) string { return v.(*TrojanInboundOptions).SpeedTest }},
		{"NaiveInboundOptions", &NaiveInboundOptions{InboundSpeedTestOptions: InboundSpeedTestOptions{SpeedTest: "allow"}}, func(v any) string { return v.(*NaiveInboundOptions).SpeedTest }},
		{"HysteriaInboundOptions", &HysteriaInboundOptions{InboundSpeedTestOptions: InboundSpeedTestOptions{SpeedTest: "allow"}}, func(v any) string { return v.(*HysteriaInboundOptions).SpeedTest }},
		{"TUICInboundOptions", &TUICInboundOptions{InboundSpeedTestOptions: InboundSpeedTestOptions{SpeedTest: "allow"}}, func(v any) string { return v.(*TUICInboundOptions).SpeedTest }},
		{"Hysteria2InboundOptions", &Hysteria2InboundOptions{InboundSpeedTestOptions: InboundSpeedTestOptions{SpeedTest: "allow"}}, func(v any) string { return v.(*Hysteria2InboundOptions).SpeedTest }},
		{"VLESSInboundOptions", &VLESSInboundOptions{InboundSpeedTestOptions: InboundSpeedTestOptions{SpeedTest: "allow"}}, func(v any) string { return v.(*VLESSInboundOptions).SpeedTest }},
		{"AnyTLSInboundOptions", &AnyTLSInboundOptions{InboundSpeedTestOptions: InboundSpeedTestOptions{SpeedTest: "allow"}}, func(v any) string { return v.(*AnyTLSInboundOptions).SpeedTest }},
		{"TrustTunnelInboundOptions", &TrustTunnelInboundOptions{InboundSpeedTestOptions: InboundSpeedTestOptions{SpeedTest: "allow"}}, func(v any) string { return v.(*TrustTunnelInboundOptions).SpeedTest }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			content, err := json.Marshal(tc.value)
			require.NoError(t, err)
			require.Contains(t, string(content), `"speed_test":"allow"`)

			var decoded map[string]any
			require.NoError(t, json.Unmarshal(content, &decoded))
			require.Equal(t, "allow", decoded["speed_test"])
		})
	}
}
