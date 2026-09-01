package tuic

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
	quicTUIC "github.com/sagernet/sing-quic/tuic"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/require"
)

const (
	tuicTestTimeout = 5 * time.Second
	tuicLegacyUUID  = "00000000-0000-0000-0000-000000000001"
	tuicAliceUUID   = "00000000-0000-0000-0000-000000000002"
	tuicBobUUID     = "00000000-0000-0000-0000-000000000003"
	tuicLegacyUUIDB = "00000000-0000-0000-0000-000000000004"
)

type tuicTestRouter struct {
	streamMetadata chan adapter.InboundContext
	packetMetadata chan adapter.InboundContext
}

func (r *tuicTestRouter) RouteConnection(
	ctx context.Context,
	conn net.Conn,
	metadata adapter.InboundContext,
) error {
	r.RouteConnectionEx(ctx, conn, metadata, nil)
	return nil
}

func (r *tuicTestRouter) RoutePacketConnection(
	ctx context.Context,
	conn N.PacketConn,
	metadata adapter.InboundContext,
) error {
	r.RoutePacketConnectionEx(ctx, conn, metadata, nil)
	return nil
}

func (r *tuicTestRouter) RouteConnectionEx(
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

func (r *tuicTestRouter) RoutePacketConnectionEx(
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

type tuicTestHarness struct {
	ctx          context.Context
	cancel       context.CancelFunc
	inbound      *Inbound
	router       *tuicTestRouter
	serverTLS    boxTLS.ServerConfig
	packetConn   net.PacketConn
	serverAddr   M.Socksaddr
	clientsMutex sync.Mutex
	clients      []*quicTUIC.Client
	closeOnce    sync.Once
}

func newTUICTestHarness(t *testing.T, staticUsers []option.TUICUser) *tuicTestHarness {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	serverTLS := newTUICTestServerTLS(t, ctx)
	router := &tuicTestRouter{
		streamMetadata: make(chan adapter.InboundContext, 256),
		packetMetadata: make(chan adapter.InboundContext, 256),
	}
	inbound := &Inbound{
		Adapter: inboundAdapter.NewAdapter(C.TypeTUIC, "managed-user-test"),
		router:  router,
		logger:  logger.NOP(),
		listener: boxListener.New(boxListener.Options{
			Context: ctx,
			Logger:  logger.NOP(),
		}),
		tlsConfig: serverTLS,
	}
	service, err := quicTUIC.NewService[adapter.UserID](quicTUIC.ServiceOptions{
		Context:     ctx,
		Logger:      logger.NOP(),
		TLSConfig:   serverTLS,
		AuthTimeout: tuicTestTimeout,
		Heartbeat:   time.Hour,
		UDPTimeout:  time.Minute,
		Handler:     inbound,
	})
	require.NoError(t, err)
	inbound.server = service
	require.NoError(t, inbound.initializeUserManager(ctx, staticUsers))
	require.NoError(t, serverTLS.Start())

	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	serverAddr := M.SocksaddrFromNet(packetConn.LocalAddr()).Unwrap()
	require.NoError(t, service.Start(packetConn))

	harness := &tuicTestHarness{
		ctx:        ctx,
		cancel:     cancel,
		inbound:    inbound,
		router:     router,
		serverTLS:  serverTLS,
		packetConn: packetConn,
		serverAddr: serverAddr,
		clients:    make([]*quicTUIC.Client, 0),
	}
	t.Cleanup(harness.close)
	return harness
}

func newTUICTestServerTLS(t *testing.T, ctx context.Context) boxTLS.ServerConfig {
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

func parseTUICTestUUID(t *testing.T, uuidString string) [16]byte {
	t.Helper()
	parsedUUID, err := uuid.FromString(uuidString)
	require.NoError(t, err)
	return [16]byte(parsedUUID)
}

func (h *tuicTestHarness) newClient(t *testing.T, uuidString string, password string) *quicTUIC.Client {
	t.Helper()
	clientTLS, err := boxTLS.NewClient(h.ctx, logger.NOP(), "localhost", option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: "localhost",
		Insecure:   true,
	})
	require.NoError(t, err)
	client, err := quicTUIC.NewClient(quicTUIC.ClientOptions{
		Context:       h.ctx,
		Dialer:        N.SystemDialer,
		ServerAddress: h.serverAddr,
		TLSConfig:     clientTLS,
		UUID:          parseTUICTestUUID(t, uuidString),
		Password:      password,
		Heartbeat:     time.Hour,
	})
	require.NoError(t, err)
	h.clientsMutex.Lock()
	h.clients = append(h.clients, client)
	h.clientsMutex.Unlock()
	return client
}

func (h *tuicTestHarness) openStream(client *quicTUIC.Client, payload string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(h.ctx, tuicTestTimeout)
	defer cancel()
	conn, err := client.DialConn(ctx, M.ParseSocksaddr("example.com:443"))
	if err != nil {
		return nil, err
	}
	if err := echoTUICStream(conn, payload); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (h *tuicTestHarness) openPacket(client *quicTUIC.Client, payload string) (net.PacketConn, error) {
	ctx, cancel := context.WithTimeout(h.ctx, tuicTestTimeout)
	defer cancel()
	conn, err := client.ListenPacket(ctx)
	if err != nil {
		return nil, err
	}
	if err := echoTUICPacket(conn, payload); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (h *tuicTestHarness) requireRejected(t *testing.T, uuidString string, password string) {
	t.Helper()
	client := h.newClient(t, uuidString, password)
	conn, err := h.openStream(client, "must-fail")
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err)
	require.NoError(t, client.CloseWithError(net.ErrClosed))
}

func (h *tuicTestHarness) requireStreamUser(t *testing.T, expected string) {
	t.Helper()
	select {
	case metadata := <-h.router.streamMetadata:
		require.Equal(t, expected, metadata.User)
	case <-time.After(tuicTestTimeout):
		t.Fatal("timed out waiting for TUIC stream metadata")
	}
}

func (h *tuicTestHarness) requirePacketUser(t *testing.T, expected string) {
	t.Helper()
	select {
	case metadata := <-h.router.packetMetadata:
		require.Equal(t, expected, metadata.User)
	case <-time.After(tuicTestTimeout):
		t.Fatal("timed out waiting for TUIC packet metadata")
	}
}

func (h *tuicTestHarness) close() {
	h.closeOnce.Do(func() {
		h.clientsMutex.Lock()
		clients := append([]*quicTUIC.Client(nil), h.clients...)
		h.clients = nil
		h.clientsMutex.Unlock()
		for _, client := range clients {
			_ = client.CloseWithError(net.ErrClosed)
		}
		h.cancel()
		_ = h.inbound.server.Close()
		_ = h.packetConn.Close()
		_ = h.serverTLS.Close()
	})
}

func echoTUICStream(conn net.Conn, payload string) error {
	if err := conn.SetDeadline(time.Now().Add(tuicTestTimeout)); err != nil {
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

func echoTUICPacket(conn net.PacketConn, payload string) error {
	if err := conn.SetReadDeadline(time.Now().Add(tuicTestTimeout)); err != nil {
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

func TestTUICUserBackendIdentityAndValidation(t *testing.T) {
	t.Parallel()

	backend := newUserBackend(nil, []unmanagedUser{{uuid: tuicLegacyUUID, password: "legacy-password"}})
	stableID, err := backend.StableID(option.TUICUser{
		Name:     "alice",
		UUID:     tuicAliceUUID,
		Password: "alice-password",
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserID("alice"), stableID)
	_, err = backend.StableID(option.TUICUser{UUID: tuicAliceUUID, Password: "alice-password"})
	require.ErrorContains(t, err, "empty TUIC user name")

	fingerprint := backend.FingerprintUser(option.TUICUser{
		Name:     "alice",
		UUID:     tuicAliceUUID,
		Password: "alice-password",
	})
	require.NotEqual(t, fingerprint, backend.FingerprintUser(option.TUICUser{
		Name:     "bob",
		UUID:     tuicAliceUUID,
		Password: "alice-password",
	}))
	require.NotEqual(t, fingerprint, backend.FingerprintUser(option.TUICUser{
		Name:     "alice",
		UUID:     tuicBobUUID,
		Password: "alice-password",
	}))
	require.NotEqual(t, fingerprint, backend.FingerprintUser(option.TUICUser{
		Name:     "alice",
		UUID:     tuicAliceUUID,
		Password: "rotated-password",
	}))

	published, err := backend.Prepare([]usermanager.Record[option.TUICUser]{
		{
			ID: "alice",
			Value: option.TUICUser{
				Name:     "alice",
				UUID:     tuicLegacyUUID,
				Password: "alice-password",
			},
		},
	})
	require.Nil(t, published)
	require.ErrorContains(t, err, "duplicate TUIC UUID")
	require.NotContains(t, err.Error(), tuicLegacyUUID)

	const invalidUUID = "not-a-valid-tuic-credential"
	published, err = backend.Prepare([]usermanager.Record[option.TUICUser]{
		{
			ID: "alice",
			Value: option.TUICUser{
				Name:     "alice",
				UUID:     invalidUUID,
				Password: "alice-password",
			},
		},
	})
	require.Nil(t, published)
	require.ErrorContains(t, err, "invalid UUID")
	require.NotContains(t, err.Error(), invalidUUID)

	published, err = backend.Prepare([]usermanager.Record[option.TUICUser]{
		{ID: "alice", Value: option.TUICUser{Name: "alice", UUID: tuicBobUUID, Password: "shared-password"}},
		{ID: "bob", Value: option.TUICUser{Name: "bob", UUID: tuicAliceUUID, Password: "shared-password"}},
	})
	require.NoError(t, err)
	require.NotNil(t, published)
}

func TestTUICManagedUsersHandshakeLifecycle(t *testing.T) {
	t.Parallel()

	harness := newTUICTestHarness(t, []option.TUICUser{
		{UUID: tuicLegacyUUID, Password: "legacy-password"},
		{UUID: tuicLegacyUUIDB, Password: "legacy-password-b"},
	})
	require.Equal(t, adapter.UserGeneration(1), harness.inbound.Generation())

	legacyClient := harness.newClient(t, tuicLegacyUUID, "legacy-password")
	legacyConn, err := harness.openStream(legacyClient, "legacy-before-updates")
	require.NoError(t, err)
	harness.requireStreamUser(t, "")
	require.NoError(t, legacyConn.Close())
	legacyClientB := harness.newClient(t, tuicLegacyUUIDB, "legacy-password-b")
	legacyConnB, err := harness.openStream(legacyClientB, "second-legacy-before-updates")
	require.NoError(t, err)
	harness.requireStreamUser(t, "")
	require.NoError(t, legacyConnB.Close())

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.TUICUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.TUICUser]{
			{
				Type: adapter.UserOperationAdd,
				ID:   "alice",
				Value: option.TUICUser{
					Name:     "alice",
					UUID:     tuicAliceUUID,
					Password: "alice-old-password",
				},
			},
		},
	})
	require.NoError(t, err)

	aliceClient := harness.newClient(t, tuicAliceUUID, "alice-old-password")
	aliceStream, err := harness.openStream(aliceClient, "alice-stream-before-updates")
	require.NoError(t, err)
	harness.requireStreamUser(t, "alice")
	alicePacket, err := harness.openPacket(aliceClient, "alice-packet-before-updates")
	require.NoError(t, err)
	harness.requirePacketUser(t, "alice")

	generationBeforeCollision := harness.inbound.Generation()
	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.TUICUser]{
		ExpectedGeneration: generationBeforeCollision,
		Operations: []adapter.UserOperation[option.TUICUser]{
			{
				Type: adapter.UserOperationAdd,
				ID:   "bob",
				Value: option.TUICUser{
					Name:     "bob",
					UUID:     tuicAliceUUID,
					Password: "bob-password",
				},
			},
		},
	})
	require.ErrorIs(t, err, usermanager.ErrBackendPrepareFailure)
	require.Equal(t, generationBeforeCollision, harness.inbound.Generation())

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.TUICUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.TUICUser]{
			{
				Type: adapter.UserOperationAdd,
				ID:   "bob",
				Value: option.TUICUser{
					Name:     "bob",
					UUID:     tuicBobUUID,
					Password: "bob-password",
				},
			},
		},
	})
	require.NoError(t, err)

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.TUICUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.TUICUser]{
			{
				Type: adapter.UserOperationUpdate,
				ID:   "alice",
				Value: option.TUICUser{
					Name:     "alice",
					UUID:     tuicBobUUID,
					Password: "bob-password",
				},
			},
			{
				Type: adapter.UserOperationUpdate,
				ID:   "bob",
				Value: option.TUICUser{
					Name:     "bob",
					UUID:     tuicAliceUUID,
					Password: "alice-old-password",
				},
			},
		},
	})
	require.NoError(t, err)
	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.TUICUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.TUICUser]{
			{
				Type: adapter.UserOperationUpdate,
				ID:   "alice",
				Value: option.TUICUser{
					Name:     "alice",
					UUID:     tuicAliceUUID,
					Password: "alice-old-password",
				},
			},
			{
				Type: adapter.UserOperationUpdate,
				ID:   "bob",
				Value: option.TUICUser{
					Name:     "bob",
					UUID:     tuicBobUUID,
					Password: "bob-password",
				},
			},
		},
	})
	require.NoError(t, err)

	_, err = harness.inbound.ReplaceUsers(
		harness.ctx,
		harness.inbound.Generation(),
		"",
		"",
		[]option.TUICUser{
			{Name: "bob", UUID: tuicBobUUID, Password: "bob-password"},
			{Name: "alice", UUID: tuicAliceUUID, Password: "alice-old-password"},
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

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.TUICUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.TUICUser]{
			{
				Type: adapter.UserOperationUpdate,
				ID:   "alice",
				Value: option.TUICUser{
					Name:     "alice",
					UUID:     tuicAliceUUID,
					Password: "alice-new-password",
				},
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, echoTUICStream(aliceStream, "existing-stream-after-rotation"))
	require.NoError(t, echoTUICPacket(alicePacket, "existing-packet-after-rotation"))
	harness.requireRejected(t, tuicAliceUUID, "alice-old-password")

	newCredentialClient := harness.newClient(t, tuicAliceUUID, "alice-new-password")
	newCredentialStream, err := harness.openStream(newCredentialClient, "new-credential-stream")
	require.NoError(t, err)
	harness.requireStreamUser(t, "alice")
	newCredentialPacket, err := harness.openPacket(newCredentialClient, "new-credential-packet")
	require.NoError(t, err)
	harness.requirePacketUser(t, "alice")
	require.NoError(t, newCredentialStream.Close())
	require.NoError(t, newCredentialPacket.Close())
	require.NoError(t, newCredentialClient.CloseWithError(net.ErrClosed))

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.TUICUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.TUICUser]{
			{Type: adapter.UserOperationDelete, ID: "alice"},
		},
	})
	require.NoError(t, err)
	harness.requireRejected(t, tuicAliceUUID, "alice-new-password")
	require.NoError(t, echoTUICStream(aliceStream, "existing-stream-after-delete"))
	require.NoError(t, echoTUICPacket(alicePacket, "existing-packet-after-delete"))

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
		[]option.TUICUser{{Name: "bob", UUID: tuicBobUUID, Password: "bob-password"}},
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

	legacyAfterUpdates := harness.newClient(t, tuicLegacyUUID, "legacy-password")
	legacyAfterUpdatesConn, err := harness.openStream(legacyAfterUpdates, "legacy-after-empty")
	require.NoError(t, err)
	harness.requireStreamUser(t, "")
	require.NoError(t, legacyAfterUpdatesConn.Close())
	require.NoError(t, legacyAfterUpdates.CloseWithError(net.ErrClosed))

	require.NoError(t, aliceStream.Close())
	require.NoError(t, alicePacket.Close())
	require.NoError(t, aliceClient.CloseWithError(net.ErrClosed))
	require.NoError(t, legacyClient.CloseWithError(net.ErrClosed))
	require.NoError(t, legacyClientB.CloseWithError(net.ErrClosed))
}

