package option

// InboundSpeedTestOptions is embedded by inbounds that support the private
// speedtest protocol.
type InboundSpeedTestOptions struct {
	// SpeedTest controls how the inbound handles private speedtest requests
	// addressed to the reserved sing-box or Hysteria 2 compatible destination.
	//
	// Available values:
	//
	// - "" / "disable" (default): the inbound does not intercept speedtest
	//   requests; they are rejected by the core router.
	// - "allow": the inbound serves speedtest requests locally.
	// - "reject": the inbound replies with a protocol-level rejection
	//   instead of serving the test.
	SpeedTest string `json:"speed_test,omitempty"`
}
