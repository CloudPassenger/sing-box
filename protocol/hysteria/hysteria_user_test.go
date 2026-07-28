package hysteria

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
	quicHysteria "github.com/sagernet/sing-quic/hysteria"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

const hysteriaTestTimeout = 5 * time.Second

type hysteriaTestRouter struct {
	streamMetadata chan adapter.InboundContext
	packetMetadata chan adapter.InboundContext
}

func (r *hysteriaTestRouter) RouteConnection(
	ctx context.Context,
	conn net.Conn,
	metadata adapter.InboundContext,
) error {
	r.RouteConnectionEx(ctx, conn, metadata, nil)
	return nil
}

func (r *hysteriaTestRouter) RoutePacketConnection(
	ctx context.Context,
	conn N.PacketConn,
	metadata adapter.InboundContext,
) error {
	r.RoutePacketConnectionEx(ctx, conn, metadata, nil)
	return nil
}

func (r *hysteriaTestRouter) RouteConnectionEx(
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

func (r *hysteriaTestRouter) RoutePacketConnectionEx(
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

type hysteriaTestHarness struct {
	ctx          context.Context
	cancel       context.CancelFunc
	inbound      *Inbound
	router       *hysteriaTestRouter
	serverTLS    boxTLS.ServerConfig
	packetConn   net.PacketConn
	serverAddr   M.Socksaddr
	clientsMutex sync.Mutex
	clients      []*quicHysteria.Client
	closeOnce    sync.Once
}

func newHysteriaTestHarness(t *testing.T, staticUsers []option.HysteriaUser) *hysteriaTestHarness {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	serverTLS := newHysteriaTestServerTLS(t, ctx)
	router := &hysteriaTestRouter{
		streamMetadata: make(chan adapter.InboundContext, 256),
		packetMetadata: make(chan adapter.InboundContext, 256),
	}
	inbound := &Inbound{
		Adapter: inboundAdapter.NewAdapter(C.TypeHysteria, "managed-user-test"),
		router:  router,
		logger:  logger.NOP(),
		listener: boxListener.New(boxListener.Options{
			Context: ctx,
			Logger:  logger.NOP(),
		}),
		tlsConfig: serverTLS,
	}
	service, err := quicHysteria.NewService[adapter.UserID](quicHysteria.ServiceOptions{
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

	harness := &hysteriaTestHarness{
		ctx:        ctx,
		cancel:     cancel,
		inbound:    inbound,
		router:     router,
		serverTLS:  serverTLS,
		packetConn: packetConn,
		serverAddr: serverAddr,
		clients:    make([]*quicHysteria.Client, 0),
	}
	t.Cleanup(harness.close)
	return harness
}

func newHysteriaTestServerTLS(t *testing.T, ctx context.Context) boxTLS.ServerConfig {
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

func (h *hysteriaTestHarness) newClient(t *testing.T, password string) *quicHysteria.Client {
	t.Helper()
	clientTLS, err := boxTLS.NewClient(h.ctx, logger.NOP(), "localhost", option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: "localhost",
		Insecure:   true,
	})
	require.NoError(t, err)
	client, err := quicHysteria.NewClient(quicHysteria.ClientOptions{
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

func (h *hysteriaTestHarness) openStream(
	client *quicHysteria.Client,
	payload string,
) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(h.ctx, hysteriaTestTimeout)
	defer cancel()
	conn, err := client.DialConn(ctx, M.ParseSocksaddr("example.com:443"))
	if err != nil {
		return nil, err
	}
	if err := echoHysteriaStream(conn, payload); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (h *hysteriaTestHarness) openPacket(
	client *quicHysteria.Client,
	payload string,
) (net.PacketConn, error) {
	ctx, cancel := context.WithTimeout(h.ctx, hysteriaTestTimeout)
	defer cancel()
	conn, err := client.ListenPacket(ctx, M.ParseSocksaddr("1.1.1.1:53"))
	if err != nil {
		return nil, err
	}
	if err := echoHysteriaPacket(conn, payload); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (h *hysteriaTestHarness) requireRejected(t *testing.T, password string) {
	t.Helper()
	client := h.newClient(t, password)
	conn, err := h.openStream(client, "must-fail")
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err)
	require.NoError(t, client.CloseWithError(net.ErrClosed))
}

func (h *hysteriaTestHarness) requireStreamUser(t *testing.T, expected string) {
	t.Helper()
	select {
	case metadata := <-h.router.streamMetadata:
		require.Equal(t, expected, metadata.User)
	case <-time.After(hysteriaTestTimeout):
		t.Fatal("timed out waiting for Hysteria stream metadata")
	}
}

func (h *hysteriaTestHarness) requirePacketUser(t *testing.T, expected string) {
	t.Helper()
	select {
	case metadata := <-h.router.packetMetadata:
		require.Equal(t, expected, metadata.User)
	case <-time.After(hysteriaTestTimeout):
		t.Fatal("timed out waiting for Hysteria packet metadata")
	}
}

func (h *hysteriaTestHarness) close() {
	h.closeOnce.Do(func() {
		h.clientsMutex.Lock()
		clients := append([]*quicHysteria.Client(nil), h.clients...)
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

func echoHysteriaStream(conn net.Conn, payload string) error {
	if err := conn.SetDeadline(time.Now().Add(hysteriaTestTimeout)); err != nil {
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

func echoHysteriaPacket(conn net.PacketConn, payload string) error {
	if err := conn.SetReadDeadline(time.Now().Add(hysteriaTestTimeout)); err != nil {
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

func TestHysteriaUserBackendIdentityAndValidation(t *testing.T) {
	t.Parallel()

	staticUsers := []option.HysteriaUser{{Auth: []byte("legacy-password")}}
	_, unmanagedUsers := splitHysteriaUsers(staticUsers)
	staticUsers[0].Auth[0] = 'X'
	require.Equal(t, "legacy-password", unmanagedUsers[0].password)

	backend := newUserBackend(nil, unmanagedUsers)
	stableID, err := backend.StableID(option.HysteriaUser{
		Name:       "alice",
		AuthString: "alice-password",
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserID("alice"), stableID)
	_, err = backend.StableID(option.HysteriaUser{AuthString: "alice-password"})
	require.ErrorContains(t, err, "empty Hysteria user name")

	fingerprint := backend.FingerprintUser(option.HysteriaUser{
		Name:       "alice",
		Auth:       []byte("ignored-auth"),
		AuthString: "alice-password",
	})
	require.NotEqual(t, fingerprint, backend.FingerprintUser(option.HysteriaUser{
		Name:       "bob",
		Auth:       []byte("ignored-auth"),
		AuthString: "alice-password",
	}))
	require.NotEqual(t, fingerprint, backend.FingerprintUser(option.HysteriaUser{
		Name:       "alice",
		Auth:       []byte("changed-auth"),
		AuthString: "alice-password",
	}))
	require.NotEqual(t, fingerprint, backend.FingerprintUser(option.HysteriaUser{
		Name:       "alice",
		Auth:       []byte("ignored-auth"),
		AuthString: "rotated-password",
	}))

	published, err := backend.Prepare([]usermanager.Record[option.HysteriaUser]{
		{
			ID: "alice",
			Value: option.HysteriaUser{
				Name:       "alice",
				AuthString: "legacy-password",
			},
		},
	})
	require.Nil(t, published)
	require.ErrorContains(t, err, "duplicate Hysteria authentication")
	require.NotContains(t, err.Error(), "legacy-password")

	published, err = backend.Prepare([]usermanager.Record[option.HysteriaUser]{
		{ID: "alice", Value: option.HysteriaUser{Name: "alice", AuthString: "bob-password"}},
		{ID: "bob", Value: option.HysteriaUser{Name: "bob", AuthString: "alice-password"}},
	})
	require.NoError(t, err)
	require.NotNil(t, published)
}

func TestHysteriaManagedUsersHandshakeLifecycle(t *testing.T) {
	t.Parallel()

	harness := newHysteriaTestHarness(t, []option.HysteriaUser{
		{Auth: []byte("legacy-password")},
	})
	require.Equal(t, adapter.UserGeneration(1), harness.inbound.Generation())

	legacyClient := harness.newClient(t, "legacy-password")
	legacyConn, err := harness.openStream(legacyClient, "legacy-before-updates")
	require.NoError(t, err)
	harness.requireStreamUser(t, "")
	require.NoError(t, legacyConn.Close())

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.HysteriaUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.HysteriaUser]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "alice",
				Value: option.HysteriaUser{Name: "alice", AuthString: "alice-old-password"},
			},
		},
	})
	require.NoError(t, err)

	aliceClient := harness.newClient(t, "alice-old-password")
	aliceStream, err := harness.openStream(aliceClient, "alice-stream-before-updates")
	require.NoError(t, err)
	harness.requireStreamUser(t, "alice")
	alicePacket, err := harness.openPacket(aliceClient, "alice-packet-before-updates")
	require.NoError(t, err)
	harness.requirePacketUser(t, "alice")

	generationBeforeCollision := harness.inbound.Generation()
	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.HysteriaUser]{
		ExpectedGeneration: generationBeforeCollision,
		Operations: []adapter.UserOperation[option.HysteriaUser]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "bob",
				Value: option.HysteriaUser{Name: "bob", AuthString: "alice-old-password"},
			},
		},
	})
	require.ErrorIs(t, err, usermanager.ErrBackendPrepareFailure)
	require.Equal(t, generationBeforeCollision, harness.inbound.Generation())

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.HysteriaUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.HysteriaUser]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "bob",
				Value: option.HysteriaUser{Name: "bob", AuthString: "bob-password"},
			},
		},
	})
	require.NoError(t, err)

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.HysteriaUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.HysteriaUser]{
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "alice",
				Value: option.HysteriaUser{Name: "alice", AuthString: "bob-password"},
			},
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "bob",
				Value: option.HysteriaUser{Name: "bob", AuthString: "alice-old-password"},
			},
		},
	})
	require.NoError(t, err)
	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.HysteriaUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.HysteriaUser]{
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "alice",
				Value: option.HysteriaUser{Name: "alice", AuthString: "alice-old-password"},
			},
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "bob",
				Value: option.HysteriaUser{Name: "bob", AuthString: "bob-password"},
			},
		},
	})
	require.NoError(t, err)

	_, err = harness.inbound.ReplaceUsers(
		harness.ctx,
		harness.inbound.Generation(),
		"",
		"",
		[]option.HysteriaUser{
			{Name: "bob", AuthString: "bob-password"},
			{Name: "alice", AuthString: "alice-old-password"},
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

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.HysteriaUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.HysteriaUser]{
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "alice",
				Value: option.HysteriaUser{Name: "alice", AuthString: "alice-new-password"},
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, echoHysteriaStream(aliceStream, "existing-stream-after-rotation"))
	require.NoError(t, echoHysteriaPacket(alicePacket, "existing-packet-after-rotation"))
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

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.HysteriaUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.HysteriaUser]{
			{Type: adapter.UserOperationDelete, ID: "alice"},
		},
	})
	require.NoError(t, err)
	harness.requireRejected(t, "alice-new-password")
	require.NoError(t, echoHysteriaStream(aliceStream, "existing-stream-after-delete"))
	require.NoError(t, echoHysteriaPacket(alicePacket, "existing-packet-after-delete"))

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
		[]option.HysteriaUser{{Name: "bob", AuthString: "bob-password"}},
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

func TestHysteriaEmptyUserSetPreservesAuthenticatedSession(t *testing.T) {
	t.Parallel()

	harness := newHysteriaTestHarness(t, []option.HysteriaUser{
		{Name: "alice", AuthString: "alice-password"},
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
	require.NoError(t, echoHysteriaStream(aliceStream, "existing-stream-after-empty"))
	require.NoError(t, echoHysteriaPacket(alicePacket, "existing-packet-after-empty"))

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

func TestHysteriaConcurrentAuthenticationAndUpdates(t *testing.T) {
	t.Parallel()

	harness := newHysteriaTestHarness(t, []option.HysteriaUser{
		{Name: "alice", AuthString: "alice-password"},
		{Name: "bob", AuthString: "bob-password-a"},
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
			_, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.HysteriaUser]{
				ExpectedGeneration: harness.inbound.Generation(),
				Operations: []adapter.UserOperation[option.HysteriaUser]{
					{
						Type:  adapter.UserOperationUpdate,
						ID:    "bob",
						Value: option.HysteriaUser{Name: "bob", AuthString: password},
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
				client, err := quicHysteria.NewClient(quicHysteria.ClientOptions{
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
