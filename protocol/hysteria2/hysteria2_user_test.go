package hysteria2

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	inboundAdapter "github.com/sagernet/sing-box/adapter/inbound"
	boxListener "github.com/sagernet/sing-box/common/listener"
	boxTLS "github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/common/usermanager"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	quicHysteria2 "github.com/sagernet/sing-quic/hysteria2"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

const hysteria2TestTimeout = 5 * time.Second

type hysteria2TestRouter struct {
	streamMetadata chan adapter.InboundContext
	packetMetadata chan adapter.InboundContext
}

func (r *hysteria2TestRouter) RouteConnection(
	ctx context.Context,
	conn net.Conn,
	metadata adapter.InboundContext,
) error {
	r.RouteConnectionEx(ctx, conn, metadata, nil)
	return nil
}

func (r *hysteria2TestRouter) RoutePacketConnection(
	ctx context.Context,
	conn N.PacketConn,
	metadata adapter.InboundContext,
) error {
	r.RoutePacketConnectionEx(ctx, conn, metadata, nil)
	return nil
}

func (r *hysteria2TestRouter) RouteConnectionEx(
	ctx context.Context,
	conn net.Conn,
	metadata adapter.InboundContext,
	onClose N.CloseHandlerFunc,
) {
	r.streamMetadata <- metadata
	defer func() {
		_ = conn.Close()
		if onClose != nil {
			onClose(nil)
		}
	}()
	buffer := make([]byte, 1024)
	for {
		n, err := conn.Read(buffer)
		if n > 0 {
			if _, writeErr := conn.Write(buffer[:n]); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (r *hysteria2TestRouter) RoutePacketConnectionEx(
	ctx context.Context,
	conn N.PacketConn,
	metadata adapter.InboundContext,
	onClose N.CloseHandlerFunc,
) {
	r.packetMetadata <- metadata
	defer func() {
		_ = conn.Close()
		if onClose != nil {
			onClose(nil)
		}
	}()
	for {
		packet := buf.NewPacket()
		destination, err := conn.ReadPacket(packet)
		if err != nil {
			packet.Release()
			return
		}
		if err := conn.WritePacket(packet, destination); err != nil {
			return
		}
	}
}

type hysteria2TestHarness struct {
	ctx          context.Context
	cancel       context.CancelFunc
	inbound      *Inbound
	router       *hysteria2TestRouter
	serverTLS    boxTLS.ServerConfig
	packetConn   net.PacketConn
	serverAddr   M.Socksaddr
	clientsMutex sync.Mutex
	clients      []*quicHysteria2.Client
	closeOnce    sync.Once
}

func newHysteria2TestHarness(t *testing.T, staticUsers []option.Hysteria2User) *hysteria2TestHarness {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	serverTLS := newHysteria2TestServerTLS(t, ctx)
	router := &hysteria2TestRouter{
		streamMetadata: make(chan adapter.InboundContext, 256),
		packetMetadata: make(chan adapter.InboundContext, 256),
	}
	inbound := &Inbound{
		Adapter: inboundAdapter.NewAdapter(C.TypeHysteria2, "managed-user-test"),
		router:  router,
		logger:  logger.NOP(),
		listener: boxListener.New(boxListener.Options{
			Context: ctx,
			Logger:  logger.NOP(),
		}),
		tlsConfig: serverTLS,
	}
	service, err := quicHysteria2.NewService[adapter.UserID](quicHysteria2.ServiceOptions{
		Context:    ctx,
		Logger:     logger.NOP(),
		SendBPS:    125000,
		ReceiveBPS: 125000,
		TLSConfig:  serverTLS,
		UDPTimeout: time.Minute,
		Handler:    inbound,
	})
	require.NoError(t, err)
	inbound.service = service
	require.NoError(t, inbound.initializeUserManager(ctx, staticUsers))
	require.NoError(t, serverTLS.Start())

	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	serverAddr := M.SocksaddrFromNet(packetConn.LocalAddr()).Unwrap()
	require.NoError(t, service.Start(packetConn))

	harness := &hysteria2TestHarness{
		ctx:        ctx,
		cancel:     cancel,
		inbound:    inbound,
		router:     router,
		serverTLS:  serverTLS,
		packetConn: packetConn,
		serverAddr: serverAddr,
		clients:    make([]*quicHysteria2.Client, 0),
	}
	t.Cleanup(harness.close)
	return harness
}

func newHysteria2TestServerTLS(t *testing.T, ctx context.Context) boxTLS.ServerConfig {
	t.Helper()
	privateKey, certificate, err := boxTLS.GenerateCertificate(
		nil,
		nil,
		time.Now,
		"localhost",
		time.Now().Add(time.Hour),
	)
	require.NoError(t, err)
	serverTLS, err := boxTLS.NewServer(ctx, logger.NOP(), option.InboundTLSOptions{
		Enabled:     true,
		ServerName:  "localhost",
		Certificate: []string{string(certificate)},
		Key:         []string{string(privateKey)},
	})
	require.NoError(t, err)
	return serverTLS
}

func (h *hysteria2TestHarness) newClient(t *testing.T, password string) *quicHysteria2.Client {
	t.Helper()
	clientTLS, err := boxTLS.NewClient(h.ctx, logger.NOP(), "localhost", option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: "localhost",
		Insecure:   true,
	})
	require.NoError(t, err)
	client, err := quicHysteria2.NewClient(quicHysteria2.ClientOptions{
		Context:       h.ctx,
		Dialer:        N.SystemDialer,
		Logger:        logger.NOP(),
		ServerAddress: h.serverAddr,
		SendBPS:       125000,
		ReceiveBPS:    125000,
		Password:      password,
		TLSConfig:     clientTLS,
	})
	require.NoError(t, err)
	h.clientsMutex.Lock()
	h.clients = append(h.clients, client)
	h.clientsMutex.Unlock()
	return client
}

func (h *hysteria2TestHarness) openStream(
	client *quicHysteria2.Client,
	payload string,
) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(h.ctx, hysteria2TestTimeout)
	defer cancel()
	conn, err := client.DialConn(ctx, M.ParseSocksaddr("example.com:443"))
	if err != nil {
		return nil, err
	}
	if err := echoHysteria2Stream(conn, payload); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (h *hysteria2TestHarness) openPacket(
	client *quicHysteria2.Client,
	payload string,
) (net.PacketConn, error) {
	ctx, cancel := context.WithTimeout(h.ctx, hysteria2TestTimeout)
	defer cancel()
	conn, err := client.ListenPacket(ctx)
	if err != nil {
		return nil, err
	}
	if err := echoHysteria2Packet(conn, payload); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (h *hysteria2TestHarness) requireRejected(t *testing.T, password string) {
	t.Helper()
	client := h.newClient(t, password)
	conn, err := h.openStream(client, "must-fail")
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err)
	require.NoError(t, client.CloseWithError(net.ErrClosed))
}

func (h *hysteria2TestHarness) requireStreamUser(t *testing.T, expected string) {
	t.Helper()
	select {
	case metadata := <-h.router.streamMetadata:
		require.Equal(t, expected, metadata.User)
	case <-time.After(hysteria2TestTimeout):
		t.Fatal("timed out waiting for Hysteria2 stream metadata")
	}
}

func (h *hysteria2TestHarness) requirePacketUser(t *testing.T, expected string) {
	t.Helper()
	select {
	case metadata := <-h.router.packetMetadata:
		require.Equal(t, expected, metadata.User)
	case <-time.After(hysteria2TestTimeout):
		t.Fatal("timed out waiting for Hysteria2 packet metadata")
	}
}

func (h *hysteria2TestHarness) close() {
	h.closeOnce.Do(func() {
		h.clientsMutex.Lock()
		clients := append([]*quicHysteria2.Client(nil), h.clients...)
		h.clients = nil
		h.clientsMutex.Unlock()
		for _, client := range clients {
			_ = client.CloseWithError(net.ErrClosed)
		}
		h.cancel()
		_ = h.inbound.service.Close()
		_ = h.packetConn.Close()
		_ = h.serverTLS.Close()
	})
}

func echoHysteria2Stream(conn net.Conn, payload string) error {
	if err := conn.SetDeadline(time.Now().Add(hysteria2TestTimeout)); err != nil {
		return err
	}
	if _, err := conn.Write([]byte(payload)); err != nil {
		return err
	}
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, received); err != nil {
		return err
	}
	if string(received) != payload {
		return fmt.Errorf("echo response %q does not match payload %q", received, payload)
	}
	return conn.SetDeadline(time.Time{})
}

func echoHysteria2Packet(conn net.PacketConn, payload string) error {
	if err := conn.SetReadDeadline(time.Now().Add(hysteria2TestTimeout)); err != nil {
		return err
	}
	if _, err := conn.WriteTo([]byte(payload), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53}); err != nil {
		return err
	}
	received := make([]byte, len(payload))
	n, _, err := conn.ReadFrom(received)
	if err != nil {
		return err
	}
	if string(received[:n]) != payload {
		return fmt.Errorf("packet echo response %q does not match payload %q", received[:n], payload)
	}
	return conn.SetReadDeadline(time.Time{})
}

