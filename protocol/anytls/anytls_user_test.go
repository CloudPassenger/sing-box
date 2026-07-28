package anytls

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	inboundAdapter "github.com/sagernet/sing-box/adapter/inbound"
	boxListener "github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/usermanager"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	anytls "github.com/anytls/sing-anytls"
	"github.com/anytls/sing-anytls/padding"
	"github.com/stretchr/testify/require"
)

const anyTLSHandshakeTimeout = 3 * time.Second

type handshakeTestRouter struct {
	metadata chan adapter.InboundContext
}

func (r *handshakeTestRouter) RouteConnection(
	ctx context.Context,
	conn net.Conn,
	metadata adapter.InboundContext,
) error {
	return conn.Close()
}

func (r *handshakeTestRouter) RoutePacketConnection(
	ctx context.Context,
	conn N.PacketConn,
	metadata adapter.InboundContext,
) error {
	return conn.Close()
}

func (r *handshakeTestRouter) RouteConnectionEx(
	ctx context.Context,
	conn net.Conn,
	metadata adapter.InboundContext,
	onClose N.CloseHandlerFunc,
) {
	r.metadata <- metadata
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

func (r *handshakeTestRouter) RoutePacketConnectionEx(
	ctx context.Context,
	conn N.PacketConn,
	metadata adapter.InboundContext,
	onClose N.CloseHandlerFunc,
) {
	_ = conn.Close()
	if onClose != nil {
		onClose(nil)
	}
}

type handshakeTestHarness struct {
	ctx             context.Context
	cancel          context.CancelFunc
	inbound         *Inbound
	router          *handshakeTestRouter
	listener        net.Listener
	acceptDone      chan struct{}
	connectionGroup sync.WaitGroup
	clientsAccess   sync.Mutex
	clients         []*anytls.Client
	closeOnce       sync.Once
}

func newHandshakeTestHarness(t *testing.T, staticUsers []option.AnyTLSUser) *handshakeTestHarness {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	router := &handshakeTestRouter{
		metadata: make(chan adapter.InboundContext, 16),
	}
	inbound := &Inbound{
		Adapter: inboundAdapter.NewAdapter(C.TypeAnyTLS, "managed-user-test"),
		router:  router,
		logger:  logger.NOP(),
		listener: boxListener.New(boxListener.Options{
			Context: ctx,
			Logger:  logger.NOP(),
		}),
	}

	serviceUsers := make([]anytls.User, 0, len(staticUsers))
	for _, user := range staticUsers {
		serviceUsers = append(serviceUsers, anytls.User(user))
	}
	service, err := anytls.NewService(anytls.ServiceConfig{
		PaddingScheme: padding.DefaultPaddingScheme,
		Users:         serviceUsers,
		Handler:       (*inboundHandler)(inbound),
		Logger:        logger.NOP(),
	})
	require.NoError(t, err)
	inbound.service = service
	require.NoError(t, inbound.initializeUserManager(ctx, staticUsers))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	harness := &handshakeTestHarness{
		ctx:        ctx,
		cancel:     cancel,
		inbound:    inbound,
		router:     router,
		listener:   listener,
		acceptDone: make(chan struct{}),
		clients:    make([]*anytls.Client, 0),
	}
	go harness.acceptConnections(service)
	t.Cleanup(harness.close)
	return harness
}

func (h *handshakeTestHarness) acceptConnections(service *anytls.Service) {
	defer close(h.acceptDone)
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			return
		}
		h.connectionGroup.Add(1)
		go func() {
			defer h.connectionGroup.Done()
			defer conn.Close()
			_ = service.NewConnection(h.ctx, conn, M.Socksaddr{}, nil)
		}()
	}
}

func (h *handshakeTestHarness) newClient(t *testing.T, password string) *anytls.Client {
	t.Helper()

	client, err := anytls.NewClient(h.ctx, anytls.ClientConfig{
		Password: password,
		DialOut: func(ctx context.Context) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", h.listener.Addr().String())
		},
		Logger: logger.NOP(),
	})
	require.NoError(t, err)

	h.clientsAccess.Lock()
	h.clients = append(h.clients, client)
	h.clientsAccess.Unlock()
	return client
}

func (h *handshakeTestHarness) openProxy(client *anytls.Client, payload string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(h.ctx, anyTLSHandshakeTimeout)
	defer cancel()

	conn, err := client.CreateProxy(ctx, M.Socksaddr{
		Fqdn: "example.com",
		Port: 443,
	})
	if err != nil {
		return nil, err
	}
	if err := echoAnyTLS(conn, payload); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (h *handshakeTestHarness) requirePasswordRejected(t *testing.T, password string) {
	t.Helper()

	conn, err := net.DialTimeout("tcp", h.listener.Addr().String(), anyTLSHandshakeTimeout)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(anyTLSHandshakeTimeout)))

	passwordHash := sha256.Sum256([]byte(password))
	var handshake [34]byte
	copy(handshake[:32], passwordHash[:])
	_, err = conn.Write(handshake[:])
	require.NoError(t, err)

	var response [1]byte
	_, err = conn.Read(response[:])
	require.Error(t, err)
	if netErr, loaded := err.(net.Error); loaded && netErr.Timeout() {
		t.Fatal("password remained accepted by the AnyTLS handshake")
	}
}

func (h *handshakeTestHarness) requireUser(t *testing.T, expected string) {
	t.Helper()
	select {
	case metadata := <-h.router.metadata:
		require.Equal(t, expected, metadata.User)
	case <-time.After(anyTLSHandshakeTimeout):
		t.Fatal("timed out waiting for routed AnyTLS connection")
	}
}

