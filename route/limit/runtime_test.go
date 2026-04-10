package limit

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/stretchr/testify/require"
)

func TestRuntimeUserScopeClientLimitRelease(t *testing.T) {
	t.Parallel()
	runtime, err := NewRuntime(Options{
		Scope:   C.LimitScopeUser,
		Clients: 1,
		RuleID:  "rule-user-limit",
	})
	require.NoError(t, err)

	metadata1 := testMetadata("alice", "in-a", "203.0.113.1")
	metadata2 := testMetadata("alice", "in-a", "203.0.113.2")

	conn1, peer1 := net.Pipe()
	wrapped1, err := runtime.WrapConnection(context.Background(), conn1, metadata1)
	require.NoError(t, err)

	conn2, peer2 := net.Pipe()
	_, err = runtime.WrapConnection(context.Background(), conn2, metadata2)
	require.ErrorContains(t, err, "client limit exceeded")

	require.NoError(t, wrapped1.Close())
	require.NoError(t, peer1.Close())
	require.NoError(t, conn2.Close())
	require.NoError(t, peer2.Close())

	conn3, peer3 := net.Pipe()
	wrapped3, err := runtime.WrapConnection(context.Background(), conn3, metadata2)
	require.NoError(t, err)
	require.NoError(t, wrapped3.Close())
	require.NoError(t, peer3.Close())
}

func TestRuntimeStackedLimitsUseStrictestIntersection(t *testing.T) {
	t.Parallel()
	userRuntime, err := NewRuntime(Options{
		Scope:   C.LimitScopeUser,
		Clients: 2,
		RuleID:  "rule-user",
	})
	require.NoError(t, err)
	ruleRuntime, err := NewRuntime(Options{
		Scope:   C.LimitScopeRule,
		Clients: 1,
		RuleID:  "shared-rule",
	})
	require.NoError(t, err)

	metadataAlice1 := testMetadata("alice", "in-a", "203.0.113.1")
	metadataAlice2 := testMetadata("alice", "in-a", "203.0.113.2")

	conn1, peer1 := net.Pipe()
	wrapped1, err := wrapSequentially([]*Runtime{userRuntime, ruleRuntime}, conn1, metadataAlice1)
	require.NoError(t, err)

	conn2, peer2 := net.Pipe()
	_, err = wrapSequentially([]*Runtime{userRuntime, ruleRuntime}, conn2, metadataAlice2)
	require.ErrorContains(t, err, "client limit exceeded")

	require.NoError(t, wrapped1.Close())
	require.NoError(t, peer1.Close())
	require.NoError(t, conn2.Close())
	require.NoError(t, peer2.Close())
}

func TestRuntimeClientsCannotUseSourceIPScope(t *testing.T) {
	t.Parallel()
	_, err := NewRuntime(Options{
		Scope:   C.LimitScopeSourceIP,
		Clients: 1,
		RuleID:  "bad-rule",
	})
	require.ErrorContains(t, err, "clients cannot be used with scope=source_ip")
}

func TestRuntimeBandwidthLimitSlowsBufferedCopy(t *testing.T) {
	t.Parallel()
	total := 1.0
	runtime, err := NewRuntime(Options{
		Scope:          C.LimitScopeRule,
		TotalMbps:      &total,
		SamplingPeriod: 100 * time.Millisecond,
		RuleID:         "rule-bandwidth",
	})
	require.NoError(t, err)

	conn, peer := net.Pipe()
	wrapped, err := runtime.WrapConnection(context.Background(), conn, testMetadata("alice", "in-a", "203.0.113.1"))
	require.NoError(t, err)
	defer wrapped.Close()
	defer peer.Close()

	go func() {
		_, _ = io.Copy(io.Discard, peer)
	}()

	payload := bytes.Repeat([]byte("x"), 256*1024)
	started := time.Now()
	_, err = bufio.Copy(wrapped, bytes.NewReader(payload))
	require.NoError(t, err)
	elapsed := time.Since(started)
	require.Greater(t, elapsed, 1500*time.Millisecond)
}

