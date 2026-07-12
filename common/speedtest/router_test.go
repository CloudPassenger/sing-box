package speedtest

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

type stubRouter struct {
	routeConnectionExCalled bool
}

func (r *stubRouter) RouteConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext) error {
	return nil
}

func (r *stubRouter) RoutePacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext) error {
	return nil
}

func (r *stubRouter) RouteConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	r.routeConnectionExCalled = true
	_ = conn.Close()
	if onClose != nil {
		onClose(nil)
	}
}

func (r *stubRouter) RoutePacketConnectionEx(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
}

// tcpPipe returns a connected pair of TCP loopback connections. Unlike
// net.Pipe(), each side has a kernel send buffer, matching real deployment
// and avoiding artificial write/write deadlocks in these tests.
func tcpPipe(t *testing.T) (server net.Conn, client net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	acceptCh := make(chan net.Conn, 1)
	acceptErrCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErrCh <- err
			return
		}
		acceptCh <- conn
	}()

	client, err = net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	select {
	case server = <-acceptCh:
	case err = <-acceptErrCh:
		require.NoError(t, err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server, client
}

func TestNewRouterDisabledPassesThrough(t *testing.T) {
	t.Parallel()
	upstream := &stubRouter{}
	router, err := NewRouter(upstream, logger.NOP(), "")
	require.NoError(t, err)
	require.Same(t, adapter.ConnectionRouterEx(upstream), router)

	router, err = NewRouter(upstream, logger.NOP(), "disable")
	require.NoError(t, err)
	require.Same(t, adapter.ConnectionRouterEx(upstream), router)
}

func TestNewRouterUnknownMode(t *testing.T) {
	t.Parallel()
	upstream := &stubRouter{}
	_, err := NewRouter(upstream, logger.NOP(), "invalid")
	require.Error(t, err)
}

func TestRouterUpstreamNonMagicPassesThrough(t *testing.T) {
	t.Parallel()
	upstream := &stubRouter{}
	router, err := NewRouter(upstream, logger.NOP(), "allow")
	require.NoError(t, err)

	serverConn, clientConn := tcpPipe(t)

	done := make(chan struct{})
	go func() {
		router.RouteConnectionEx(context.Background(), serverConn, adapter.InboundContext{
			Destination: M.Socksaddr{Fqdn: "example.com"},
		}, nil)
		close(done)
	}()
	clientConn.Close()
	<-done
	require.True(t, upstream.routeConnectionExCalled)
}

func TestRouterAllowServesDownload(t *testing.T) {
	t.Parallel()
	upstream := &stubRouter{}
	router, err := NewRouter(upstream, logger.NOP(), "allow")
	require.NoError(t, err)

	serverConn, clientConn := tcpPipe(t)

	done := make(chan struct{})
	go func() {
		router.RouteConnectionEx(context.Background(), serverConn, adapter.InboundContext{
			Destination: M.Socksaddr{Fqdn: MagicAddress},
		}, nil)
		close(done)
	}()

	var received uint32
	var finalDuration time.Duration
	var ended bool
	err = DownloadTest(context.Background(), clientConn, 4096, func(duration time.Duration, transferred uint32, end bool) {
		if end {
			ended = true
			received = transferred
			finalDuration = duration
		}
	})
	require.NoError(t, err)
	require.True(t, ended)
	require.Equal(t, uint32(4096), received)
	require.GreaterOrEqual(t, finalDuration, time.Duration(0))
	<-done
	require.False(t, upstream.routeConnectionExCalled)
}

func TestRouterAllowServesUpload(t *testing.T) {
	t.Parallel()
	upstream := &stubRouter{}
	router, err := NewRouter(upstream, logger.NOP(), "allow")
	require.NoError(t, err)

	serverConn, clientConn := tcpPipe(t)

	done := make(chan struct{})
	go func() {
		router.RouteConnectionEx(context.Background(), serverConn, adapter.InboundContext{
			Destination: M.Socksaddr{Fqdn: MagicAddress},
		}, nil)
		close(done)
	}()

	var received uint32
	var ended bool
	err = UploadTest(context.Background(), clientConn, 4096, func(duration time.Duration, transferred uint32, end bool) {
		if end {
			ended = true
			received = transferred
		}
	})
	require.NoError(t, err)
	require.True(t, ended)
	require.Equal(t, uint32(4096), received)
	<-done
}

func TestRouterRejectDownload(t *testing.T) {
	t.Parallel()
	upstream := &stubRouter{}
	router, err := NewRouter(upstream, logger.NOP(), "reject")
	require.NoError(t, err)

	serverConn, clientConn := tcpPipe(t)

	go router.RouteConnectionEx(context.Background(), serverConn, adapter.InboundContext{
		Destination: M.Socksaddr{Fqdn: MagicAddress},
	}, nil)

	err = DownloadTest(context.Background(), clientConn, 4096, func(duration time.Duration, transferred uint32, end bool) {})
	require.Error(t, err)
}

func TestRouterRejectUpload(t *testing.T) {
	t.Parallel()
	upstream := &stubRouter{}
	router, err := NewRouter(upstream, logger.NOP(), "reject")
	require.NoError(t, err)

	serverConn, clientConn := tcpPipe(t)

	go router.RouteConnectionEx(context.Background(), serverConn, adapter.InboundContext{
		Destination: M.Socksaddr{Fqdn: MagicAddress},
	}, nil)

	err = UploadTest(context.Background(), clientConn, 4096, func(duration time.Duration, transferred uint32, end bool) {})
	require.Error(t, err)
}

func TestRouterUnknownRequestType(t *testing.T) {
	t.Parallel()
	upstream := &stubRouter{}
	router, err := NewRouter(upstream, logger.NOP(), "allow")
	require.NoError(t, err)

	serverConn, clientConn := tcpPipe(t)

	done := make(chan struct{})
	go func() {
		router.RouteConnectionEx(context.Background(), serverConn, adapter.InboundContext{
			Destination: M.Socksaddr{Fqdn: MagicAddress},
		}, nil)
		close(done)
	}()

	_, err = clientConn.Write([]byte{0xFF})
	require.NoError(t, err)
	_ = clientConn.Close()
	<-done
}

func TestClientDownloadCancelMidTransfer(t *testing.T) {
	t.Parallel()
	upstream := &stubRouter{}
	router, err := NewRouter(upstream, logger.NOP(), "allow")
	require.NoError(t, err)

	serverConn, clientConn := tcpPipe(t)

	go router.RouteConnectionEx(context.Background(), serverConn, adapter.InboundContext{
		Destination: M.Socksaddr{Fqdn: MagicAddress},
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	length := uint32(10 * chunkSize)
	firstProgress := true
	err = DownloadTest(ctx, clientConn, length, func(duration time.Duration, transferred uint32, end bool) {
		if !end && firstProgress {
			firstProgress = false
			cancel()
		}
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestClientUploadCancelMidTransfer(t *testing.T) {
	t.Parallel()
	upstream := &stubRouter{}
	router, err := NewRouter(upstream, logger.NOP(), "allow")
	require.NoError(t, err)

	serverConn, clientConn := tcpPipe(t)

	go router.RouteConnectionEx(context.Background(), serverConn, adapter.InboundContext{
		Destination: M.Socksaddr{Fqdn: MagicAddress},
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	length := uint32(10 * chunkSize)
	firstProgress := true
	err = UploadTest(ctx, clientConn, length, func(duration time.Duration, transferred uint32, end bool) {
		if !end && firstProgress {
			firstProgress = false
			cancel()
		}
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestRouterInvokesOnCloseForSpeedTest(t *testing.T) {
	t.Parallel()
	upstream := &stubRouter{}
	router, err := NewRouter(upstream, logger.NOP(), "allow")
	require.NoError(t, err)

	serverConn, clientConn := tcpPipe(t)

	onCloseCalled := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		router.RouteConnectionEx(context.Background(), serverConn, adapter.InboundContext{
			Destination: M.Socksaddr{Fqdn: MagicAddress},
		}, func(closeErr error) {
			onCloseCalled <- closeErr
		})
		close(done)
	}()

	err = DownloadTest(context.Background(), clientConn, 4096, func(duration time.Duration, transferred uint32, end bool) {})
	require.NoError(t, err)
	<-done

	select {
	case <-onCloseCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("onClose was not invoked")
	}
}
