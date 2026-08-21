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

func requireShadowsocksUDPExchangeRejected(t *testing.T, conn N.PacketConn, payload string) {
	t.Helper()
	require.NoError(t, conn.SetDeadline(time.Now().Add(500*time.Millisecond)))

	packet := buf.NewPacket()
	_, err := packet.Write([]byte(payload))
	require.NoError(t, err)
	_, err = SBufio.WritePacketBuffer(conn, packet, M.ParseSocksaddr("example.com:53"))
	require.NoError(t, err)
	response := buf.NewPacket()
	defer response.Release()
	_, err = conn.ReadPacket(response)
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

type shadowsocksManagedTestProfile struct {
	name   string
	method string
	legacy bool
}

type shadowsocksManagedTestCredential struct {
	password string
	key      []byte
}

type shadowsocksUserIdentityBackend interface {
	StableID(user option.ShadowsocksUser) (adapter.UserID, error)
	FingerprintUser(user option.ShadowsocksUser) uint64
}

func shadowsocksManagedTestProfiles() []shadowsocksManagedTestProfile {
	return []shadowsocksManagedTestProfile{
		{name: "2022 AES-128-GCM", method: "2022-blake3-aes-128-gcm"},
		{name: "legacy AES-128-GCM", method: "aes-128-gcm", legacy: true},
		{name: "legacy ChaCha20-IETF-Poly1305", method: "chacha20-ietf-poly1305", legacy: true},
	}
}

func shadowsocksLegacyManagedTestProfiles() []shadowsocksManagedTestProfile {
	return shadowsocksManagedTestProfiles()[1:]
}

func (p shadowsocksManagedTestProfile) credential(value byte) shadowsocksManagedTestCredential {
	if p.legacy {
		return shadowsocksManagedTestCredential{
			password: fmt.Sprintf("legacy-password-%02x", value),
		}
	}
	key := shadowsocksTestKey(value)
	return shadowsocksManagedTestCredential{
		password: shadowsocksTestPassword(key),
		key:      key,
	}
}

func (p shadowsocksManagedTestProfile) options(
	users []option.ShadowsocksUser,
	managed bool,
) option.ShadowsocksInboundOptions {
	options := option.ShadowsocksInboundOptions{
		Method:  p.method,
		Managed: managed,
		Users:   users,
	}
	if !p.legacy {
		options.Password = shadowsocksTestPassword(shadowsocksTestKey(0x7f))
	}
	return options
}

func (p shadowsocksManagedTestProfile) newClient(
	t *testing.T,
	harness *shadowsocksTestHarness,
	credential shadowsocksManagedTestCredential,
) ss.Method {
	t.Helper()
	if p.legacy {
		return harness.newLegacyClient(t, credential.password)
	}
	return harness.new2022Client(t, credential.key)
}

func TestShadowsocksUserBackendIdentityAndFingerprint(t *testing.T) {
	t.Parallel()
	backends := []struct {
		name    string
		backend shadowsocksUserIdentityBackend
	}{
		{name: "2022", backend: newShadowsocks2022UserBackend(nil, nil)},
		{name: "legacy", backend: newLegacyShadowsocksUserBackend(nil, nil)},
	}
	for _, test := range backends {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stableID, err := test.backend.StableID(option.ShadowsocksUser{Name: "alice", Password: "password"})
			require.NoError(t, err)
			require.Equal(t, adapter.UserID("alice"), stableID)
			_, err = test.backend.StableID(option.ShadowsocksUser{Password: "password"})
			require.ErrorContains(t, err, "empty Shadowsocks user name")

			fingerprint := test.backend.FingerprintUser(option.ShadowsocksUser{Name: "alice", Password: "password"})
			require.NotEqual(t, fingerprint, test.backend.FingerprintUser(option.ShadowsocksUser{Name: "bob", Password: "password"}))
			require.NotEqual(t, fingerprint, test.backend.FingerprintUser(option.ShadowsocksUser{Name: "alice", Password: "rotated"}))
		})
	}
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
	for _, profile := range shadowsocksManagedTestProfiles() {
		t.Run(profile.name, func(t *testing.T) {
			t.Parallel()
			baseCredential := profile.credential(0x11)
			aliceCredentialA := profile.credential(0x12)
			aliceCredentialB := profile.credential(0x13)
			bobCredential := profile.credential(0x14)
			idCollisionCredential := profile.credential(0x15)
			harness := newShadowsocksTestHarness(t, profile.options([]option.ShadowsocksUser{
				{Password: baseCredential.password},
				{Name: "alice", Password: aliceCredentialA.password},
			}, false))
			managed, loaded := harness.inbound.(adapter.ManagedUserManager[option.ShadowsocksUser])
			require.True(t, loaded)
			_, managedSSM := harness.inbound.(adapter.ManagedSSMServer)
			require.True(t, managedSSM)
			require.Equal(t, adapter.UserGeneration(1), managed.Generation())

			baseConn, err := harness.openTCP(profile.newClient(t, harness, baseCredential), "base-before-updates")
			require.NoError(t, err)
			harness.requireTCPUser(t, "")
			_ = baseConn.Close()
			baseUDPConn, err := harness.openUDP(profile.newClient(t, harness, baseCredential), "base-udp-before-updates")
			require.NoError(t, err)
			harness.requireUDPUser(t, "")
			_ = baseUDPConn.Close()

			establishedConn, err := harness.openTCP(
				profile.newClient(t, harness, aliceCredentialA),
				"alice-before-rotation",
			)
			require.NoError(t, err)
			harness.requireTCPUser(t, "alice")
			aliceUDPConn, err := harness.openUDP(
				profile.newClient(t, harness, aliceCredentialA),
				"alice-udp-before-rotation",
			)
			require.NoError(t, err)
			harness.requireUDPUser(t, "alice")
			_ = aliceUDPConn.Close()

			duplicateID := shadowsocksStaticUserID(0)
			_, err = managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
				ExpectedGeneration: managed.Generation(),
				Operations: []adapter.UserOperation[option.ShadowsocksUser]{
					{
						Type: adapter.UserOperationAdd,
						ID:   duplicateID,
						Value: option.ShadowsocksUser{
							Name:     string(duplicateID),
							Password: idCollisionCredential.password,
						},
					},
				},
			})
			require.ErrorIs(t, err, usermanager.ErrBackendPrepareFailure)
			require.Equal(t, adapter.UserGeneration(1), managed.Generation())

			_, err = managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
				ExpectedGeneration: managed.Generation(),
				Operations: []adapter.UserOperation[option.ShadowsocksUser]{
					{
						Type: adapter.UserOperationAdd,
						ID:   "base-collision",
						Value: option.ShadowsocksUser{
							Name:     "base-collision",
							Password: baseCredential.password,
						},
					},
				},
			})
			require.ErrorIs(t, err, usermanager.ErrBackendPrepareFailure)
			require.Equal(t, adapter.UserGeneration(1), managed.Generation())

			_, err = managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
				ExpectedGeneration: managed.Generation(),
				Operations: []adapter.UserOperation[option.ShadowsocksUser]{
					{
						Type: adapter.UserOperationAdd,
						ID:   "bob",
						Value: option.ShadowsocksUser{
							Name:     "bob",
							Password: aliceCredentialA.password,
						},
					},
				},
			})
			require.ErrorIs(t, err, usermanager.ErrBackendPrepareFailure)
			require.Equal(t, adapter.UserGeneration(1), managed.Generation())

			_, err = managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
				ExpectedGeneration: managed.Generation(),
				Operations: []adapter.UserOperation[option.ShadowsocksUser]{
					{
						Type: adapter.UserOperationUpdate,
						ID:   "alice",
						Value: option.ShadowsocksUser{
							Name:     "alice",
							Password: aliceCredentialB.password,
						},
					},
				},
			})
			require.NoError(t, err)
			require.NoError(t, echoShadowsocksTCP(establishedConn, "alice-existing-after-rotation"))
			harness.requireTCPRejected(t, profile.newClient(t, harness, aliceCredentialA))

			rotatedConn, err := harness.openTCP(
				profile.newClient(t, harness, aliceCredentialB),
				"alice-after-rotation",
			)
			require.NoError(t, err)
			harness.requireTCPUser(t, "alice")
			_ = rotatedConn.Close()

			addResult, err := managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
				ExpectedGeneration: managed.Generation(),
				Operations: []adapter.UserOperation[option.ShadowsocksUser]{
					{
						Type: adapter.UserOperationAdd,
						ID:   "bob",
						Value: option.ShadowsocksUser{
							Name:     "bob",
							Password: bobCredential.password,
						},
					},
				},
			})
			require.NoError(t, err)
			require.Equal(t, []adapter.UserID{"bob"}, addResult.Added)

			swapResult, err := managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
				ExpectedGeneration: managed.Generation(),
				Operations: []adapter.UserOperation[option.ShadowsocksUser]{
					{
						Type: adapter.UserOperationUpdate,
						ID:   "alice",
						Value: option.ShadowsocksUser{
							Name:     "alice",
							Password: bobCredential.password,
						},
					},
					{
						Type: adapter.UserOperationUpdate,
						ID:   "bob",
						Value: option.ShadowsocksUser{
							Name:     "bob",
							Password: aliceCredentialB.password,
						},
					},
				},
			})
			require.NoError(t, err)
			require.Equal(t, []adapter.UserID{"alice", "bob"}, swapResult.Updated)

			aliceSwappedConn, err := harness.openTCP(
				profile.newClient(t, harness, bobCredential),
				"alice-after-swap",
			)
			require.NoError(t, err)
			harness.requireTCPUser(t, "alice")
			_ = aliceSwappedConn.Close()
			bobSwappedConn, err := harness.openTCP(
				profile.newClient(t, harness, aliceCredentialB),
				"bob-after-swap",
			)
			require.NoError(t, err)
			harness.requireTCPUser(t, "bob")
			_ = bobSwappedConn.Close()

			generationBeforeInvalid := managed.Generation()
			invalidPasswords := []string{""}
			if !profile.legacy {
				invalidPasswords = append(
					invalidPasswords,
					"not-base64!",
					shadowsocksTestPassword(bytes.Repeat([]byte{0x16}, 15)),
				)
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
			stillActiveConn, err := harness.openTCP(
				profile.newClient(t, harness, bobCredential),
				"alice-after-invalid-update",
			)
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
			harness.requireTCPRejected(t, profile.newClient(t, harness, bobCredential))
			require.NoError(t, echoShadowsocksTCP(establishedConn, "alice-existing-after-delete"))
			_ = establishedConn.Close()

			replaceResult, err := managed.ReplaceUsers(harness.ctx, managed.Generation(), "", "", nil)
			require.NoError(t, err)
			require.Equal(t, []adapter.UserID{"bob"}, replaceResult.Deleted)
			harness.requireTCPRejected(t, profile.newClient(t, harness, aliceCredentialB))

			baseAfterUpdates, err := harness.openTCP(
				profile.newClient(t, harness, baseCredential),
				"base-after-delete-all",
			)
			require.NoError(t, err)
			harness.requireTCPUser(t, "")
			_ = baseAfterUpdates.Close()
			baseUDPAfterUpdates, err := harness.openUDP(
				profile.newClient(t, harness, baseCredential),
				"base-udp-after-delete-all",
			)
			require.NoError(t, err)
			harness.requireUDPUser(t, "")
			_ = baseUDPAfterUpdates.Close()
		})
	}
}

