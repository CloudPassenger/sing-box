package trojan

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
	"github.com/sagernet/sing-box/common/usermanager"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	trojanTransport "github.com/sagernet/sing-box/transport/trojan"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

const trojanHandshakeTimeout = 3 * time.Second

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
	ctx               context.Context
	cancel            context.CancelFunc
	inbound           *Inbound
	router            *handshakeTestRouter
	listener          net.Listener
	acceptDone        chan struct{}
	connectionGroup   sync.WaitGroup
	connectionsAccess sync.Mutex
	connections       []net.Conn
	closeOnce         sync.Once
}

func newHandshakeTestHarness(t *testing.T, staticUsers []option.TrojanUser) *handshakeTestHarness {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	router := &handshakeTestRouter{
		metadata: make(chan adapter.InboundContext, 64),
	}
	inbound := &Inbound{
		Adapter: inboundAdapter.NewAdapter(C.TypeTrojan, "managed-user-test"),
		router:  router,
		logger:  logger.NOP(),
	}
	service := trojanTransport.NewService[adapter.UserID](
		adapter.NewUpstreamContextHandler(inbound.newConnection, inbound.newPacketConnection),
		nil,
		logger.NOP(),
	)
	inbound.service = service
	require.NoError(t, inbound.initializeUserManager(ctx, staticUsers))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	harness := &handshakeTestHarness{
		ctx:         ctx,
		cancel:      cancel,
		inbound:     inbound,
		router:      router,
		listener:    listener,
		acceptDone:  make(chan struct{}),
		connections: make([]net.Conn, 0),
	}
	go harness.acceptConnections()
	t.Cleanup(harness.close)
	return harness
}

func (h *handshakeTestHarness) acceptConnections() {
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
			_ = h.inbound.service.NewConnection(h.ctx, conn, M.Socksaddr{}, nil)
		}()
	}
}

func (h *handshakeTestHarness) openProxy(password string, payload string) (net.Conn, error) {
	rawConn, err := net.DialTimeout("tcp", h.listener.Addr().String(), trojanHandshakeTimeout)
	if err != nil {
		return nil, err
	}
	clientConn := trojanTransport.NewClientConn(rawConn, trojanTransport.Key(password), M.Socksaddr{
		Fqdn: "example.com",
		Port: 443,
	})
	h.connectionsAccess.Lock()
	h.connections = append(h.connections, clientConn)
	h.connectionsAccess.Unlock()
	if err := echoTrojan(clientConn, payload); err != nil {
		_ = clientConn.Close()
		return nil, err
	}
	return clientConn, nil
}

func (h *handshakeTestHarness) requirePasswordRejected(t *testing.T, password string) {
	t.Helper()
	conn, err := h.openProxy(password, "must-be-rejected")
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err)
}

func (h *handshakeTestHarness) requireUser(t *testing.T, expected string) {
	t.Helper()
	select {
	case metadata := <-h.router.metadata:
		require.Equal(t, expected, metadata.User)
	case <-time.After(trojanHandshakeTimeout):
		t.Fatal("timed out waiting for routed Trojan connection")
	}
}

func (h *handshakeTestHarness) close() {
	h.closeOnce.Do(func() {
		h.connectionsAccess.Lock()
		connections := append([]net.Conn(nil), h.connections...)
		h.connections = nil
		h.connectionsAccess.Unlock()
		for _, conn := range connections {
			_ = conn.Close()
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
		case <-time.After(trojanHandshakeTimeout):
		}
	})
}