func TestHysteria2UserBackendIdentityAndValidation(t *testing.T) {
	t.Parallel()

	backend := newUserBackend(nil, []unmanagedUser{{password: "legacy-password"}})
	stableID, err := backend.StableID(option.Hysteria2User{
		Name:     "alice",
		Password: "alice-password",
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserID("alice"), stableID)
	_, err = backend.StableID(option.Hysteria2User{Password: "alice-password"})
	require.ErrorContains(t, err, "empty Hysteria2 user name")
	fingerprint := backend.FingerprintUser(option.Hysteria2User{
		Name:     "alice",
		Password: "alice-password",
	})
	require.NotEqual(t, fingerprint, backend.FingerprintUser(option.Hysteria2User{
		Name:     "bob",
		Password: "alice-password",
	}))
	require.NotEqual(t, fingerprint, backend.FingerprintUser(option.Hysteria2User{
		Name:     "alice",
		Password: "rotated-password",
	}))

	published, err := backend.Prepare([]usermanager.Record[option.Hysteria2User]{
		{
			ID: "alice",
			Value: option.Hysteria2User{
				Name:     "alice",
				Password: "legacy-password",
			},
		},
	})
	require.Nil(t, published)
	require.ErrorContains(t, err, "duplicate Hysteria2 password")
	require.NotContains(t, err.Error(), "legacy-password")

	published, err = backend.Prepare([]usermanager.Record[option.Hysteria2User]{
		{ID: "alice", Value: option.Hysteria2User{Name: "alice", Password: "bob-password"}},
		{ID: "bob", Value: option.Hysteria2User{Name: "bob", Password: "alice-password"}},
	})
	require.NoError(t, err)
	require.NotNil(t, published)
}

func TestHysteria2ManagedUsersHandshakeLifecycle(t *testing.T) {
	t.Parallel()

	harness := newHysteria2TestHarness(t, []option.Hysteria2User{
		{Password: "legacy-password"},
	})
	require.Equal(t, adapter.UserGeneration(1), harness.inbound.Generation())

	legacyClient := harness.newClient(t, "legacy-password")
	legacyConn, err := harness.openStream(legacyClient, "legacy-before-updates")
	require.NoError(t, err)
	harness.requireStreamUser(t, "")
	require.NoError(t, legacyConn.Close())

	addResult, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.Hysteria2User]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.Hysteria2User]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "alice",
				Value: option.Hysteria2User{Name: "alice", Password: "alice-old-password"},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []adapter.UserID{"alice"}, addResult.Added)

	aliceClient := harness.newClient(t, "alice-old-password")
	aliceStream, err := harness.openStream(aliceClient, "alice-stream-before-updates")
	require.NoError(t, err)
	harness.requireStreamUser(t, "alice")
	alicePacket, err := harness.openPacket(aliceClient, "alice-packet-before-updates")
	require.NoError(t, err)
	harness.requirePacketUser(t, "alice")

	generationBeforeCollision := harness.inbound.Generation()
	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.Hysteria2User]{
		ExpectedGeneration: generationBeforeCollision,
		Operations: []adapter.UserOperation[option.Hysteria2User]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "bob",
				Value: option.Hysteria2User{Name: "bob", Password: "alice-old-password"},
			},
		},
	})
	require.ErrorIs(t, err, usermanager.ErrBackendPrepareFailure)
	require.Equal(t, generationBeforeCollision, harness.inbound.Generation())

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.Hysteria2User]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.Hysteria2User]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "bob",
				Value: option.Hysteria2User{Name: "bob", Password: "bob-password"},
			},
		},
	})
	require.NoError(t, err)

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.Hysteria2User]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.Hysteria2User]{
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "alice",
				Value: option.Hysteria2User{Name: "alice", Password: "bob-password"},
			},
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "bob",
				Value: option.Hysteria2User{Name: "bob", Password: "alice-old-password"},
			},
		},
	})
	require.NoError(t, err)
	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.Hysteria2User]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.Hysteria2User]{
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "alice",
				Value: option.Hysteria2User{Name: "alice", Password: "alice-old-password"},
			},
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "bob",
				Value: option.Hysteria2User{Name: "bob", Password: "bob-password"},
			},
		},
	})
	require.NoError(t, err)

	_, err = harness.inbound.ReplaceUsers(
		harness.ctx,
		harness.inbound.Generation(),
		"",
		"",
		[]option.Hysteria2User{
			{Name: "bob", Password: "bob-password"},
			{Name: "alice", Password: "alice-old-password"},
		},
	)
	require.NoError(t, err)
	reorderedStream, err := harness.openStream(aliceClient, "stream-after-reorder")
	require.NoError(t, err)
	harness.requireStreamUser(t, "alice")
	reorderedPacket, err := harness.openPacket(aliceClient, "packet-after-reorder")
	require.NoError(t, err)
	harness.requirePacketUser(t, "alice")
	require.NoError(t, reorderedStream.Close())
	require.NoError(t, reorderedPacket.Close())

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.Hysteria2User]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.Hysteria2User]{
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "alice",
				Value: option.Hysteria2User{Name: "alice", Password: "alice-new-password"},
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, echoHysteria2Stream(aliceStream, "existing-stream-after-rotation"))
	require.NoError(t, echoHysteria2Packet(alicePacket, "existing-packet-after-rotation"))
	harness.requireRejected(t, "alice-old-password")

	newCredentialClient := harness.newClient(t, "alice-new-password")
	newCredentialStream, err := harness.openStream(newCredentialClient, "new-credential-stream")
	require.NoError(t, err)
	harness.requireStreamUser(t, "alice")
	newCredentialPacket, err := harness.openPacket(newCredentialClient, "new-credential-packet")
	require.NoError(t, err)
	harness.requirePacketUser(t, "alice")
	require.NoError(t, newCredentialStream.Close())
	require.NoError(t, newCredentialPacket.Close())
	require.NoError(t, newCredentialClient.CloseWithError(net.ErrClosed))

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.Hysteria2User]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.Hysteria2User]{
			{Type: adapter.UserOperationDelete, ID: "alice"},
		},
	})
	require.NoError(t, err)
	harness.requireRejected(t, "alice-new-password")
	require.NoError(t, echoHysteria2Stream(aliceStream, "existing-stream-after-delete"))
	require.NoError(t, echoHysteria2Packet(alicePacket, "existing-packet-after-delete"))

	reusedStream, err := harness.openStream(aliceClient, "new-stream-on-existing-session-after-delete")
	require.NoError(t, err)
	harness.requireStreamUser(t, "alice")
	reusedPacket, err := harness.openPacket(aliceClient, "new-packet-on-existing-session-after-delete")
	require.NoError(t, err)
	harness.requirePacketUser(t, "alice")
	require.NoError(t, reusedStream.Close())
	require.NoError(t, reusedPacket.Close())

	_, err = harness.inbound.ReplaceUsers(
		harness.ctx,
		harness.inbound.Generation(),
		"",
		"",
		[]option.Hysteria2User{{Name: "bob", Password: "bob-password"}},
	)
	require.NoError(t, err)
	postShrinkStream, err := harness.openStream(aliceClient, "stream-after-shrink")
	require.NoError(t, err)
	harness.requireStreamUser(t, "alice")
	require.NoError(t, postShrinkStream.Close())

	_, err = harness.inbound.ReplaceUsers(harness.ctx, harness.inbound.Generation(), "", "", nil)
	require.NoError(t, err)
	emptyStateStream, err := harness.openStream(aliceClient, "stream-after-empty")
	require.NoError(t, err)
	harness.requireStreamUser(t, "alice")
	emptyStatePacket, err := harness.openPacket(aliceClient, "packet-after-empty")
	require.NoError(t, err)
	harness.requirePacketUser(t, "alice")
	require.NoError(t, emptyStateStream.Close())
	require.NoError(t, emptyStatePacket.Close())

	legacyAfterUpdates := harness.newClient(t, "legacy-password")
	legacyAfterUpdatesConn, err := harness.openStream(legacyAfterUpdates, "legacy-after-empty")
	require.NoError(t, err)
	harness.requireStreamUser(t, "")
	require.NoError(t, legacyAfterUpdatesConn.Close())
	require.NoError(t, legacyAfterUpdates.CloseWithError(net.ErrClosed))

	require.NoError(t, aliceStream.Close())
	require.NoError(t, alicePacket.Close())
	require.NoError(t, aliceClient.CloseWithError(net.ErrClosed))
	require.NoError(t, legacyClient.CloseWithError(net.ErrClosed))
}

