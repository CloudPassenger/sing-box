package vmess

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	inboundAdapter "github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/usermanager"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	vmessService "github.com/sagernet/sing-vmess"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

const vmessHandshakeTimeout = 3 * time.Second

const (
	vmessUUIDLegacy    = "00000000-0000-4000-8000-000000000001"
	vmessUUIDLegacyTwo = "00000000-0000-4000-8000-000000000007"
	vmessUUIDAliceOld  = "00000000-0000-4000-8000-000000000002"
	vmessUUIDAliceNew  = "00000000-0000-4000-8000-000000000003"
	vmessUUIDBob       = "00000000-0000-4000-8000-000000000004"
	vmessUUIDSwapOne   = "00000000-0000-4000-8000-000000000005"
	vmessUUIDSwapTwo   = "00000000-0000-4000-8000-000000000006"
)

type handshakeTestRouter struct {
	metadata chan adapter.InboundContext
	failures chan error
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
	if !vmessDestinationAllowsUser(metadata.Destination.Fqdn, metadata.User) {
		r.failures <- fmt.Errorf(
			"destination %q was attributed to VMess user %q",
			metadata.Destination.Fqdn,
			metadata.User,
		)
	}
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

func vmessDestinationAllowsUser(destination string, user string) bool {
	switch destination {
	case "unnamed.managed.test":
		return user == ""
	case "swap.managed.test":
		return user == "alice" || user == "bob"
	default:
		expected, _, loaded := strings.Cut(destination, ".")
		return loaded && user == expected
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

func newHandshakeTestHarness(t *testing.T, staticUsers []option.VMessUser) *handshakeTestHarness {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	router := &handshakeTestRouter{
		metadata: make(chan adapter.InboundContext, 2048),
		failures: make(chan error, 2048),
	}
	inbound := &Inbound{
		Adapter: inboundAdapter.NewAdapter(C.TypeVMess, "managed-user-test"),
		ctx:     ctx,
		router:  router,
		logger:  logger.NOP(),
	}
	service := vmessService.NewService[adapter.UserID](
		adapter.NewUpstreamContextHandler(inbound.newConnectionEx, inbound.newPacketConnectionEx),
	)
	inbound.service = service
	require.NoError(t, inbound.initializeUserManager(ctx, staticUsers))
	require.NoError(t, service.Start())

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
	go harness.acceptConnections(service)
	t.Cleanup(harness.close)
	return harness
}

func (h *handshakeTestHarness) acceptConnections(service *vmessService.Service[adapter.UserID]) {
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

func (h *handshakeTestHarness) open(
	userUUID string,
	alterID int,
	expectedUser string,
	payload string,
) (net.Conn, error) {
	client, err := vmessService.NewClient(userUUID, "none", alterID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(h.ctx, vmessHandshakeTimeout)
	defer cancel()
	var dialer net.Dialer
	upstream, err := dialer.DialContext(ctx, "tcp", h.listener.Addr().String())
	if err != nil {
		return nil, err
	}

	destinationUser := expectedUser
	if destinationUser == "" {
		destinationUser = "unnamed"
	}
	conn, err := client.DialConn(upstream, M.Socksaddr{
		Fqdn: destinationUser + ".managed.test",
		Port: 443,
	})
	if err != nil {
		_ = upstream.Close()
		return nil, err
	}
	if err := echoVMess(conn, payload); err != nil {
		_ = conn.Close()
		return nil, err
	}

	h.connectionsAccess.Lock()
	h.connections = append(h.connections, conn)
	h.connectionsAccess.Unlock()
	return conn, nil
}

func (h *handshakeTestHarness) openSwap(userUUID string, payload string) (net.Conn, error) {
	client, err := vmessService.NewClient(userUUID, "none", 0)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(h.ctx, vmessHandshakeTimeout)
	defer cancel()
	var dialer net.Dialer
	upstream, err := dialer.DialContext(ctx, "tcp", h.listener.Addr().String())
	if err != nil {
		return nil, err
	}
	conn, err := client.DialConn(upstream, M.Socksaddr{
		Fqdn: "swap.managed.test",
		Port: 443,
	})
	if err != nil {
		_ = upstream.Close()
		return nil, err
	}
	if err := echoVMess(conn, payload); err != nil {
		_ = conn.Close()
		return nil, err
	}

	h.connectionsAccess.Lock()
	h.connections = append(h.connections, conn)
	h.connectionsAccess.Unlock()
	return conn, nil
}

func (h *handshakeTestHarness) requireRejected(
	t *testing.T,
	userUUID string,
	alterID int,
	expectedUser string,
) {
	t.Helper()
	conn, err := h.open(userUUID, alterID, expectedUser, "must-be-rejected")
	if err == nil {
		_ = conn.Close()
		t.Fatal("VMess credential remained accepted")
	}
}

func (h *handshakeTestHarness) requireUser(t *testing.T, expected string) {
	t.Helper()
	select {
	case metadata := <-h.router.metadata:
		require.Equal(t, expected, metadata.User)
	case failure := <-h.router.failures:
		t.Fatal(failure)
	case <-time.After(vmessHandshakeTimeout):
		t.Fatal("timed out waiting for routed VMess connection")
	}
}

func (h *handshakeTestHarness) requireNoRoutingFailures(t *testing.T) {
	t.Helper()
	for {
		select {
		case err := <-h.router.failures:
			t.Fatal(err)
		default:
			return
		}
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
		_ = h.inbound.service.Close()

		done := make(chan struct{})
		go func() {
			h.connectionGroup.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(vmessHandshakeTimeout):
		}
	})
}

func echoVMess(conn net.Conn, payload string) error {
	if err := conn.SetDeadline(time.Now().Add(vmessHandshakeTimeout)); err != nil {
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

func TestVMessUserBackendIdentityAndValidation(t *testing.T) {
	t.Parallel()

	service := vmessService.NewService[adapter.UserID](nil)
	backend := newUserBackend(service, []option.VMessUser{
		{UUID: vmessUUIDLegacy},
	})

	stableID, err := backend.StableID(option.VMessUser{
		Name: "alice",
		UUID: vmessUUIDAliceOld,
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserID("alice"), stableID)
	_, err = backend.StableID(option.VMessUser{UUID: vmessUUIDAliceOld})
	require.ErrorContains(t, err, "empty VMess user name")

	fingerprint := backend.FingerprintUser(option.VMessUser{
		Name:    "alice",
		UUID:    vmessUUIDAliceOld,
		AlterId: 0,
	})
	require.NotEqual(t, fingerprint, backend.FingerprintUser(option.VMessUser{
		Name:    "bob",
		UUID:    vmessUUIDAliceOld,
		AlterId: 0,
	}))
	require.NotEqual(t, fingerprint, backend.FingerprintUser(option.VMessUser{
		Name:    "alice",
		UUID:    vmessUUIDAliceNew,
		AlterId: 0,
	}))
	require.NotEqual(t, fingerprint, backend.FingerprintUser(option.VMessUser{
		Name:    "alice",
		UUID:    vmessUUIDAliceOld,
		AlterId: 1,
	}))

	published, err := backend.Prepare([]usermanager.Record[option.VMessUser]{
		{
			ID: "alice",
			Value: option.VMessUser{
				Name: "alice",
				UUID: vmessUUIDLegacy,
			},
		},
	})
	require.Nil(t, published)
	require.ErrorContains(t, err, "duplicate VMess UUID credential")
	require.NotContains(t, err.Error(), vmessUUIDLegacy)

	published, err = backend.Prepare([]usermanager.Record[option.VMessUser]{
		{
			ID: "alice",
			Value: option.VMessUser{
				Name:    "alice",
				UUID:    vmessUUIDAliceOld,
				AlterId: -1,
			},
		},
	})
	require.Nil(t, published)
	require.ErrorContains(t, err, "invalid VMess alter ID")
	require.NotContains(t, err.Error(), vmessUUIDAliceOld)
}

func TestVMessManagedUsersHandshakeLifecycle(t *testing.T) {
	t.Parallel()

	harness := newHandshakeTestHarness(t, []option.VMessUser{
		{UUID: vmessUUIDLegacy},
		{UUID: vmessUUIDLegacyTwo},
		{Name: "alice", UUID: vmessUUIDAliceOld},
		{Name: "bob", UUID: vmessUUIDBob},
	})
	require.Equal(t, adapter.UserGeneration(1), harness.inbound.Generation())

	unnamedConn, err := harness.open(vmessUUIDLegacy, 0, "", "unnamed-before-updates")
	require.NoError(t, err)
	harness.requireUser(t, "")
	_ = unnamedConn.Close()

	establishedConn, err := harness.open(vmessUUIDAliceOld, 0, "alice", "alice-before-updates")
	require.NoError(t, err)
	harness.requireUser(t, "alice")

	reorderResult, err := harness.inbound.ReplaceUsers(
		harness.ctx,
		harness.inbound.Generation(),
		"",
		"",
		[]option.VMessUser{
			{Name: "bob", UUID: vmessUUIDBob},
			{Name: "alice", UUID: vmessUUIDAliceOld},
		},
	)
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(2), reorderResult.Generation)

	bobConn, err := harness.open(vmessUUIDBob, 0, "bob", "bob-after-reorder")
	require.NoError(t, err)
	harness.requireUser(t, "bob")
	_ = bobConn.Close()
	aliceAfterReorder, err := harness.open(vmessUUIDAliceOld, 0, "alice", "alice-after-reorder")
	require.NoError(t, err)
	harness.requireUser(t, "alice")
	_ = aliceAfterReorder.Close()

	shrinkResult, err := harness.inbound.ReplaceUsers(
		harness.ctx,
		harness.inbound.Generation(),
		"",
		"",
		[]option.VMessUser{
			{Name: "alice", UUID: vmessUUIDAliceOld},
		},
	)
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(3), shrinkResult.Generation)
	harness.requireRejected(t, vmessUUIDBob, 0, "bob")
	require.NoError(t, echoVMess(establishedConn, "alice-established-after-shrink"))

	rotateResult, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.VMessUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.VMessUser]{
			{
				Type: adapter.UserOperationUpdate,
				ID:   "alice",
				Value: option.VMessUser{
					Name: "alice",
					UUID: vmessUUIDAliceNew,
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(4), rotateResult.Generation)
	harness.requireRejected(t, vmessUUIDAliceOld, 0, "alice")
	require.NoError(t, echoVMess(establishedConn, "alice-established-after-rotation"))

	rotatedConn, err := harness.open(vmessUUIDAliceNew, 0, "alice", "alice-new-credential")
	require.NoError(t, err)
	harness.requireUser(t, "alice")
	_ = rotatedConn.Close()

	generationBeforeCollision := harness.inbound.Generation()
	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.VMessUser]{
		ExpectedGeneration: generationBeforeCollision,
		Operations: []adapter.UserOperation[option.VMessUser]{
			{
				Type: adapter.UserOperationAdd,
				ID:   "bob",
				Value: option.VMessUser{
					Name: "bob",
					UUID: vmessUUIDAliceNew,
				},
			},
		},
	})
	require.ErrorIs(t, err, usermanager.ErrBackendPrepareFailure)
	require.NotContains(t, err.Error(), vmessUUIDAliceNew)
	require.Equal(t, generationBeforeCollision, harness.inbound.Generation())

	postRollbackConn, err := harness.open(vmessUUIDAliceNew, 0, "alice", "alice-after-collision-rollback")
	require.NoError(t, err)
	harness.requireUser(t, "alice")
	_ = postRollbackConn.Close()

	deleteResult, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.VMessUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.VMessUser]{
			{
				Type: adapter.UserOperationDelete,
				ID:   "alice",
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(5), deleteResult.Generation)
	harness.requireRejected(t, vmessUUIDAliceNew, 0, "alice")
	require.NoError(t, echoVMess(establishedConn, "alice-established-after-delete"))
	_ = establishedConn.Close()

	unnamedAfterUpdates, err := harness.open(vmessUUIDLegacy, 0, "", "unnamed-after-updates")
	require.NoError(t, err)
	harness.requireUser(t, "")
	_ = unnamedAfterUpdates.Close()
	secondUnnamedAfterUpdates, err := harness.open(vmessUUIDLegacyTwo, 0, "", "second-unnamed-after-updates")
	require.NoError(t, err)
	harness.requireUser(t, "")
	_ = secondUnnamedAfterUpdates.Close()
	harness.requireNoRoutingFailures(t)
}

func TestVMessAlterIDRuntimeLifecycle(t *testing.T) {
	t.Parallel()

	harness := newHandshakeTestHarness(t, nil)
	_, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.VMessUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.VMessUser]{
			{
				Type: adapter.UserOperationAdd,
				ID:   "legacy",
				Value: option.VMessUser{
					Name:    "legacy",
					UUID:    vmessUUIDLegacy,
					AlterId: 1,
				},
			},
		},
	})
	require.NoError(t, err)

	legacyConn, err := harness.open(vmessUUIDLegacy, 1, "legacy", "legacy-after-runtime-add")
	require.NoError(t, err)
	harness.requireUser(t, "legacy")
	_ = legacyConn.Close()

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.VMessUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.VMessUser]{
			{
				Type: adapter.UserOperationUpdate,
				ID:   "legacy",
				Value: option.VMessUser{
					Name: "legacy",
					UUID: vmessUUIDLegacy,
				},
			},
		},
	})
	require.NoError(t, err)
	harness.requireRejected(t, vmessUUIDLegacy, 1, "legacy")

	modernConn, err := harness.open(vmessUUIDLegacy, 0, "legacy", "modern-after-alter-id-removal")
	require.NoError(t, err)
	harness.requireUser(t, "legacy")
	_ = modernConn.Close()
	harness.requireNoRoutingFailures(t)
}