func (h *handshakeTestHarness) close() {
	h.closeOnce.Do(func() {
		h.clientsAccess.Lock()
		clients := append([]*anytls.Client(nil), h.clients...)
		h.clients = nil
		h.clientsAccess.Unlock()
		for _, client := range clients {
			_ = client.Close()
		}
		h.cancel()
		_ = h.listener.Close()
		<-h.acceptDone

		done := make(chan struct{})
		go func() {
			h.connectionGroup.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(anyTLSHandshakeTimeout):
		}
	})
}

func echoAnyTLS(conn net.Conn, payload string) error {
	if err := conn.SetDeadline(time.Now().Add(anyTLSHandshakeTimeout)); err != nil {
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

func TestAnyTLSUserBackendIdentityAndValidation(t *testing.T) {
	t.Parallel()

	backend := newUserBackend(nil, []anytls.User{
		{Name: "", Password: "do-not-leak"},
	})
	stableID, err := backend.StableID(option.AnyTLSUser{
		Name:     "alice",
		Password: "alice-password",
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserID("alice"), stableID)

	_, err = backend.StableID(option.AnyTLSUser{Password: "alice-password"})
	require.ErrorContains(t, err, "empty AnyTLS user name")

	baseFingerprint := backend.FingerprintUser(option.AnyTLSUser{
		Name:     "alice",
		Password: "alice-password",
	})
	require.NotEqual(t, baseFingerprint, backend.FingerprintUser(option.AnyTLSUser{
		Name:     "bob",
		Password: "alice-password",
	}))
	require.NotEqual(t, baseFingerprint, backend.FingerprintUser(option.AnyTLSUser{
		Name:     "alice",
		Password: "rotated-password",
	}))

	published, err := backend.Prepare([]usermanager.Record[option.AnyTLSUser]{
		{
			ID: "alice",
			Value: option.AnyTLSUser{
				Name:     "alice",
				Password: "do-not-leak",
			},
		},
	})
	require.Nil(t, published)
	require.ErrorContains(t, err, "duplicate AnyTLS password")
	require.NotContains(t, err.Error(), "do-not-leak")
}

func TestAnyTLSManagedUsersHandshakeLifecycle(t *testing.T) {
	t.Parallel()

	harness := newHandshakeTestHarness(t, []option.AnyTLSUser{
		{Password: "legacy-unnamed-password"},
	})
	require.Equal(t, adapter.UserGeneration(1), harness.inbound.Generation())

	legacyClient := harness.newClient(t, "legacy-unnamed-password")
	legacyConn, err := harness.openProxy(legacyClient, "legacy-before-updates")
	require.NoError(t, err)
	harness.requireUser(t, "")
	require.NoError(t, legacyConn.Close())
	require.NoError(t, legacyClient.Close())

	addResult, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.AnyTLSUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.AnyTLSUser]{
			{
				Type: adapter.UserOperationAdd,
				ID:   "alice",
				Value: option.AnyTLSUser{
					Name:     "alice",
					Password: "alice-old-password",
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(2), addResult.Generation)
	require.Equal(t, []adapter.UserID{"alice"}, addResult.Added)

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.AnyTLSUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.AnyTLSUser]{
			{
				Type: adapter.UserOperationAdd,
				ID:   "bob",
				Value: option.AnyTLSUser{
					Name:     "bob",
					Password: "alice-old-password",
				},
			},
		},
	})
	require.ErrorIs(t, err, usermanager.ErrBackendPrepareFailure)
	require.Equal(t, adapter.UserGeneration(2), harness.inbound.Generation())

	establishedClient := harness.newClient(t, "alice-old-password")
	establishedConn, err := harness.openProxy(establishedClient, "alice-before-rotation")
	require.NoError(t, err)
	harness.requireUser(t, "alice")

	rotateResult, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.AnyTLSUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.AnyTLSUser]{
			{
				Type: adapter.UserOperationUpdate,
				ID:   "alice",
				Value: option.AnyTLSUser{
					Name:     "alice",
					Password: "alice-new-password",
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(3), rotateResult.Generation)
	require.Equal(t, []adapter.UserID{"alice"}, rotateResult.Updated)
	require.NoError(t, echoAnyTLS(establishedConn, "alice-after-rotation"))

	harness.requirePasswordRejected(t, "alice-old-password")

	newPasswordClient := harness.newClient(t, "alice-new-password")
	newPasswordConn, err := harness.openProxy(newPasswordClient, "new-password-works")
	require.NoError(t, err)
	harness.requireUser(t, "alice")
	require.NoError(t, newPasswordConn.Close())
	require.NoError(t, newPasswordClient.Close())

	deleteResult, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.AnyTLSUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.AnyTLSUser]{
			{
				Type: adapter.UserOperationDelete,
				ID:   "alice",
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(4), deleteResult.Generation)
	require.Equal(t, []adapter.UserID{"alice"}, deleteResult.Deleted)

	harness.requirePasswordRejected(t, "alice-new-password")

	require.NoError(t, echoAnyTLS(establishedConn, "alice-existing-stream-after-delete"))
	require.NoError(t, establishedConn.Close())

	reusedSessionConn, err := harness.openProxy(establishedClient, "alice-existing-session-after-delete")
	require.NoError(t, err)
	harness.requireUser(t, "alice")
	require.NoError(t, reusedSessionConn.Close())
	require.NoError(t, establishedClient.Close())

	legacyAfterUpdatesClient := harness.newClient(t, "legacy-unnamed-password")
	legacyAfterUpdatesConn, err := harness.openProxy(legacyAfterUpdatesClient, "legacy-after-updates")
	require.NoError(t, err)
	harness.requireUser(t, "")
	require.NoError(t, legacyAfterUpdatesConn.Close())
	require.NoError(t, legacyAfterUpdatesClient.Close())
}