func TestHysteria2EmptyUserSetPreservesAuthenticatedSession(t *testing.T) {
	t.Parallel()

	harness := newHysteria2TestHarness(t, []option.Hysteria2User{
		{Name: "alice", Password: "alice-password"},
	})
	aliceClient := harness.newClient(t, "alice-password")
	aliceStream, err := harness.openStream(aliceClient, "stream-before-empty")
	require.NoError(t, err)
	harness.requireStreamUser(t, "alice")
	alicePacket, err := harness.openPacket(aliceClient, "packet-before-empty")
	require.NoError(t, err)
	harness.requirePacketUser(t, "alice")

	_, err = harness.inbound.ReplaceUsers(harness.ctx, harness.inbound.Generation(), "", "", nil)
	require.NoError(t, err)
	harness.requireRejected(t, "alice-password")
	require.NoError(t, echoHysteria2Stream(aliceStream, "existing-stream-after-empty"))
	require.NoError(t, echoHysteria2Packet(alicePacket, "existing-packet-after-empty"))

	newStream, err := harness.openStream(aliceClient, "new-stream-after-empty")
	require.NoError(t, err)
	harness.requireStreamUser(t, "alice")
	newPacket, err := harness.openPacket(aliceClient, "new-packet-after-empty")
	require.NoError(t, err)
	harness.requirePacketUser(t, "alice")
	require.NoError(t, newStream.Close())
	require.NoError(t, newPacket.Close())
	require.NoError(t, aliceStream.Close())
	require.NoError(t, alicePacket.Close())
	require.NoError(t, aliceClient.CloseWithError(net.ErrClosed))
}