func TestShadowsocksManagedUsersUDPSessionDeletionBehavior(t *testing.T) {
	t.Parallel()
	for _, profile := range shadowsocksManagedTestProfiles() {
		t.Run(profile.name, func(t *testing.T) {
			t.Parallel()
			baseCredential := profile.credential(0x20)
			aliceCredential := profile.credential(0x21)
			harness := newShadowsocksTestHarness(t, profile.options([]option.ShadowsocksUser{
				{Password: baseCredential.password},
				{Name: "alice", Password: aliceCredential.password},
			}, false))
			managed := harness.inbound.(adapter.ManagedUserManager[option.ShadowsocksUser])

			udpConn, err := harness.openUDP(
				profile.newClient(t, harness, aliceCredential),
				"alice-udp-before-delete",
			)
			require.NoError(t, err)
			harness.requireUDPUser(t, "alice")

			_, err = managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
				ExpectedGeneration: managed.Generation(),
				Operations: []adapter.UserOperation[option.ShadowsocksUser]{
					{Type: adapter.UserOperationDelete, ID: "alice"},
				},
			})
			require.NoError(t, err)
			requireShadowsocksUDPExchangeRejected(t, udpConn, "alice-udp-existing-after-delete")
			_ = udpConn.Close()
			harness.requireUDPRejected(t, profile.newClient(t, harness, aliceCredential))

			baseConn, err := harness.openUDP(
				profile.newClient(t, harness, baseCredential),
				"base-udp-after-managed-delete",
			)
			require.NoError(t, err)
			harness.requireUDPUser(t, "")
			_ = baseConn.Close()
		})
	}
}