func TestTUICEmptyUserSetPreservesAuthenticatedSession(t *testing.T) {
	t.Parallel()

	harness := newTUICTestHarness(t, []option.TUICUser{
		{Name: "alice", UUID: tuicAliceUUID, Password: "alice-password"},
	})
	aliceClient := harness.newClient(t, tuicAliceUUID, "alice-password")
	aliceStream, err := harness.openStream(aliceClient, "stream-before-empty")
	require.NoError(t, err)
	harness.requireStreamUser(t, "alice")
	alicePacket, err := harness.openPacket(aliceClient, "packet-before-empty")
	require.NoError(t, err)
	harness.requirePacketUser(t, "alice")

	_, err = harness.inbound.ReplaceUsers(harness.ctx, harness.inbound.Generation(), "", "", nil)
	require.NoError(t, err)
	harness.requireRejected(t, tuicAliceUUID, "alice-password")
	require.NoError(t, echoTUICStream(aliceStream, "existing-stream-after-empty"))
	require.NoError(t, echoTUICPacket(alicePacket, "existing-packet-after-empty"))

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

func TestTUICConcurrentAuthenticationAndUpdates(t *testing.T) {
	t.Parallel()

	harness := newTUICTestHarness(t, []option.TUICUser{
		{Name: "alice", UUID: tuicAliceUUID, Password: "alice-password"},
		{Name: "bob", UUID: tuicBobUUID, Password: "bob-password-a"},
	})

	const workerCount = 4
	const handshakesPerWorker = 3
	start := make(chan struct{})
	errorsChannel := make(chan error, workerCount+1)
	var waitGroup sync.WaitGroup

	waitGroup.Go(func() {
		<-start
		for index := range 12 {
			password := "bob-password-a"
			if index%2 == 0 {
				password = "bob-password-b"
			}
			_, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.TUICUser]{
				ExpectedGeneration: harness.inbound.Generation(),
				Operations: []adapter.UserOperation[option.TUICUser]{
					{
						Type: adapter.UserOperationUpdate,
						ID:   "bob",
						Value: option.TUICUser{
							Name:     "bob",
							UUID:     tuicBobUUID,
							Password: password,
						},
					},
				},
			})
			if err != nil {
				errorsChannel <- err
				return
			}
		}
	})

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
				client, err := quicTUIC.NewClient(quicTUIC.ClientOptions{
					Context:       harness.ctx,
					Dialer:        N.SystemDialer,
					ServerAddress: harness.serverAddr,
					TLSConfig:     clientTLS,
					UUID:          parseTUICTestUUID(t, tuicAliceUUID),
					Password:      "alice-password",
					Heartbeat:     time.Hour,
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
