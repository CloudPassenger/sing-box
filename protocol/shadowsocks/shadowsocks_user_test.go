package shadowsocks

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/usermanager"
	"github.com/sagernet/sing-box/option"
	ss "github.com/sagernet/sing-shadowsocks"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	"github.com/sagernet/sing/common/buf"
	SBufio "github.com/sagernet/sing/common/bufio"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

const shadowsocksHandshakeTimeout = 3 * time.Second

type shadowsocksTestRouter struct {
	adapter.Router
	tcpMetadata chan adapter.InboundContext
	udpMetadata chan adapter.InboundContext
}

func newShadowsocksTestRouter() *shadowsocksTestRouter {
	return &shadowsocksTestRouter{
		tcpMetadata: make(chan adapter.InboundContext, 4096),
		udpMetadata: make(chan adapter.InboundContext, 4096),
	}
}

func (r *shadowsocksTestRouter) RouteConnection(
	ctx context.Context,
	conn net.Conn,
	metadata adapter.InboundContext,
) error {
	r.tcpMetadata <- metadata
	buffer := make([]byte, 1024)
	for {
		n, err := conn.Read(buffer)
		if n > 0 {
			if _, writeErr := conn.Write(buffer[:n]); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			return err
		}
	}
}

func (r *shadowsocksTestRouter) RoutePacketConnection(
	ctx context.Context,
	conn N.PacketConn,
	metadata adapter.InboundContext,
) error {
	r.udpMetadata <- metadata
	for {
		packet := buf.NewPacket()
		destination, err := conn.ReadPacket(packet)
		if err != nil {
			packet.Release()
			return err
		}
		if _, err := SBufio.WritePacketBuffer(conn, packet, destination); err != nil {
			return err
		}
	}
}

func (r *shadowsocksTestRouter) RouteConnectionEx(
	ctx context.Context,
	conn net.Conn,
	metadata adapter.InboundContext,
	onClose N.CloseHandlerFunc,
) {
	err := r.RouteConnection(ctx, conn, metadata)
	if onClose != nil {
		onClose(err)
	}
}

func (r *shadowsocksTestRouter) RoutePacketConnectionEx(
	ctx context.Context,
	conn N.PacketConn,
	metadata adapter.InboundContext,
	onClose N.CloseHandlerFunc,
) {
	err := r.RoutePacketConnection(ctx, conn, metadata)
	if onClose != nil {
		onClose(err)
	}
}

//nolint:staticcheck
type shadowsocksTestService interface {
	N.TCPConnectionHandler
	N.UDPHandler
}

type shadowsocksTestHarness struct {
	ctx             context.Context
	cancel          context.CancelFunc
	inbound         adapter.Inbound
	service         shadowsocksTestService
	router          *shadowsocksTestRouter
	tcpListener     net.Listener
	udpConn         net.PacketConn
	udpPacketConn   N.NetPacketConn
	acceptDone      chan struct{}
	packetDone      chan struct{}
	connectionGroup sync.WaitGroup
	closeOnce       sync.Once
	method          string
	iPSK            []byte
}

func newShadowsocksTestHarness(
	t *testing.T,
	options option.ShadowsocksInboundOptions,
) *shadowsocksTestHarness {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	router := newShadowsocksTestRouter()
	createdInbound, err := NewInbound(ctx, router, logger.NOP(), "managed-user-test", options)
	require.NoError(t, err)

	var service shadowsocksTestService
	switch inbound := createdInbound.(type) {
	case *Inbound:
		service = inbound.service
	case *MultiInbound:
		service = inbound.service
	case *legacyMultiInbound:
		service = (*MultiInbound)(inbound).service
	default:
		t.Fatalf("unexpected Shadowsocks inbound type %T", createdInbound)
	}

	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	udpConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)

	harness := &shadowsocksTestHarness{
		ctx:           ctx,
		cancel:        cancel,
		inbound:       createdInbound,
		service:       service,
		router:        router,
		tcpListener:   tcpListener,
		udpConn:       udpConn,
		udpPacketConn: SBufio.NewPacketConn(udpConn),
		acceptDone:    make(chan struct{}),
		packetDone:    make(chan struct{}),
		method:        options.Method,
	}
	if strings.HasPrefix(options.Method, "2022-") && options.Password != "" {
		harness.iPSK, err = base64.StdEncoding.DecodeString(options.Password)
		require.NoError(t, err)
	}
	go harness.acceptConnections()
	go harness.acceptPackets()
	t.Cleanup(harness.close)
	return harness
}