func TestShadowsocksManagedUsersConcurrentTCPAndUDPUpdates(t *testing.T) {
	t.Parallel()
	for _, profile := range shadowsocksManagedTestProfiles() {
		t.Run(profile.name, func(t *testing.T) {
			t.Parallel()
			credentialA := profile.credential(0x31)
			credentialB := profile.credential(0x32)
			harness := newShadowsocksTestHarness(t, profile.options([]option.ShadowsocksUser{
				{Name: "alice", Password: credentialA.password},
				{Name: "bob", Password: credentialB.password},
			}, false))
			managed := harness.inbound.(adapter.ManagedUserManager[option.ShadowsocksUser])
			clientA := profile.newClient(t, harness, credentialA)
			clientB := profile.newClient(t, harness, credentialB)

			start := make(chan struct{})
			errors := make(chan error, 1)
			var group sync.WaitGroup
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				for iteration := range 150 {
					aliceCredential, bobCredential := credentialA, credentialB
					if iteration%2 != 0 {
						aliceCredential, bobCredential = credentialB, credentialA
					}
					_, err := managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
						Operations: []adapter.UserOperation[option.ShadowsocksUser]{
							{
								Type: adapter.UserOperationUpdate,
								ID:   "alice",
								Value: option.ShadowsocksUser{
									Name:     "alice",
									Password: aliceCredential.password,
								},
							},
							{
								Type: adapter.UserOperationUpdate,
								ID:   "bob",
								Value: option.ShadowsocksUser{
									Name:     "bob",
									Password: bobCredential.password,
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
				}(worker >= 2, []ss.Method{clientA, clientB}[worker%2])
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
		})
	}
}

func TestShadowsocksManagedSSMUpdateUsersCompatibility(t *testing.T) {
	t.Parallel()
	for _, profile := range shadowsocksManagedTestProfiles() {
		t.Run(profile.name, func(t *testing.T) {
			t.Parallel()
			credential := profile.credential(0x41)
			harness := newShadowsocksTestHarness(t, profile.options(nil, true))
			_, multiInbound := harness.inbound.(*MultiInbound)
			require.True(t, multiInbound)
			_, managedUsers := harness.inbound.(adapter.ManagedUserManager[option.ShadowsocksUser])
			require.True(t, managedUsers)
			managedSSM, loaded := harness.inbound.(adapter.ManagedSSMServer)
			require.True(t, loaded)
			require.NoError(t, managedSSM.UpdateUsers(
				[]string{"ssm-user"},
				[]string{credential.password},
			))

			conn, err := harness.openTCP(
				profile.newClient(t, harness, credential),
				"ssm-compatible-update",
			)
			require.NoError(t, err)
			harness.requireTCPUser(t, "ssm-user")
			_ = conn.Close()
			udpConn, err := harness.openUDP(
				profile.newClient(t, harness, credential),
				"ssm-compatible-udp-update",
			)
			require.NoError(t, err)
			harness.requireUDPUser(t, "ssm-user")
			_ = udpConn.Close()
		})
	}
}

func TestShadowsocksManagedAndSSMNamespacesShareCanonicalState(t *testing.T) {
	t.Parallel()
	for _, profile := range shadowsocksManagedTestProfiles() {
		t.Run(profile.name, func(t *testing.T) {
			t.Parallel()
			apiOld := profile.credential(0x61)
			apiNew := profile.credential(0x62)
			ssmOld := profile.credential(0x63)
			ssmNew := profile.credential(0x64)
			harness := newShadowsocksTestHarness(t, profile.options([]option.ShadowsocksUser{
				{Name: "api-user", Password: apiOld.password},
			}, false))
			managed, loaded := harness.inbound.(adapter.ManagedUserManager[option.ShadowsocksUser])
			require.True(t, loaded)
			managedSSM, loaded := harness.inbound.(adapter.ManagedSSMServer)
			require.True(t, loaded)

			generationBeforeReservedID := managed.Generation()
			reservedID := adapter.UserID(shadowsocksSSMUserIDPrefix + "forged")
			_, err := managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
				ExpectedGeneration: generationBeforeReservedID,
				Operations: []adapter.UserOperation[option.ShadowsocksUser]{
					{
						Type: adapter.UserOperationAdd,
						ID:   reservedID,
						Value: option.ShadowsocksUser{
							Name:     string(reservedID),
							Password: profile.credential(0x65).password,
						},
					},
				},
			})
			require.ErrorIs(t, err, usermanager.ErrInvalidTransaction)
			require.Equal(t, generationBeforeReservedID, managed.Generation())

			require.NoError(t, managedSSM.UpdateUsers(
				[]string{"ssm-user"},
				[]string{ssmOld.password},
			))
			apiConn, err := harness.openTCP(profile.newClient(t, harness, apiOld), "api-after-ssm-add")
			require.NoError(t, err)
			harness.requireTCPUser(t, "api-user")
			_ = apiConn.Close()
			ssmConn, err := harness.openTCP(profile.newClient(t, harness, ssmOld), "ssm-after-add")
			require.NoError(t, err)
			harness.requireTCPUser(t, "ssm-user")
			_ = ssmConn.Close()

			require.NoError(t, managedSSM.UpdateUsers(
				[]string{"ssm-user"},
				[]string{ssmNew.password},
			))
			harness.requireTCPRejected(t, profile.newClient(t, harness, ssmOld))
			apiConn, err = harness.openTCP(profile.newClient(t, harness, apiOld), "api-after-ssm-rotation")
			require.NoError(t, err)
			harness.requireTCPUser(t, "api-user")
			_ = apiConn.Close()

			_, err = managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
				ExpectedGeneration: managed.Generation(),
				Operations: []adapter.UserOperation[option.ShadowsocksUser]{
					{
						Type: adapter.UserOperationUpdate,
						ID:   "api-user",
						Value: option.ShadowsocksUser{
							Name:     "api-user",
							Password: apiNew.password,
						},
					},
				},
			})
			require.NoError(t, err)
			harness.requireTCPRejected(t, profile.newClient(t, harness, apiOld))
			ssmConn, err = harness.openTCP(profile.newClient(t, harness, ssmNew), "ssm-after-api-rotation")
			require.NoError(t, err)
			harness.requireTCPUser(t, "ssm-user")
			_ = ssmConn.Close()

			require.NoError(t, managedSSM.UpdateUsers(nil, nil))
			harness.requireTCPRejected(t, profile.newClient(t, harness, ssmNew))
			apiConn, err = harness.openTCP(profile.newClient(t, harness, apiNew), "api-after-ssm-delete")
			require.NoError(t, err)
			harness.requireTCPUser(t, "api-user")
			_ = apiConn.Close()
		})
	}
}