func echoTrojan(conn net.Conn, payload string) error {
	if err := conn.SetDeadline(time.Now().Add(trojanHandshakeTimeout)); err != nil {
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

type discardTrojanHandler struct{}

func (discardTrojanHandler) NewConnectionEx(
	ctx context.Context,
	conn net.Conn,
	source M.Socksaddr,
	destination M.Socksaddr,
	onClose N.CloseHandlerFunc,
) {
}

func (discardTrojanHandler) NewPacketConnectionEx(
	ctx context.Context,
	conn N.PacketConn,
	source M.Socksaddr,
	destination M.Socksaddr,
	onClose N.CloseHandlerFunc,
) {
}

func TestTrojanUserBackendIdentityAndValidation(t *testing.T) {
	t.Parallel()

	service := trojanTransport.NewService[adapter.UserID](discardTrojanHandler{}, nil, logger.NOP())
	backend := newUserBackend(service, []string{"do-not-leak"})
	stableID, err := backend.StableID(option.TrojanUser{
		Name:     "alice",
		Password: "alice-password",
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserID("alice"), stableID)

	_, err = backend.StableID(option.TrojanUser{Password: "alice-password"})
	require.ErrorContains(t, err, "empty Trojan user name")

	baseFingerprint := backend.FingerprintUser(option.TrojanUser{
		Name:     "alice",
		Password: "alice-password",
	})
	require.NotEqual(t, baseFingerprint, backend.FingerprintUser(option.TrojanUser{
		Name:     "bob",
		Password: "alice-password",
	}))
	require.NotEqual(t, baseFingerprint, backend.FingerprintUser(option.TrojanUser{
		Name:     "alice",
		Password: "rotated-password",
	}))

	published, err := backend.Prepare([]usermanager.Record[option.TrojanUser]{
		{
			ID: "alice",
			Value: option.TrojanUser{
				Name:     "alice",
				Password: "do-not-leak",
			},
		},
	})
	require.Nil(t, published)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "do-not-leak")
}

func TestTrojanManagedUsersHandshakeLifecycle(t *testing.T) {
	t.Parallel()

	harness := newHandshakeTestHarness(t, []option.TrojanUser{
		{Password: "legacy-unnamed-password"},
		{Password: "legacy-secondary-password"},
		{Name: "alice", Password: "alice-password"},
		{Name: "bob", Password: "bob-password"},
	})
	require.Equal(t, adapter.UserGeneration(1), harness.inbound.Generation())

	legacyConn, err := harness.openProxy("legacy-unnamed-password", "legacy-before-updates")
	require.NoError(t, err)
	harness.requireUser(t, "")
	require.NoError(t, legacyConn.Close())

	establishedAlice, err := harness.openProxy("alice-password", "alice-established")
	require.NoError(t, err)
	harness.requireUser(t, "alice")

	addResult, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.TrojanUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.TrojanUser]{
			{
				Type: adapter.UserOperationAdd,
				ID:   "charlie",
				Value: option.TrojanUser{
					Name:     "charlie",
					Password: "charlie-password",
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(2), addResult.Generation)
	require.Equal(t, []adapter.UserID{"charlie"}, addResult.Added)

	charlieConn, err := harness.openProxy("charlie-password", "charlie-added")
	require.NoError(t, err)
	harness.requireUser(t, "charlie")
	require.NoError(t, charlieConn.Close())

	collisionGeneration := harness.inbound.Generation()
	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.TrojanUser]{
		ExpectedGeneration: collisionGeneration,
		Operations: []adapter.UserOperation[option.TrojanUser]{
			{
				Type: adapter.UserOperationUpdate,
				ID:   "bob",
				Value: option.TrojanUser{
					Name:     "bob",
					Password: "alice-password",
				},
			},
		},
	})
	require.ErrorIs(t, err, usermanager.ErrBackendPrepareFailure)
	require.NotContains(t, err.Error(), "alice-password")
	require.Equal(t, collisionGeneration, harness.inbound.Generation())

	bobAfterCollision, err := harness.openProxy("bob-password", "bob-after-collision")
	require.NoError(t, err)
	harness.requireUser(t, "bob")
	require.NoError(t, bobAfterCollision.Close())

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.TrojanUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.TrojanUser]{
			{
				Type: adapter.UserOperationAdd,
				ID:   "legacy-collision",
				Value: option.TrojanUser{
					Name:     "legacy-collision",
					Password: "legacy-unnamed-password",
				},
			},
		},
	})
	require.ErrorIs(t, err, usermanager.ErrBackendPrepareFailure)
	require.NotContains(t, err.Error(), "legacy-unnamed-password")
	require.Equal(t, collisionGeneration, harness.inbound.Generation())

	reorderResult, err := harness.inbound.ReplaceUsers(
		harness.ctx,
		harness.inbound.Generation(),
		"",
		"",
		[]option.TrojanUser{
			{Name: "charlie", Password: "charlie-password"},
			{Name: "bob", Password: "bob-password"},
			{Name: "alice", Password: "alice-password"},
		},
	)
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(3), reorderResult.Generation)

	aliceAfterReorder, err := harness.openProxy("alice-password", "alice-after-reorder")
	require.NoError(t, err)
	harness.requireUser(t, "alice")
	require.NoError(t, aliceAfterReorder.Close())
	bobAfterReorder, err := harness.openProxy("bob-password", "bob-after-reorder")
	require.NoError(t, err)
	harness.requireUser(t, "bob")
	require.NoError(t, bobAfterReorder.Close())

	shrinkResult, err := harness.inbound.ReplaceUsers(
		harness.ctx,
		harness.inbound.Generation(),
		"",
		"",
		[]option.TrojanUser{
			{Name: "charlie", Password: "charlie-password"},
			{Name: "alice", Password: "alice-password"},
		},
	)
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(4), shrinkResult.Generation)
	harness.requirePasswordRejected(t, "bob-password")
	require.NoError(t, echoTrojan(establishedAlice, "alice-established-after-shrink"))

	rotateResult, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.TrojanUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.TrojanUser]{
			{
				Type: adapter.UserOperationUpdate,
				ID:   "alice",
				Value: option.TrojanUser{
					Name:     "alice",
					Password: "alice-rotated-password",
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(5), rotateResult.Generation)
	harness.requirePasswordRejected(t, "alice-password")
	require.NoError(t, echoTrojan(establishedAlice, "alice-established-after-rotation"))

	rotatedAlice, err := harness.openProxy("alice-rotated-password", "alice-rotated")
	require.NoError(t, err)
	harness.requireUser(t, "alice")
	require.NoError(t, rotatedAlice.Close())

	swapResult, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.TrojanUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.TrojanUser]{
			{
				Type: adapter.UserOperationUpdate,
				ID:   "alice",
				Value: option.TrojanUser{
					Name:     "alice",
					Password: "charlie-password",
				},
			},
			{
				Type: adapter.UserOperationUpdate,
				ID:   "charlie",
				Value: option.TrojanUser{
					Name:     "charlie",
					Password: "alice-rotated-password",
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(6), swapResult.Generation)
	require.ElementsMatch(t, []adapter.UserID{"alice", "charlie"}, swapResult.Updated)

	aliceAfterSwap, err := harness.openProxy("charlie-password", "alice-after-swap")
	require.NoError(t, err)
	harness.requireUser(t, "alice")
	require.NoError(t, aliceAfterSwap.Close())
	charlieAfterSwap, err := harness.openProxy("alice-rotated-password", "charlie-after-swap")
	require.NoError(t, err)
	harness.requireUser(t, "charlie")
	require.NoError(t, charlieAfterSwap.Close())

	deleteResult, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.TrojanUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.TrojanUser]{
			{
				Type: adapter.UserOperationDelete,
				ID:   "alice",
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(7), deleteResult.Generation)
	require.Equal(t, []adapter.UserID{"alice"}, deleteResult.Deleted)
	harness.requirePasswordRejected(t, "charlie-password")
	require.NoError(t, echoTrojan(establishedAlice, "alice-established-after-delete"))

	charlieAfterDelete, err := harness.openProxy("alice-rotated-password", "charlie-after-delete")
	require.NoError(t, err)
	harness.requireUser(t, "charlie")
	require.NoError(t, charlieAfterDelete.Close())

	legacyAfterUpdates, err := harness.openProxy("legacy-unnamed-password", "legacy-after-updates")
	require.NoError(t, err)
	harness.requireUser(t, "")
	require.NoError(t, legacyAfterUpdates.Close())
	secondaryLegacyAfterUpdates, err := harness.openProxy("legacy-secondary-password", "secondary-legacy-after-updates")
	require.NoError(t, err)
	harness.requireUser(t, "")
	require.NoError(t, secondaryLegacyAfterUpdates.Close())
	require.NoError(t, establishedAlice.Close())
}