//nolint:staticcheck
func (h *shadowsocksTestHarness) acceptConnections() {
	defer close(h.acceptDone)
	for {
		conn, err := h.tcpListener.Accept()
		if err != nil {
			return
		}
		h.connectionGroup.Add(1)
		go func() {
			defer h.connectionGroup.Done()
			defer conn.Close()
			_ = h.service.NewConnection(
				h.ctx,
				conn,
				M.Metadata{Source: M.SocksaddrFromNet(conn.RemoteAddr()).Unwrap()},
			)
		}()
	}
}

//nolint:staticcheck
func (h *shadowsocksTestHarness) acceptPackets() {
	defer close(h.packetDone)
	for {
		packet := buf.NewPacket()
		source, err := h.udpPacketConn.ReadPacket(packet)
		if err != nil {
			packet.Release()
			return
		}
		err = h.service.NewPacket(
			h.ctx,
			h.udpPacketConn,
			packet,
			M.Metadata{Source: source},
		)
		if err != nil {
			packet.Release()
		}
	}
}

func (h *shadowsocksTestHarness) new2022Client(t *testing.T, uPSK []byte) ss.Method {
	t.Helper()
	client, err := shadowaead_2022.New(h.method, [][]byte{h.iPSK, uPSK}, nil)
	require.NoError(t, err)
	return client
}

func (h *shadowsocksTestHarness) newLegacyClient(t *testing.T, password string) ss.Method {
	t.Helper()
	client, err := shadowaead.New(h.method, nil, password)
	require.NoError(t, err)
	return client
}

func (h *shadowsocksTestHarness) openTCP(client ss.Method, payload string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", h.tcpListener.Addr().String(), shadowsocksHandshakeTimeout)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(shadowsocksHandshakeTimeout)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	protocolConn, err := client.DialConn(conn, M.ParseSocksaddr("example.com:443"))
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := echoShadowsocksTCP(protocolConn, payload); err != nil {
		_ = protocolConn.Close()
		return nil, err
	}
	if err := protocolConn.SetDeadline(time.Time{}); err != nil {
		_ = protocolConn.Close()
		return nil, err
	}
	return protocolConn, nil
}

func (h *shadowsocksTestHarness) openUDP(client ss.Method, payload string) (N.NetPacketConn, error) {
	conn, err := net.DialTimeout("udp", h.udpConn.LocalAddr().String(), shadowsocksHandshakeTimeout)
	if err != nil {
		return nil, err
	}
	packetConn := client.DialPacketConn(conn)
	if err := packetConn.SetDeadline(time.Now().Add(shadowsocksHandshakeTimeout)); err != nil {
		_ = packetConn.Close()
		return nil, err
	}
	if err := echoShadowsocksUDP(packetConn, payload); err != nil {
		_ = packetConn.Close()
		return nil, err
	}
	if err := packetConn.SetDeadline(time.Time{}); err != nil {
		_ = packetConn.Close()
		return nil, err
	}
	return packetConn, nil
}

func (h *shadowsocksTestHarness) requireTCPUser(t *testing.T, expected string) {
	t.Helper()
	select {
	case metadata := <-h.router.tcpMetadata:
		require.Equal(t, expected, metadata.User)
	case <-time.After(shadowsocksHandshakeTimeout):
		t.Fatal("timed out waiting for routed Shadowsocks TCP connection")
	}
}