func TestShadowsocksManagedAndSSMConcurrentUpdatesPreserveNamespaces(t *testing.T) {
	t.Parallel()
	for _, profile := range shadowsocksManagedTestProfiles() {
		t.Run(profile.name, func(t *testing.T) {
			t.Parallel()
			apiOld := profile.credential(0x71)
			apiNew := profile.credential(0x72)
			ssmCredential := profile.credential(0x73)
			harness := newShadowsocksTestHarness(t, profile.options([]option.ShadowsocksUser{
				{Name: "api-user", Password: apiOld.password},
			}, false))
			managed, loaded := harness.inbound.(adapter.ManagedUserManager[option.ShadowsocksUser])
			require.True(t, loaded)
			managedSSM, loaded := harness.inbound.(adapter.ManagedSSMServer)
			require.True(t, loaded)

			start := make(chan struct{})
			errors := make(chan error, 2)
			var group sync.WaitGroup
			group.Add(2)
			go func() {
				defer group.Done()
				<-start
				_, err := managed.ApplyUsers(harness.ctx, adapter.UserTransaction[option.ShadowsocksUser]{
					Operations: []adapter.UserOperation[option.ShadowsocksUser]{
						{
							Type: adapter.UserOperationUpdate,
							ID:   "api-user",
							Value: option.ShadowsocksUser{
								Name:     "api-user",
								Password: apiNew.password,
							},
						},
					},
				})
				errors <- err
			}()
			go func() {
				defer group.Done()
				<-start
				errors <- managedSSM.UpdateUsers(
					[]string{"ssm-user"},
					[]string{ssmCredential.password},
				)
			}()
			close(start)
			group.Wait()
			close(errors)
			for err := range errors {
				require.NoError(t, err)
			}
			require.Equal(t, adapter.UserGeneration(3), managed.Generation())

			apiConn, err := harness.openTCP(profile.newClient(t, harness, apiNew), "api-after-concurrent-update")
			require.NoError(t, err)
			harness.requireTCPUser(t, "api-user")
			_ = apiConn.Close()
			ssmConn, err := harness.openTCP(profile.newClient(t, harness, ssmCredential), "ssm-after-concurrent-update")
			require.NoError(t, err)
			harness.requireTCPUser(t, "ssm-user")
			_ = ssmConn.Close()
		})
	}
}