func TestRuntimeTrafficWrapperPreservesHeadroom(t *testing.T) {
	t.Parallel()
	total := 1.0
	runtime, err := NewRuntime(Options{
		Scope:     C.LimitScopeRule,
		TotalMbps: &total,
		RuleID:    "rule-headroom-traffic",
	})
	require.NoError(t, err)

	conn1, conn2 := net.Pipe()
	defer conn2.Close()
	wrapped, err := runtime.WrapConnection(context.Background(), &testHeadroomConn{Conn: conn1}, testMetadata("alice", "in-a", "203.0.113.1"))
	require.NoError(t, err)
	defer wrapped.Close()

	require.Equal(t, 2, N.CalculateFrontHeadroom(wrapped))
	require.Equal(t, 3, N.CalculateRearHeadroom(wrapped))
}

func TestRuntimeClientsWrapperPreservesHeadroom(t *testing.T) {
	t.Parallel()
	runtime, err := NewRuntime(Options{
		Scope:   C.LimitScopeUser,
		Clients: 1,
		RuleID:  "rule-headroom-clients",
	})
	require.NoError(t, err)

	conn1, conn2 := net.Pipe()
	defer conn2.Close()
	wrapped, err := runtime.WrapConnection(context.Background(), &testHeadroomConn{Conn: conn1}, testMetadata("alice", "in-a", "203.0.113.1"))
	require.NoError(t, err)
	defer wrapped.Close()

	require.Equal(t, 2, N.CalculateFrontHeadroom(wrapped))
	require.Equal(t, 3, N.CalculateRearHeadroom(wrapped))
}

func TestRuntimeTrafficPacketWrapperPreservesHeadroom(t *testing.T) {
	t.Parallel()
	total := 1.0
	runtime, err := NewRuntime(Options{
		Scope:     C.LimitScopeRule,
		TotalMbps: &total,
		RuleID:    "rule-headroom-packet-traffic",
	})
	require.NoError(t, err)

	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer packetConn.Close()
	wrapped, err := runtime.WrapPacketConnection(context.Background(), &testHeadroomPacketConn{PacketConn: bufio.NewPacketConn(packetConn)}, testMetadata("alice", "in-a", "203.0.113.1"))
	require.NoError(t, err)
	defer wrapped.Close()

	require.Equal(t, 2, N.CalculateFrontHeadroom(wrapped))
	require.Equal(t, 3, N.CalculateRearHeadroom(wrapped))
}

func TestRuntimeClientsPacketWrapperPreservesHeadroom(t *testing.T) {
	t.Parallel()
	runtime, err := NewRuntime(Options{
		Scope:   C.LimitScopeUser,
		Clients: 1,
		RuleID:  "rule-headroom-packet-clients",
	})
	require.NoError(t, err)

	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer packetConn.Close()
	wrapped, err := runtime.WrapPacketConnection(context.Background(), &testHeadroomPacketConn{PacketConn: bufio.NewPacketConn(packetConn)}, testMetadata("alice", "in-a", "203.0.113.1"))
	require.NoError(t, err)
	defer wrapped.Close()

	require.Equal(t, 2, N.CalculateFrontHeadroom(wrapped))
	require.Equal(t, 3, N.CalculateRearHeadroom(wrapped))
}

func wrapSequentially(runtimes []*Runtime, conn net.Conn, metadata *adapter.InboundContext) (net.Conn, error) {
	var err error
	for _, runtime := range runtimes {
		conn, err = runtime.WrapConnection(context.Background(), conn, metadata)
		if err != nil {
			return nil, err
		}
	}
	return conn, nil
}

func testMetadata(user string, inbound string, ip string) *adapter.InboundContext {
	return &adapter.InboundContext{
		User:    user,
		Inbound: inbound,
		Source:  M.Socksaddr{Addr: netip.MustParseAddr(ip), Port: 12345},
	}
}

type testHeadroomConn struct {
	net.Conn
}

func (c *testHeadroomConn) ReadBuffer(buffer *buf.Buffer) error {
	return bufio.NewExtendedConn(c.Conn).ReadBuffer(buffer)
}

func (c *testHeadroomConn) WriteBuffer(buffer *buf.Buffer) error {
	return bufio.NewExtendedConn(c.Conn).WriteBuffer(buffer)
}

func (c *testHeadroomConn) FrontHeadroom() int {
	return 2
}

func (c *testHeadroomConn) RearHeadroom() int {
	return 3
}

type testHeadroomPacketConn struct {
	N.PacketConn
}

func (c *testHeadroomPacketConn) FrontHeadroom() int {
	return 2
}

func (c *testHeadroomPacketConn) RearHeadroom() int {
	return 3
}
