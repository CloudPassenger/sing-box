package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/speedtest"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

func TestFormatSpeedBits(t *testing.T) {
	t.Parallel()
	// 125000 bytes/s == 1,000,000 bits/s == 1 Mbps.
	require.Equal(t, "1 Mbps", formatSpeed(125000, time.Second, false))
}

func TestFormatSpeedBytes(t *testing.T) {
	t.Parallel()
	require.Equal(t, "125 kB/s", formatSpeed(125000, time.Second, true))
}

func TestFormatSpeedZeroDuration(t *testing.T) {
	t.Parallel()
	require.Equal(t, "N/A", formatSpeed(1000, 0, false))
}

func TestProgress(t *testing.T) {
	t.Parallel()
	require.Equal(t, "50.00%", progress(50, 100))
	require.Equal(t, "0.00%", progress(0, 0))
}

// fakeSpeedTestRouter is a minimal adapter.ConnectionRouterEx that only
// serves the private speedtest protocol, used to back a local TCP listener
// for exercising the CLI's dialer usage end-to-end.
type fakeSpeedTestRouter struct{}

func (r *fakeSpeedTestRouter) RouteConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext) error {
	return nil
}

func (r *fakeSpeedTestRouter) RoutePacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext) error {
	return nil
}

func (r *fakeSpeedTestRouter) RouteConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	_ = conn.Close()
}

func (r *fakeSpeedTestRouter) RoutePacketConnectionEx(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
}

// speedTestDialer dials the given fixed listener address for any
// destination, letting tests point the CLI at a local speedtest server
// regardless of the requested magic FQDN.
type speedTestDialer struct {
	addr string
}

func (d *speedTestDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return net.Dial(network, d.addr)
}

func (d *speedTestDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return net.ListenPacket("udp", "127.0.0.1:0")
}

func startSpeedTestServer(t *testing.T, mode string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	router, err := speedtest.NewRouter(&fakeSpeedTestRouter{}, logger.NOP(), mode)
	require.NoError(t, err)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go router.RouteConnectionEx(context.Background(), conn, adapter.InboundContext{
				Destination: M.Socksaddr{Fqdn: speedtest.MagicAddress},
			}, nil)
		}
	}()
	return listener.Addr().String()
}

func TestRunSpeedTestBothDirections(t *testing.T) {
	t.Parallel()
	addr := startSpeedTestServer(t, "allow")
	dialer := &speedTestDialer{addr: addr}
	err := runSpeedTest(context.Background(), dialer, speedTestCommandOptions{
		dataSize: 4096,
		timeout:  5 * time.Second,
		quiet:    true,
	})
	require.NoError(t, err)
}

func TestRunSpeedTestSkipBothDirectionsErrors(t *testing.T) {
	t.Parallel()
	dialer := &speedTestDialer{addr: "127.0.0.1:0"}
	err := runSpeedTest(context.Background(), dialer, speedTestCommandOptions{
		skipUpload:   true,
		skipDownload: true,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no speedtest direction enabled")
}

func TestRunSpeedTestSkipDownloadOnly(t *testing.T) {
	t.Parallel()
	addr := startSpeedTestServer(t, "allow")
	dialer := &speedTestDialer{addr: addr}
	err := runSpeedTest(context.Background(), dialer, speedTestCommandOptions{
		skipDownload: true,
		dataSize:     4096,
		timeout:      5 * time.Second,
		quiet:        true,
	})
	require.NoError(t, err)
}

func TestRunSpeedTestSkipUploadOnly(t *testing.T) {
	t.Parallel()
	addr := startSpeedTestServer(t, "allow")
	dialer := &speedTestDialer{addr: addr}
	err := runSpeedTest(context.Background(), dialer, speedTestCommandOptions{
		skipUpload: true,
		dataSize:   4096,
		timeout:    5 * time.Second,
		quiet:      true,
	})
	require.NoError(t, err)
}

func TestRunSpeedTestRejected(t *testing.T) {
	t.Parallel()
	addr := startSpeedTestServer(t, "reject")
	dialer := &speedTestDialer{addr: addr}
	err := runSpeedTest(context.Background(), dialer, speedTestCommandOptions{
		dataSize: 4096,
		timeout:  5 * time.Second,
		quiet:    true,
	})
	require.Error(t, err)
}
