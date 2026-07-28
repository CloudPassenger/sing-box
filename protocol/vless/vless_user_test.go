package vless

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
	vmessvless "github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

const vlessHandshakeTimeout = 3 * time.Second

type vlessHandshakeTestRouter struct {
	metadata chan adapter.InboundContext
}

func (r *vlessHandshakeTestRouter) RouteConnection(
	ctx context.Context,
	conn net.Conn,
	metadata adapter.InboundContext,
) error {
	return conn.Close()
}

func (r *vlessHandshakeTestRouter) RoutePacketConnection(
	ctx context.Context,
	conn N.PacketConn,
	metadata adapter.InboundContext,
) error {
	return conn.Close()
}

func (r *vlessHandshakeTestRouter) RouteConnectionEx(
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

func (r *vlessHandshakeTestRouter) RoutePacketConnectionEx(
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

type vlessHandshakeTestHarness struct {
	ctx               context.Context
	cancel            context.CancelFunc
	inbound           *Inbound
	router            *vlessHandshakeTestRouter
	listener          net.Listener
	acceptDone        chan struct{}
	connectionGroup   sync.WaitGroup
	connectionsAccess sync.Mutex
	connections       []net.Conn
	closeOnce         sync.Once
}

func newVLESSHandshakeTestHarness(
	t *testing.T,
	staticUsers []option.VLESSUser,
) *vlessHandshakeTestHarness {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	router := &vlessHandshakeTestRouter{
		metadata: make(chan adapter.InboundContext, 4096),
	}
	inbound := &Inbound{
		Adapter: inboundAdapter.NewAdapter(C.TypeVLESS, "managed-user-test"),
		ctx:     ctx,
		router:  router,
		logger:  logger.NOP(),
	}
	service, err := vmessvless.NewService[adapter.UserID](
		ctx,
		"",
		logger.NOP(),
		adapter.NewUpstreamContextHandlerEx(
			inbound.newConnectionEx,
			inbound.newPacketConnectionEx,
		),
	)
	require.NoError(t, err)
	inbound.service = service
	require.NoError(t, inbound.initializeUserManager(ctx, staticUsers))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	harness := &vlessHandshakeTestHarness{
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

func (h *vlessHandshakeTestHarness) acceptConnections() {
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
			h.inbound.NewConnectionEx(h.ctx, conn, adapter.InboundContext{}, nil)
		}()
	}
}

func (h *vlessHandshakeTestHarness) openProxy(
	userUUID string,
	flow string,
	payload string,
) (net.Conn, error) {
	client, err := vmessvless.NewClient(h.ctx, userUUID, flow, "", logger.NOP())
	if err != nil {
		return nil, err
	}
	rawConn, err := net.DialTimeout("tcp", h.listener.Addr().String(), vlessHandshakeTimeout)
	if err != nil {
		return nil, err
	}
	if err := rawConn.SetDeadline(time.Now().Add(vlessHandshakeTimeout)); err != nil {
		_ = rawConn.Close()
		return nil, err
	}
	conn, err := client.DialConn(rawConn, M.Socksaddr{
		Fqdn: "example.com",
		Port: 443,
	})
	if err != nil {
		_ = rawConn.Close()
		return nil, err
	}
	if err := echoVLESS(conn, payload); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	h.connectionsAccess.Lock()
	h.connections = append(h.connections, conn)
	h.connectionsAccess.Unlock()
	return conn, nil
}

func (h *vlessHandshakeTestHarness) requireRejected(
	t *testing.T,
	userUUID string,
	flow string,
) {
	t.Helper()
	conn, err := h.openProxy(userUUID, flow, "must-be-rejected")
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err)
}

func (h *vlessHandshakeTestHarness) requireUser(t *testing.T, expected string) {
	t.Helper()
	select {
	case metadata := <-h.router.metadata:
		require.Equal(t, expected, metadata.User)
	case <-time.After(vlessHandshakeTimeout):
		t.Fatal("timed out waiting for routed VLESS connection")
	}
}

func (h *vlessHandshakeTestHarness) close() {
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
		case <-time.After(vlessHandshakeTimeout):
		}
	})
}

