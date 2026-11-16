package dialer

import (
	"context"
	"net"
	"net/netip"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	proxyproto "github.com/pires/go-proxyproto"
)

type upstreamDialer interface {
	Upstream() any
}

type proxyProtocolDialer struct {
	dialer  N.Dialer
	version byte
}

func wrapProxyProtocolDialer(dialer N.Dialer, version option.ProxyProtocolVersion) N.Dialer {
	base := proxyProtocolDialer{
		dialer:  dialer,
		version: byte(version),
	}
	if parallelResolveDialer, isParallelResolve := dialer.(ParallelInterfaceResolveDialer); isParallelResolve {
		return &proxyProtocolParallelResolveDialer{proxyProtocolResolveDialer{base, parallelResolveDialer}, parallelResolveDialer}
	}
	if parallelDialer, isParallel := dialer.(ParallelInterfaceDialer); isParallel {
		return &proxyProtocolParallelDialer{proxyProtocolDialer: base, parallelDialer: parallelDialer}
	}
	if resolveDialer, isResolve := dialer.(ResolveDialer); isResolve {
		return &proxyProtocolResolveDialer{proxyProtocolDialer: base, resolveDialer: resolveDialer}
	}
	return &base
}

func (d *proxyProtocolDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := d.dialer.DialContext(ctx, network, destination)
	if err != nil {
		return nil, err
	}
	return writeProxyProtocolHeader(ctx, conn, network, destination, d.version)
}

func (d *proxyProtocolDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return d.dialer.ListenPacket(ctx, destination)
}

func (d *proxyProtocolDialer) Upstream() any {
	if upstream, loaded := d.dialer.(upstreamDialer); loaded {
		return upstream.Upstream()
	}
	return d.dialer
}

type proxyProtocolParallelDialer struct {
	proxyProtocolDialer
	parallelDialer ParallelInterfaceDialer
}

func (d *proxyProtocolParallelDialer) DialParallelInterface(ctx context.Context, network string, destination M.Socksaddr, strategy *C.NetworkStrategy, interfaceType []C.InterfaceType, fallbackInterfaceType []C.InterfaceType, fallbackDelay time.Duration) (net.Conn, error) {
	conn, err := d.parallelDialer.DialParallelInterface(ctx, network, destination, strategy, interfaceType, fallbackInterfaceType, fallbackDelay)
	if err != nil {
		return nil, err
	}
	return writeProxyProtocolHeader(ctx, conn, network, destination, d.version)
}

func (d *proxyProtocolParallelDialer) ListenSerialInterfacePacket(ctx context.Context, destination M.Socksaddr, strategy *C.NetworkStrategy, interfaceType []C.InterfaceType, fallbackInterfaceType []C.InterfaceType, fallbackDelay time.Duration) (net.PacketConn, error) {
	return d.parallelDialer.ListenSerialInterfacePacket(ctx, destination, strategy, interfaceType, fallbackInterfaceType, fallbackDelay)
}

type proxyProtocolResolveDialer struct {
	proxyProtocolDialer
	resolveDialer ResolveDialer
}

func (d *proxyProtocolResolveDialer) QueryOptions() adapter.DNSQueryOptions {
	return d.resolveDialer.QueryOptions()
}

type proxyProtocolParallelResolveDialer struct {
	proxyProtocolResolveDialer
	parallelResolveDialer ParallelInterfaceResolveDialer
}

func (d *proxyProtocolParallelResolveDialer) DialParallelInterface(ctx context.Context, network string, destination M.Socksaddr, strategy *C.NetworkStrategy, interfaceType []C.InterfaceType, fallbackInterfaceType []C.InterfaceType, fallbackDelay time.Duration) (net.Conn, error) {
	conn, err := d.parallelResolveDialer.DialParallelInterface(ctx, network, destination, strategy, interfaceType, fallbackInterfaceType, fallbackDelay)
	if err != nil {
		return nil, err
	}
	return writeProxyProtocolHeader(ctx, conn, network, destination, d.version)
}

func (d *proxyProtocolParallelResolveDialer) ListenSerialInterfacePacket(ctx context.Context, destination M.Socksaddr, strategy *C.NetworkStrategy, interfaceType []C.InterfaceType, fallbackInterfaceType []C.InterfaceType, fallbackDelay time.Duration) (net.PacketConn, error) {
	return d.parallelResolveDialer.ListenSerialInterfacePacket(ctx, destination, strategy, interfaceType, fallbackInterfaceType, fallbackDelay)
}

func writeProxyProtocolHeader(ctx context.Context, conn net.Conn, network string, destination M.Socksaddr, version byte) (net.Conn, error) {
	if N.NetworkName(network) != N.NetworkTCP {
		return conn, nil
	}
	metadata := adapter.ContextFrom(ctx)
	var source M.Socksaddr
	if metadata != nil {
		source = metadata.Source
	}
	if !source.IsValid() {
		source = M.SocksaddrFromNet(conn.LocalAddr()).Unwrap()
	}
	actualDestination := M.SocksaddrFromNet(conn.RemoteAddr()).Unwrap()
	if !actualDestination.IsValid() {
		actualDestination = destination
	}
	if !source.IsValid() || !actualDestination.IsValid() || !source.IsIP() || !actualDestination.IsIP() {
		conn.Close()
		return nil, E.New("proxy protocol requires valid TCP source and destination addresses")
	}
	if actualDestination.IsIPv6() && source.IsIPv4() {
		source = M.SocksaddrFrom(netip.AddrFrom16(source.Addr.As16()), source.Port)
	} else if actualDestination.IsIPv4() && source.IsIPv6() && source.Addr.Is4In6() {
		source = M.SocksaddrFrom(netip.AddrFrom4(source.Addr.As4()), source.Port)
	}
	header := proxyproto.HeaderProxyFromAddrs(version, source.TCPAddr(), actualDestination.TCPAddr())
	_, err := header.WriteTo(conn)
	if err != nil {
		conn.Close()
		return nil, E.Cause(err, "write proxy protocol header")
	}
	return conn, nil
}