func TestHysteria2ConcurrentAuthenticationAndUpdates(t *testing.T) {
	t.Parallel()

	harness := newHysteria2TestHarness(t, []option.Hysteria2User{
		{Name: "alice", Password: "alice-password"},
		{Name: "bob", Password: "bob-password-a"},
	})

	const workerCount = 4
	const handshakesPerWorker = 3
	start := make(chan struct{})
	errorsChannel := make(chan error, workerCount+1)
	var waitGroup sync.WaitGroup

	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		<-start
		for index := range 12 {
			password := "bob-password-a"
			if index%2 == 0 {
				password = "bob-password-b"
			}
			_, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.Hysteria2User]{
				ExpectedGeneration: harness.inbound.Generation(),
				Operations: []adapter.UserOperation[option.Hysteria2User]{
					{
						Type:  adapter.UserOperationUpdate,
						ID:    "bob",
						Value: option.Hysteria2User{Name: "bob", Password: password},
					},
				},
			})
			if err != nil {
				errorsChannel <- err
				return
			}
		}
	}()

	for worker := range workerCount {
		waitGroup.Add(1)
		go func(worker int) {
			defer waitGroup.Done()
			<-start
			for handshake := range handshakesPerWorker {
				clientTLS, err := boxTLS.NewClient(harness.ctx, logger.NOP(), "localhost", option.OutboundTLSOptions{
					Enabled:    true,
					ServerName: "localhost",
					Insecure:   true,
				})
				if err != nil {
					errorsChannel <- err
					return
				}
				client, err := quicHysteria2.NewClient(quicHysteria2.ClientOptions{
					Context:       harness.ctx,
					Dialer:        N.SystemDialer,
					Logger:        logger.NOP(),
					ServerAddress: harness.serverAddr,
					SendBPS:       125000,
					ReceiveBPS:    125000,
					Password:      "alice-password",
					TLSConfig:     clientTLS,
				})
				if err != nil {
					errorsChannel <- err
					return
				}
				conn, err := harness.openStream(client, fmt.Sprintf("worker-%d-handshake-%d", worker, handshake))
				if err == nil {
					err = conn.Close()
				}
				_ = client.CloseWithError(net.ErrClosed)
				if err != nil {
					errorsChannel <- err
					return
				}
			}
		}(worker)
	}

	close(start)
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		require.NoError(t, err)
	}
	for range workerCount * handshakesPerWorker {
		harness.requireStreamUser(t, "alice")
	}
}
