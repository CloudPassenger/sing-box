package trojan

import (
	"context"
	"encoding/binary"
	"net"
	"sync/atomic"

	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/rw"
)

type Handler interface {
	N.TCPConnectionHandlerEx
	N.UDPConnectionHandlerEx
}

type preparedUser[K comparable] struct {
	user K
	key  [KeyLength]byte
}

// PreparedUsers is one immutable Trojan authentication state.
type PreparedUsers[K comparable] struct {
	users []preparedUser[K]
	keys  map[[KeyLength]byte]K
}

type Service[K comparable] struct {
	state           atomic.Pointer[PreparedUsers[K]]
	handler         Handler
	fallbackHandler N.TCPConnectionHandlerEx
	logger          logger.ContextLogger
}

func NewService[K comparable](handler Handler, fallbackHandler N.TCPConnectionHandlerEx, logger logger.ContextLogger) *Service[K] {
	service := &Service[K]{
		handler:         handler,
		fallbackHandler: fallbackHandler,
		logger:          logger,
	}
	service.state.Store(&PreparedUsers[K]{
		keys: make(map[[KeyLength]byte]K),
	})
	return service
}

var ErrUserExists = E.New("user already exists")

func (s *Service[K]) UpdateUsers(userList []K, passwordList []string) error {
	if len(userList) != len(passwordList) {
		return E.New("user and password count mismatch")
	}
	users := make(map[K]struct{}, len(userList))
	for _, user := range userList {
		if _, loaded := users[user]; loaded {
			return ErrUserExists
		}
		users[user] = struct{}{}
	}
	prepared, err := s.PrepareUsers(userList, passwordList)
	if err != nil {
		return err
	}
	s.InstallUsers(prepared)
	return nil
}

// PrepareUsers validates and compiles one complete immutable authentication state.
func (s *Service[K]) PrepareUsers(userList []K, passwordList []string) (*PreparedUsers[K], error) {
	if len(userList) != len(passwordList) {
		return nil, E.New("user and password count mismatch")
	}
	users := make([]preparedUser[K], 0, len(userList))
	keys := make(map[[KeyLength]byte]K, len(userList))
	for index, user := range userList {
		key := Key(passwordList[index])
		if _, loaded := keys[key]; loaded {
			return nil, E.Extend(ErrUserExists, "password is assigned to multiple users")
		}
		users = append(users, preparedUser[K]{
			user: user,
			key:  key,
		})
		keys[key] = user
	}
	return &PreparedUsers[K]{
		users: users,
		keys:  keys,
	}, nil
}

// InstallUsers atomically publishes a state returned by PrepareUsers.
func (s *Service[K]) InstallUsers(users *PreparedUsers[K]) {
	s.state.Store(users)
}

func (s *Service[K]) NewConnection(ctx context.Context, conn net.Conn, source M.Socksaddr, onClose N.CloseHandlerFunc) error {
	var key [KeyLength]byte
	n, err := conn.Read(key[:])
	if err != nil {
		return err
	} else if n != KeyLength {
		return s.fallback(ctx, conn, source, key[:n], E.New("bad request size"), onClose)
	}

	state := s.state.Load()
	if user, loaded := state.keys[key]; loaded {
		ctx = auth.ContextWithUser(ctx, user)
	} else {
		return s.fallback(ctx, conn, source, key[:], E.New("bad request"), onClose)
	}

	err = rw.SkipN(conn, 2)
	if err != nil {
		return E.Cause(err, "skip crlf")
	}

	var command byte
	err = binary.Read(conn, binary.BigEndian, &command)
	if err != nil {
		return E.Cause(err, "read command")
	}

	switch command {
	case CommandTCP, CommandUDP, CommandMux:
	default:
		return E.New("unknown command ", command)
	}

	// var destination M.Socksaddr
	destination, err := M.SocksaddrSerializer.ReadAddrPort(conn)
	if err != nil {
		return E.Cause(err, "read destination")
	}

	err = rw.SkipN(conn, 2)
	if err != nil {
		return E.Cause(err, "skip crlf")
	}

	switch command {
	case CommandTCP:
		s.handler.NewConnectionEx(ctx, conn, source, destination, onClose)
	case CommandUDP:
		s.handler.NewPacketConnectionEx(ctx, &PacketConn{Conn: conn}, source, destination, onClose)
	// case CommandMux:
	default:
		return HandleMuxConnection(ctx, conn, source, s.handler, s.logger, onClose)
	}
	return nil
}

func (s *Service[K]) fallback(ctx context.Context, conn net.Conn, source M.Socksaddr, header []byte, err error, onClose N.CloseHandlerFunc) error {
	if s.fallbackHandler == nil {
		return E.Extend(err, "fallback disabled")
	}
	conn = bufio.NewCachedConn(conn, buf.As(header).ToOwned())
	s.fallbackHandler.NewConnectionEx(ctx, conn, source, M.Socksaddr{}, onClose)
	return nil
}

type PacketConn struct {
	net.Conn
	readWaitOptions N.ReadWaitOptions
}

func (c *PacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	return ReadPacket(c.Conn, buffer)
}

func (c *PacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	return WritePacket(c.Conn, buffer, destination)
}

func (c *PacketConn) FrontHeadroom() int {
	return M.MaxSocksaddrLength + 4
}

func (c *PacketConn) NeedAdditionalReadDeadline() bool {
	return true
}

func (c *PacketConn) Upstream() any {
	return c.Conn
}