func (h *shadowsocksTestHarness) requireUDPUser(t *testing.T, expected string) {
	t.Helper()
	select {
	case metadata := <-h.router.udpMetadata:
		require.Equal(t, expected, metadata.User)
	case <-time.After(shadowsocksHandshakeTimeout):
		t.Fatal("timed out waiting for routed Shadowsocks UDP session")
	}
}

func (h *shadowsocksTestHarness) requireTCPRejected(t *testing.T, client ss.Method) {
	t.Helper()
	conn, err := h.openTCP(client, "must-be-rejected")
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err)
}

func (h *shadowsocksTestHarness) requireUDPRejected(t *testing.T, client ss.Method) {
	t.Helper()
	conn, err := net.DialTimeout("udp", h.udpConn.LocalAddr().String(), shadowsocksHandshakeTimeout)
	require.NoError(t, err)
	packetConn := client.DialPacketConn(conn)
	defer packetConn.Close()
	require.NoError(t, packetConn.SetDeadline(time.Now().Add(500*time.Millisecond)))

	packet := buf.NewPacket()
	_, err = packet.Write([]byte("must-be-rejected"))
	require.NoError(t, err)
	_, err = SBufio.WritePacketBuffer(packetConn, packet, M.ParseSocksaddr("example.com:53"))
	require.NoError(t, err)
	response := buf.NewPacket()
	defer response.Release()
	_, err = packetConn.ReadPacket(response)
	require.Error(t, err)
}

func (h *shadowsocksTestHarness) close() {
	h.closeOnce.Do(func() {
		h.cancel()
		_ = h.tcpListener.Close()
		_ = h.udpConn.Close()
		<-h.acceptDone
		<-h.packetDone

		done := make(chan struct{})
		go func() {
			h.connectionGroup.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(shadowsocksHandshakeTimeout):
		}
	})
}

func echoShadowsocksTCP(conn net.Conn, payload string) error {
	if err := conn.SetDeadline(time.Now().Add(shadowsocksHandshakeTimeout)); err != nil {
		return err
	}
	if _, err := conn.Write([]byte(payload)); err != nil {
		return err
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if string(response) != payload {
		return fmt.Errorf("TCP echo response %q does not match payload %q", response, payload)
	}
	return conn.SetDeadline(time.Time{})
}

func echoShadowsocksUDP(conn N.PacketConn, payload string) error {
	if err := conn.SetDeadline(time.Now().Add(shadowsocksHandshakeTimeout)); err != nil {
		return err
	}
	packet := buf.NewPacket()
	if _, err := packet.Write([]byte(payload)); err != nil {
		packet.Release()
		return err
	}
	if _, err := SBufio.WritePacketBuffer(conn, packet, M.ParseSocksaddr("example.com:53")); err != nil {
		return err
	}
	response := buf.NewPacket()
	defer response.Release()
	if _, err := conn.ReadPacket(response); err != nil {
		return err
	}
	if !bytes.Equal(response.Bytes(), []byte(payload)) {
		return fmt.Errorf("UDP echo response %q does not match payload %q", response.Bytes(), payload)
	}
	return conn.SetDeadline(time.Time{})
}

func shadowsocksTestKey(value byte) []byte {
	return bytes.Repeat([]byte{value}, 16)
}

func shadowsocksTestPassword(key []byte) string {
	return base64.StdEncoding.EncodeToString(key)
}

func TestShadowsocksUserBackendIdentityAndFingerprint(t *testing.T) {
	t.Parallel()

	backend := newShadowsocksUserBackend(nil, nil)
	stableID, err := backend.StableID(option.ShadowsocksUser{Name: "alice", Password: "password"})
	require.NoError(t, err)
	require.Equal(t, adapter.UserID("alice"), stableID)
	_, err = backend.StableID(option.ShadowsocksUser{Password: "password"})
	require.ErrorContains(t, err, "empty Shadowsocks user name")

	fingerprint := backend.FingerprintUser(option.ShadowsocksUser{Name: "alice", Password: "password"})
	require.NotEqual(t, fingerprint, backend.FingerprintUser(option.ShadowsocksUser{Name: "bob", Password: "password"}))
	require.NotEqual(t, fingerprint, backend.FingerprintUser(option.ShadowsocksUser{Name: "alice", Password: "rotated"}))
}

func TestShadowsocksManaged2022Methods(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		method  string
		keySize int
	}{
		{name: "AES-128-GCM", method: "2022-blake3-aes-128-gcm", keySize: 16},
		{name: "AES-256-GCM", method: "2022-blake3-aes-256-gcm", keySize: 32},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			iPSK := bytes.Repeat([]byte{0x08}, test.keySize)
			userKey := bytes.Repeat([]byte{0x09}, test.keySize)
			harness := newShadowsocksTestHarness(t, option.ShadowsocksInboundOptions{
				Method:   test.method,
				Password: shadowsocksTestPassword(iPSK),
				Users: []option.ShadowsocksUser{
					{Name: "alice", Password: shadowsocksTestPassword(userKey)},
				},
			})
			_, managed := harness.inbound.(adapter.ManagedUserManager[option.ShadowsocksUser])
			require.True(t, managed)

			conn, err := harness.openTCP(harness.new2022Client(t, userKey), "managed-method")
			require.NoError(t, err)
			harness.requireTCPUser(t, "alice")
			_ = conn.Close()
		})
	}
}

