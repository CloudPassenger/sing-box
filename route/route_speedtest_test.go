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
// router rejects private speedtest destinations before rule matching, so
// disabled or unsupported inbounds cannot route them externally.
func TestRouteConnectionRejectsSpeedTestMagicAddress(t *testing.T) {
	t.Parallel()
	for _, destination := range []string{speedtest.MagicAddress, speedtest.LegacyMagicAddress} {
		t.Run(destination, func(t *testing.T) {
			t.Parallel()
			router := &Router{}
			serverConn, clientConn := net.Pipe()
			defer serverConn.Close()
			defer clientConn.Close()

			err := router.routeConnection(context.Background(), serverConn, adapter.InboundContext{
				Destination: M.Socksaddr{Fqdn: destination},
			}, nil)
			require.Error(t, err)
			require.Contains(t, err.Error(), "invalid speedtest request")
		})
	}
}