func TestShadowsocksLegacyMultiUserDecision(t *testing.T) {
	t.Parallel()
	for _, profile := range shadowsocksLegacyManagedTestProfiles() {
		t.Run(profile.name, func(t *testing.T) {
			t.Parallel()
			t.Run("multi-user managed capability", func(t *testing.T) {
				t.Parallel()
				baseCredential := profile.credential(0x51)
				aliceCredential := profile.credential(0x52)
				harness := newShadowsocksTestHarness(t, profile.options([]option.ShadowsocksUser{
					{Password: baseCredential.password},
					{Name: "alice", Password: aliceCredential.password},
				}, false))
				_, multiInbound := harness.inbound.(*MultiInbound)
				require.True(t, multiInbound)
				_, managed := harness.inbound.(adapter.ManagedUserManager[option.ShadowsocksUser])
				require.True(t, managed)
				_, managedSSM := harness.inbound.(adapter.ManagedSSMServer)
				require.True(t, managedSSM)

				aliceConn, err := harness.openTCP(
					profile.newClient(t, harness, aliceCredential),
					"legacy-managed-alice",
				)
				require.NoError(t, err)
				harness.requireTCPUser(t, "alice")
				_ = aliceConn.Close()
				aliceUDPConn, err := harness.openUDP(
					profile.newClient(t, harness, aliceCredential),
					"legacy-managed-alice-udp",
				)
				require.NoError(t, err)
				harness.requireUDPUser(t, "alice")
				_ = aliceUDPConn.Close()

				baseConn, err := harness.openTCP(
					profile.newClient(t, harness, baseCredential),
					"legacy-managed-static-base",
				)
				require.NoError(t, err)
				harness.requireTCPUser(t, "")
				_ = baseConn.Close()
				baseUDPConn, err := harness.openUDP(
					profile.newClient(t, harness, baseCredential),
					"legacy-managed-static-base-udp",
				)
				require.NoError(t, err)
				harness.requireUDPUser(t, "")
				_ = baseUDPConn.Close()
			})

			t.Run("single named user exposes managed capability", func(t *testing.T) {
				t.Parallel()
				credential := profile.credential(0x53)
				harness := newShadowsocksTestHarness(t, profile.options([]option.ShadowsocksUser{
					{Name: "legacy", Password: credential.password},
				}, false))
				_, multiInbound := harness.inbound.(*MultiInbound)
				require.True(t, multiInbound)
				managed, loaded := harness.inbound.(adapter.ManagedUserManager[option.ShadowsocksUser])
				require.True(t, loaded)
				require.Equal(t, adapter.UserGeneration(1), managed.Generation())
				_, managedSSM := harness.inbound.(adapter.ManagedSSMServer)
				require.True(t, managedSSM)

				conn, err := harness.openTCP(
					profile.newClient(t, harness, credential),
					"legacy-single-user",
				)
				require.NoError(t, err)
				harness.requireTCPUser(t, "legacy")
				_ = conn.Close()
				udpConn, err := harness.openUDP(
					profile.newClient(t, harness, credential),
					"legacy-single-user-udp",
				)
				require.NoError(t, err)
				harness.requireUDPUser(t, "legacy")
				_ = udpConn.Close()
			})

			t.Run("top-level password remains non-managed", func(t *testing.T) {
				t.Parallel()
				credential := profile.credential(0x54)
				harness := newShadowsocksTestHarness(t, option.ShadowsocksInboundOptions{
					Method:   profile.method,
					Password: credential.password,
				})
				_, singleInbound := harness.inbound.(*Inbound)
				require.True(t, singleInbound)
				_, managed := harness.inbound.(adapter.ManagedUserManager[option.ShadowsocksUser])
				require.False(t, managed)
				_, managedSSM := harness.inbound.(adapter.ManagedSSMServer)
				require.False(t, managedSSM)

				conn, err := harness.openTCP(
					profile.newClient(t, harness, credential),
					"top-level-legacy-single-user",
				)
				require.NoError(t, err)
				harness.requireTCPUser(t, "")
				_ = conn.Close()
				udpConn, err := harness.openUDP(
					profile.newClient(t, harness, credential),
					"top-level-legacy-single-user-udp",
				)
				require.NoError(t, err)
				harness.requireUDPUser(t, "")
				_ = udpConn.Close()
			})
		})
	}
}
