package route

import (
	"context"
	"net"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/speedtest"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/stretchr/testify/require"
)

// TestRouteConnectionRejectsSpeedTestMagicAddress verifies that the core
// router rejects the private speedtest magic destination before any rule
// matching or inbound access happens, so inbounds that never installed the
// speedtest.Router wrapper (disabled, misconfigured, or unsupported) cannot
// have @SpeedTest forwarded to the outside as a normal domain.
func TestRouteConnectionRejectsSpeedTestMagicAddress(t *testing.T) {
	t.Parallel()
	router := &Router{}
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	err := router.routeConnection(context.Background(), serverConn, adapter.InboundContext{
		Destination: M.Socksaddr{Fqdn: speedtest.MagicAddress},
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid speedtest request")
}