func echoVLESS(conn net.Conn, payload string) error {
	if err := conn.SetDeadline(time.Now().Add(vlessHandshakeTimeout)); err != nil {
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

func TestVLESSUserBackendIdentityAndFingerprint(t *testing.T) {
	t.Parallel()
	backend := newUserBackend(nil, nil)
	user := option.VLESSUser{
		Name: "alice",
		UUID: "11111111-1111-4111-8111-111111111111",
		Flow: "",
	}

	stableID, err := backend.StableID(user)
	require.NoError(t, err)
	require.Equal(t, adapter.UserID("alice"), stableID)
	_, err = backend.StableID(option.VLESSUser{UUID: user.UUID})
	require.ErrorContains(t, err, "empty VLESS user name")

	fingerprint := backend.FingerprintUser(user)
	changedName := user
	changedName.Name = "bob"
	require.NotEqual(t, fingerprint, backend.FingerprintUser(changedName))
	changedUUID := user
	changedUUID.UUID = "22222222-2222-4222-8222-222222222222"
	require.NotEqual(t, fingerprint, backend.FingerprintUser(changedUUID))
	changedFlow := user
	changedFlow.Flow = vmessvless.FlowVision
	require.NotEqual(t, fingerprint, backend.FingerprintUser(changedFlow))
}

func TestVLESSManagedUsersHandshakeLifecycle(t *testing.T) {
	t.Parallel()
	const (
		legacyUUID   = "10000000-0000-4000-8000-000000000001"
		aliceOldUUID = "20000000-0000-4000-8000-000000000002"
		aliceNewUUID = "30000000-0000-4000-8000-000000000003"
	)
	harness := newVLESSHandshakeTestHarness(t, []option.VLESSUser{
		{UUID: legacyUUID},
	})
	require.Equal(t, adapter.UserGeneration(1), harness.inbound.Generation())

	legacyConn, err := harness.openProxy(legacyUUID, "", "legacy-before-updates")
	require.NoError(t, err)
	harness.requireUser(t, "")
	require.NoError(t, legacyConn.Close())

	addResult, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.VLESSUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.VLESSUser]{
			{
				Type: adapter.UserOperationAdd,
				ID:   "alice",
				Value: option.VLESSUser{
					Name: "alice",
					UUID: aliceOldUUID,
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(2), addResult.Generation)
	require.Equal(t, []adapter.UserID{"alice"}, addResult.Added)

	generationBeforeCollision := harness.inbound.Generation()
	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.VLESSUser]{
		ExpectedGeneration: generationBeforeCollision,
		Operations: []adapter.UserOperation[option.VLESSUser]{
			{
				Type: adapter.UserOperationAdd,
				ID:   "bob",
				Value: option.VLESSUser{
					Name: "bob",
					UUID: aliceOldUUID,
				},
			},
		},
	})
	require.ErrorIs(t, err, usermanager.ErrBackendPrepareFailure)
	require.NotContains(t, err.Error(), aliceOldUUID)
	require.Equal(t, generationBeforeCollision, harness.inbound.Generation())

	rollbackConn, err := harness.openProxy(aliceOldUUID, "", "alice-after-collision")
	require.NoError(t, err)
	harness.requireUser(t, "alice")
	require.NoError(t, rollbackConn.Close())

	establishedConn, err := harness.openProxy(aliceOldUUID, "", "alice-before-rotation")
	require.NoError(t, err)
	harness.requireUser(t, "alice")

	rotateResult, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.VLESSUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.VLESSUser]{
			{
				Type: adapter.UserOperationUpdate,
				ID:   "alice",
				Value: option.VLESSUser{
					Name: "alice",
					UUID: aliceNewUUID,
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []adapter.UserID{"alice"}, rotateResult.Updated)
	harness.requireRejected(t, aliceOldUUID, "")

	rotatedConn, err := harness.openProxy(aliceNewUUID, "", "alice-new-credential")
	require.NoError(t, err)
	harness.requireUser(t, "alice")
	require.NoError(t, rotatedConn.Close())
	require.NoError(t, echoVLESS(establishedConn, "alice-established-after-rotation"))

	deleteResult, err := harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.VLESSUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.VLESSUser]{
			{
				Type: adapter.UserOperationDelete,
				ID:   "alice",
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []adapter.UserID{"alice"}, deleteResult.Deleted)
	harness.requireRejected(t, aliceNewUUID, "")
	require.NoError(t, echoVLESS(establishedConn, "alice-established-after-delete"))
	require.NoError(t, establishedConn.Close())

	legacyAfterConn, err := harness.openProxy(legacyUUID, "", "legacy-after-updates")
	require.NoError(t, err)
	harness.requireUser(t, "")
	require.NoError(t, legacyAfterConn.Close())
}

func TestVLESSManagedUsersReorderSwapAndShrink(t *testing.T) {
	t.Parallel()
	const (
		aliceUUID = "40000000-0000-4000-8000-000000000004"
		bobUUID   = "50000000-0000-4000-8000-000000000005"
		carolUUID = "60000000-0000-4000-8000-000000000006"
	)
	alice := option.VLESSUser{Name: "alice", UUID: aliceUUID}
	bob := option.VLESSUser{Name: "bob", UUID: bobUUID}
	carol := option.VLESSUser{Name: "carol", UUID: carolUUID}
	harness := newVLESSHandshakeTestHarness(t, []option.VLESSUser{bob, alice, carol})
	require.Equal(t, adapter.UserGeneration(1), harness.inbound.Generation())

	for _, testUser := range []option.VLESSUser{alice, bob, carol} {
		conn, err := harness.openProxy(testUser.UUID, "", "static-"+testUser.Name)
		require.NoError(t, err)
		harness.requireUser(t, testUser.Name)
		require.NoError(t, conn.Close())
	}

	_, err := harness.inbound.ReplaceUsers(
		harness.ctx,
		harness.inbound.Generation(),
		"",
		"",
		[]option.VLESSUser{carol, alice, bob},
	)
	require.NoError(t, err)
	for _, testUser := range []option.VLESSUser{bob, carol, alice} {
		conn, openErr := harness.openProxy(testUser.UUID, "", "reordered-"+testUser.Name)
		require.NoError(t, openErr)
		harness.requireUser(t, testUser.Name)
		require.NoError(t, conn.Close())
	}

	_, err = harness.inbound.ApplyUsers(harness.ctx, adapter.UserTransaction[option.VLESSUser]{
		ExpectedGeneration: harness.inbound.Generation(),
		Operations: []adapter.UserOperation[option.VLESSUser]{
			{
				Type: adapter.UserOperationUpdate,
				ID:   "alice",
				Value: option.VLESSUser{
					Name: "alice",
					UUID: bobUUID,
				},
			},
			{
				Type: adapter.UserOperationUpdate,
				ID:   "bob",
				Value: option.VLESSUser{
					Name: "bob",
					UUID: aliceUUID,
				},
			},
		},
	})
	require.NoError(t, err)

	aliceAfterSwapConn, err := harness.openProxy(bobUUID, "", "alice-after-swap")
	require.NoError(t, err)
	harness.requireUser(t, "alice")
	require.NoError(t, aliceAfterSwapConn.Close())

	bobEstablishedConn, err := harness.openProxy(aliceUUID, "", "bob-before-shrink")
	require.NoError(t, err)
	harness.requireUser(t, "bob")

	alice.UUID = bobUUID
	_, err = harness.inbound.ReplaceUsers(
		harness.ctx,
		harness.inbound.Generation(),
		"",
		"",
		[]option.VLESSUser{carol, alice},
	)
	require.NoError(t, err)
	harness.requireRejected(t, aliceUUID, "")
	require.NoError(t, echoVLESS(bobEstablishedConn, "bob-established-after-shrink"))
	require.NoError(t, bobEstablishedConn.Close())

	for _, testUser := range []option.VLESSUser{alice, carol} {
		conn, openErr := harness.openProxy(testUser.UUID, "", "after-shrink-"+testUser.Name)
		require.NoError(t, openErr)
		harness.requireUser(t, testUser.Name)
		require.NoError(t, conn.Close())
	}
}

func TestVLESSManagedUsersConcurrentFlowAndIdentityPublication(t *testing.T) {
	t.Parallel()
	const sharedUUID = "70000000-0000-4000-8000-000000000007"
	plainUser := option.VLESSUser{
		Name: "plain-user",
		UUID: sharedUUID,
	}
	visionUser := option.VLESSUser{
		Name: "vision-user",
		UUID: sharedUUID,
		Flow: vmessvless.FlowVision,
	}
	harness := newVLESSHandshakeTestHarness(t, []option.VLESSUser{plainUser})

	initialConn, err := harness.openProxy(sharedUUID, "", "plain-before-race")
	require.NoError(t, err)
	harness.requireUser(t, "plain-user")
	require.NoError(t, initialConn.Close())

	stop := make(chan struct{})
	updateErrors := make(chan error, 1)
	var updateGroup sync.WaitGroup
	updateGroup.Add(1)
	go func() {
		defer updateGroup.Done()
		users := []option.VLESSUser{visionUser}
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, updateErr := harness.inbound.ReplaceUsers(
				harness.ctx,
				0,
				"",
				"",
				users,
			)
			if updateErr != nil {
				updateErrors <- updateErr
				return
			}
			if users[0].Name == visionUser.Name {
				users = []option.VLESSUser{plainUser}
			} else {
				users = []option.VLESSUser{visionUser}
			}
			time.Sleep(25 * time.Microsecond)
		}
	}()

	var stopOnce sync.Once
	stopUpdates := func() {
		stopOnce.Do(func() {
			close(stop)
			updateGroup.Wait()
		})
	}
	defer stopUpdates()

	for index := range 300 {
		select {
		case updateErr := <-updateErrors:
			t.Fatalf("ReplaceUsers() error = %v", updateErr)
		default:
		}
		conn, authErr := harness.openProxy(
			sharedUUID,
			"",
			fmt.Sprintf("concurrent-auth-%d", index),
		)
		if authErr != nil {
			continue
		}
		harness.requireUser(t, "plain-user")
		require.NoError(t, conn.Close())
	}

	stopUpdates()
	select {
	case updateErr := <-updateErrors:
		t.Fatalf("ReplaceUsers() error = %v", updateErr)
	default:
	}
}