func TestShadowsocksManagedUsersTCPHandshakeLifecycle(t *testing.T) {
	t.Parallel()

	iPSK := shadowsocksTestKey(0x10)
	unmanagedKey := shadowsocksTestKey(0x11)
	aliceKeyA := shadowsocksTestKey(0x12)
	aliceKeyB := shadowsocksTestKey(0x13)
	bobKey := shadowsocksTestKey(0x14)
	harness := newShadowsocksTestHarness(t, option.ShadowsocksInboundOptions{
		Method:   "2022-blake3-aes-128-gcm",
		Password: shadowsocksTestPassword(iPSK),
		Users: []option.ShadowsocksUser{
			{Password: shadowsocksTestPassword(unmanagedKey)},
		},
	})
	managed := harness.inbound.(adapter.ManagedUserManager[option.ShadowsocksUser])
	require.Equal(t, adapter.UserGeneration(1), managed.Generation())

	unmanagedConn, err := harness.openTCP(harness.new2022Client(t, unmanagedKey), "unmanaged-before-updates")
	require.NoError(t, err)
	harness.requireTCPUser(t, "")
	_ = unmanagedConn.Close()
	_, err = managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
		ExpectedGeneration: managed.Generation(),
		Operations: []adapter.UserOperation[option.ShadowsocksUser]{
			{
				Type: adapter.UserOperationAdd,
				ID:   "base-collision",
				Value: option.ShadowsocksUser{
					Name:     "base-collision",
					Password: shadowsocksTestPassword(unmanagedKey),
				},
			},
		},
	})
	require.ErrorIs(t, err, usermanager.ErrBackendPrepareFailure)
	require.Equal(t, adapter.UserGeneration(1), managed.Generation())

	addResult, err := managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
		ExpectedGeneration: managed.Generation(),
		Operations: []adapter.UserOperation[option.ShadowsocksUser]{
			{
				Type: adapter.UserOperationAdd,
				ID:   "alice",
				Value: option.ShadowsocksUser{
					Name:     "alice",
					Password: shadowsocksTestPassword(aliceKeyA),
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(2), addResult.Generation)

	_, err = managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
		ExpectedGeneration: managed.Generation(),
		Operations: []adapter.UserOperation[option.ShadowsocksUser]{
			{
				Type: adapter.UserOperationAdd,
				ID:   "bob",
				Value: option.ShadowsocksUser{
					Name:     "bob",
					Password: shadowsocksTestPassword(aliceKeyA),
				},
			},
		},
	})
	require.ErrorIs(t, err, usermanager.ErrBackendPrepareFailure)
	require.Equal(t, adapter.UserGeneration(2), managed.Generation())

	establishedConn, err := harness.openTCP(harness.new2022Client(t, aliceKeyA), "alice-before-rotation")
	require.NoError(t, err)
	harness.requireTCPUser(t, "alice")

	_, err = managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
		ExpectedGeneration: managed.Generation(),
		Operations: []adapter.UserOperation[option.ShadowsocksUser]{
			{
				Type: adapter.UserOperationUpdate,
				ID:   "alice",
				Value: option.ShadowsocksUser{
					Name:     "alice",
					Password: shadowsocksTestPassword(aliceKeyB),
				},
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, echoShadowsocksTCP(establishedConn, "alice-existing-after-rotation"))
	harness.requireTCPRejected(t, harness.new2022Client(t, aliceKeyA))

	rotatedConn, err := harness.openTCP(harness.new2022Client(t, aliceKeyB), "alice-after-rotation")
	require.NoError(t, err)
	harness.requireTCPUser(t, "alice")
	_ = rotatedConn.Close()

	_, err = managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
		ExpectedGeneration: managed.Generation(),
		Operations: []adapter.UserOperation[option.ShadowsocksUser]{
			{
				Type: adapter.UserOperationAdd,
				ID:   "bob",
				Value: option.ShadowsocksUser{
					Name:     "bob",
					Password: shadowsocksTestPassword(bobKey),
				},
			},
		},
	})
	require.NoError(t, err)

	swapResult, err := managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
		ExpectedGeneration: managed.Generation(),
		Operations: []adapter.UserOperation[option.ShadowsocksUser]{
			{
				Type: adapter.UserOperationUpdate,
				ID:   "alice",
				Value: option.ShadowsocksUser{
					Name:     "alice",
					Password: shadowsocksTestPassword(bobKey),
				},
			},
			{
				Type: adapter.UserOperationUpdate,
				ID:   "bob",
				Value: option.ShadowsocksUser{
					Name:     "bob",
					Password: shadowsocksTestPassword(aliceKeyB),
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []adapter.UserID{"alice", "bob"}, swapResult.Updated)

	aliceSwappedConn, err := harness.openTCP(harness.new2022Client(t, bobKey), "alice-after-swap")
	require.NoError(t, err)
	harness.requireTCPUser(t, "alice")
	_ = aliceSwappedConn.Close()
	bobSwappedConn, err := harness.openTCP(harness.new2022Client(t, aliceKeyB), "bob-after-swap")
	require.NoError(t, err)
	harness.requireTCPUser(t, "bob")
	_ = bobSwappedConn.Close()

	generationBeforeInvalid := managed.Generation()
	invalidPasswords := []string{
		"not-base64!",
		shadowsocksTestPassword(bytes.Repeat([]byte{0x15}, 15)),
	}
	for _, invalidPassword := range invalidPasswords {
		_, err = managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
			ExpectedGeneration: managed.Generation(),
			Operations: []adapter.UserOperation[option.ShadowsocksUser]{
				{
					Type: adapter.UserOperationUpdate,
					ID:   "alice",
					Value: option.ShadowsocksUser{
						Name:     "alice",
						Password: invalidPassword,
					},
				},
			},
		})
		require.ErrorIs(t, err, usermanager.ErrBackendPrepareFailure)
		require.Equal(t, generationBeforeInvalid, managed.Generation())
	}
	stillActiveConn, err := harness.openTCP(harness.new2022Client(t, bobKey), "alice-after-invalid-update")
	require.NoError(t, err)
	harness.requireTCPUser(t, "alice")
	_ = stillActiveConn.Close()

	_, err = managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
		ExpectedGeneration: managed.Generation(),
		Operations: []adapter.UserOperation[option.ShadowsocksUser]{
			{Type: adapter.UserOperationDelete, ID: "alice"},
		},
	})
	require.NoError(t, err)
	harness.requireTCPRejected(t, harness.new2022Client(t, bobKey))
	require.NoError(t, echoShadowsocksTCP(establishedConn, "alice-existing-after-delete"))
	_ = establishedConn.Close()

	replaceResult, err := managed.ReplaceUsers(harness.ctx, managed.Generation(), "", "", nil)
	require.NoError(t, err)
	require.Equal(t, []adapter.UserID{"bob"}, replaceResult.Deleted)
	harness.requireTCPRejected(t, harness.new2022Client(t, aliceKeyB))

	unmanagedAfterUpdates, err := harness.openTCP(harness.new2022Client(t, unmanagedKey), "unmanaged-after-delete-all")
	require.NoError(t, err)
	harness.requireTCPUser(t, "")
	_ = unmanagedAfterUpdates.Close()
}