func TestVMessConcurrentAuthenticationUsesOneGeneration(t *testing.T) {
	t.Parallel()

	harness := newHandshakeTestHarness(t, []option.VMessUser{
		{Name: "alice", UUID: vmessUUIDSwapOne},
		{Name: "bob", UUID: vmessUUIDSwapTwo},
	})

	const workerCount = 6
	const attemptsPerWorker = 32
	start := make(chan struct{})
	workerErrors := make(chan error, workerCount*attemptsPerWorker)
	var successCount atomic.Int64
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for workerIndex := range workerCount {
		go func() {
			defer workers.Done()
			<-start
			for attempt := range attemptsPerWorker {
				userUUID := vmessUUIDSwapOne
				if (workerIndex+attempt)%2 == 1 {
					userUUID = vmessUUIDSwapTwo
				}
				conn, err := harness.openSwap(
					userUUID,
					fmt.Sprintf("worker-%d-attempt-%d", workerIndex, attempt),
				)
				if err != nil {
					workerErrors <- err
					continue
				}
				successCount.Add(1)
				_ = conn.Close()
			}
		}()
	}

	close(start)
	for updateIndex := range 48 {
		users := []option.VMessUser{
			{Name: "alice", UUID: vmessUUIDSwapOne},
			{Name: "bob", UUID: vmessUUIDSwapTwo},
		}
		if updateIndex%2 == 0 {
			users = []option.VMessUser{
				{Name: "bob", UUID: vmessUUIDSwapOne},
				{Name: "alice", UUID: vmessUUIDSwapTwo},
			}
		}
		_, err := harness.inbound.ReplaceUsers(
			harness.ctx,
			harness.inbound.Generation(),
			"",
			"",
			users,
		)
		require.NoError(t, err)
		time.Sleep(time.Millisecond)
	}
	workers.Wait()
	close(workerErrors)
	for err := range workerErrors {
		t.Errorf("VMess handshake observed a mixed generation: %v", err)
	}
	require.Equal(t, int64(workerCount*attemptsPerWorker), successCount.Load())
	harness.requireNoRoutingFailures(t)
}