func TestShadowsocksManagedUsersUDPSessionSurvivesDeletion(t *testing.T) {
	t.Parallel()

	iPSK := shadowsocksTestKey(0x20)
	aliceKey := shadowsocksTestKey(0x21)
	harness := newShadowsocksTestHarness(t, option.ShadowsocksInboundOptions{
		Method:   "2022-blake3-aes-128-gcm",
		Password: shadowsocksTestPassword(iPSK),
		Users: []option.ShadowsocksUser{
			{Name: "alice", Password: shadowsocksTestPassword(aliceKey)},
		},
	})
	managed := harness.inbound.(adapter.ManagedUserManager[option.ShadowsocksUser])

	udpConn, err := harness.openUDP(harness.new2022Client(t, aliceKey), "alice-udp-before-delete")
	require.NoError(t, err)
	harness.requireUDPUser(t, "alice")

	_, err = managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
		ExpectedGeneration: managed.Generation(),
		Operations: []adapter.UserOperation[option.ShadowsocksUser]{
			{Type: adapter.UserOperationDelete, ID: "alice"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, echoShadowsocksUDP(udpConn, "alice-udp-existing-after-delete"))
	_ = udpConn.Close()
	harness.requireUDPRejected(t, harness.new2022Client(t, aliceKey))
}

func TestShadowsocksManagedUsersConcurrentTCPAndUDPUpdates(t *testing.T) {
	t.Parallel()

	iPSK := shadowsocksTestKey(0x30)
	keyA := shadowsocksTestKey(0x31)
	keyB := shadowsocksTestKey(0x32)
	harness := newShadowsocksTestHarness(t, option.ShadowsocksInboundOptions{
		Method:   "2022-blake3-aes-128-gcm",
		Password: shadowsocksTestPassword(iPSK),
		Users: []option.ShadowsocksUser{
			{Name: "alice", Password: shadowsocksTestPassword(keyA)},
			{Name: "bob", Password: shadowsocksTestPassword(keyB)},
		},
	})
	managed := harness.inbound.(adapter.ManagedUserManager[option.ShadowsocksUser])
	clientA := harness.new2022Client(t, keyA)
	clientB := harness.new2022Client(t, keyB)

	start := make(chan struct{})
	errors := make(chan error, 1)
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		<-start
		for iteration := range 150 {
			aliceKey, bobKey := keyA, keyB
			if iteration%2 != 0 {
				aliceKey, bobKey = keyB, keyA
			}
			_, err := managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
				Operations: []adapter.UserOperation[option.ShadowsocksUser]{
					{
						Type: adapter.UserOperationUpdate,
						ID:   "alice",
						Value: option.ShadowsocksUser{
							Name:     "alice",
							Password: shadowsocksTestPassword(aliceKey),
						},
					},
					{
						Type: adapter.UserOperationUpdate,
						ID:   "bob",
						Value: option.ShadowsocksUser{
							Name:     "bob",
							Password: shadowsocksTestPassword(bobKey),
						},
					},
				},
			})
			if err != nil {
				select {
				case errors <- err:
				default:
				}
				return
			}
		}
	}()
	for worker := range 4 {
		group.Add(1)
		go func(useUDP bool, client ss.Method) {
			defer group.Done()
			<-start
			for iteration := range 40 {
				payload := fmt.Sprintf("race-%d", iteration)
				if useUDP {
					conn, err := harness.openUDP(client, payload)
					if err == nil {
						_ = conn.Close()
					}
					if err != nil {
						select {
						case errors <- err:
						default:
						}
						return
					}
					continue
				}
				conn, err := harness.openTCP(client, payload)
				if err == nil {
					_ = conn.Close()
				}
				if err != nil {
					select {
					case errors <- err:
					default:
					}
					return
				}
			}
		}(worker%2 != 0, []ss.Method{clientA, clientB}[worker%2])
	}
	close(start)
	group.Wait()
	close(errors)
	for raceErr := range errors {
		t.Fatal(raceErr)
	}

	for len(harness.router.tcpMetadata) > 0 {
		metadata := <-harness.router.tcpMetadata
		require.Contains(t, []string{"alice", "bob"}, metadata.User)
	}
	for len(harness.router.udpMetadata) > 0 {
		metadata := <-harness.router.udpMetadata
		require.Contains(t, []string{"alice", "bob"}, metadata.User)
	}
}

func TestShadowsocksManagedSSMUpdateUsersCompatibility(t *testing.T) {
	t.Parallel()

	iPSK := shadowsocksTestKey(0x40)
	userKey := shadowsocksTestKey(0x41)
	harness := newShadowsocksTestHarness(t, option.ShadowsocksInboundOptions{
		Method:   "2022-blake3-aes-128-gcm",
		Password: shadowsocksTestPassword(iPSK),
		Managed:  true,
	})
	managedSSM, loaded := harness.inbound.(adapter.ManagedSSMServer)
	require.True(t, loaded)
	require.NoError(t, managedSSM.UpdateUsers(
		[]string{"ssm-user"},
		[]string{shadowsocksTestPassword(userKey)},
	))

	conn, err := harness.openTCP(harness.new2022Client(t, userKey), "ssm-compatible-update")
	require.NoError(t, err)
	harness.requireTCPUser(t, "ssm-user")
	_ = conn.Close()
}

func TestShadowsocksLegacyMultiUserDecision(t *testing.T) {
	t.Parallel()

	router := newShadowsocksTestRouter()
	_, err := NewInbound(
		context.Background(),
		router,
		logger.NOP(),
		"legacy-rejection-test",
		option.ShadowsocksInboundOptions{
			Method: "aes-128-gcm",
			Users: []option.ShadowsocksUser{
				{Name: "alice", Password: "alice-password"},
				{Name: "bob", Password: "bob-password"},
			},
		},
	)
	require.EqualError(
		t,
		err,
		"legacy Shadowsocks multi-user is unsupported for method \"aes-128-gcm\"; configure a single user or migrate to 2022-blake3-aes-128-gcm or 2022-blake3-aes-256-gcm",
	)

	harness := newShadowsocksTestHarness(t, option.ShadowsocksInboundOptions{
		Method: "aes-128-gcm",
		Users: []option.ShadowsocksUser{
			{Name: "legacy", Password: "legacy-password"},
		},
	})
	_, managed := harness.inbound.(adapter.ManagedUserManager[option.ShadowsocksUser])
	require.False(t, managed)
	_, managedSSM := harness.inbound.(adapter.ManagedSSMServer)
	require.False(t, managedSSM)

	conn, err := harness.openTCP(harness.newLegacyClient(t, "legacy-password"), "legacy-single-user")
	require.NoError(t, err)
	harness.requireTCPUser(t, "legacy")
	_ = conn.Close()

	singleHarness := newShadowsocksTestHarness(t, option.ShadowsocksInboundOptions{
		Method:   "aes-128-gcm",
		Password: "top-level-legacy-password",
	})
	_, managed = singleHarness.inbound.(adapter.ManagedUserManager[option.ShadowsocksUser])
	require.False(t, managed)
	singleConn, err := singleHarness.openTCP(
		singleHarness.newLegacyClient(t, "top-level-legacy-password"),
		"top-level-legacy-single-user",
	)
	require.NoError(t, err)
	singleHarness.requireTCPUser(t, "")
	_ = singleConn.Close()
}
