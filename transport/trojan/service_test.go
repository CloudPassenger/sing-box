package trojan

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

type serviceTestHandler struct {
	users chan string
}

func (h *serviceTestHandler) NewConnectionEx(
	ctx context.Context,
	conn net.Conn,
	source M.Socksaddr,
	destination M.Socksaddr,
	onClose N.CloseHandlerFunc,
) {
	user, _ := auth.UserFromContext[string](ctx)
	h.users <- user
	if onClose != nil {
		onClose(nil)
	}
}

func (h *serviceTestHandler) NewPacketConnectionEx(
	ctx context.Context,
	conn N.PacketConn,
	source M.Socksaddr,
	destination M.Socksaddr,
	onClose N.CloseHandlerFunc,
) {
	user, _ := auth.UserFromContext[string](ctx)
	h.users <- user
	if onClose != nil {
		onClose(nil)
	}
}

func authenticateTrojanService(service *Service[string], password string) error {
	serverConn, clientConn := net.Pipe()
	done := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		done <- service.NewConnection(context.Background(), serverConn, M.Socksaddr{}, nil)
	}()

	handshakeErr := ClientHandshake(clientConn, Key(password), M.Socksaddr{
		Fqdn: "example.com",
		Port: 443,
	}, nil)
	_ = clientConn.Close()
	serviceErr := <-done
	if handshakeErr != nil {
		return handshakeErr
	}
	return serviceErr
}

func TestServicePreparedUsersAtomicPublication(t *testing.T) {
	t.Parallel()

	handler := &serviceTestHandler{
		users: make(chan string, 8),
	}
	service := NewService[string](handler, nil, logger.NOP())
	require.NoError(t, service.UpdateUsers(
		[]string{"alice"},
		[]string{"alice-password"},
	))
	initialState := service.state.Load()

	prepared, err := service.PrepareUsers(
		[]string{"bob"},
		[]string{"bob-password"},
	)
	require.NoError(t, err)
	require.Same(t, initialState, service.state.Load())
	require.NotSame(t, initialState, prepared)
	require.Len(t, prepared.users, 1)
	require.Equal(t, "bob", prepared.users[0].user)
	require.Equal(t, Key("bob-password"), prepared.users[0].key)
	require.Equal(t, "bob", prepared.keys[Key("bob-password")])

	require.NoError(t, authenticateTrojanService(service, "alice-password"))
	require.Equal(t, "alice", <-handler.users)
	require.Error(t, authenticateTrojanService(service, "bob-password"))

	service.InstallUsers(prepared)
	require.Same(t, prepared, service.state.Load())
	require.Error(t, authenticateTrojanService(service, "alice-password"))
	require.NoError(t, authenticateTrojanService(service, "bob-password"))
	require.Equal(t, "bob", <-handler.users)

	unnamed, err := service.PrepareUsers(
		[]string{"", ""},
		[]string{"legacy-one", "legacy-two"},
	)
	require.NoError(t, err)
	require.Len(t, unnamed.users, 2)

	_, err = service.PrepareUsers(
		[]string{"alice", "bob"},
		[]string{"do-not-leak", "do-not-leak"},
	)
	require.ErrorIs(t, err, ErrUserExists)
	require.NotContains(t, err.Error(), "do-not-leak")
	require.Same(t, prepared, service.state.Load())

	_, err = service.PrepareUsers([]string{"alice"}, nil)
	require.Error(t, err)
	require.NoError(t, service.UpdateUsers(
		[]string{"carol"},
		[]string{"carol-password"},
	))
	require.ErrorIs(t, service.UpdateUsers(
		[]string{"carol", "carol"},
		[]string{"carol-password", "other-password"},
	), ErrUserExists)
}

func TestServiceConcurrentAuthenticationAndUpdate(t *testing.T) {
	t.Parallel()

	const (
		readerCount = 4
		iterations  = 128
		authCount   = readerCount * iterations
	)

	handler := &serviceTestHandler{
		users: make(chan string, authCount),
	}
	service := NewService[string](handler, nil, logger.NOP())
	require.NoError(t, service.UpdateUsers(
		[]string{"alice", "bob"},
		[]string{"password-a", "password-b"},
	))

	start := make(chan struct{})
	errors := make(chan error, authCount+1)
	var group sync.WaitGroup
	group.Go(func() {
		<-start
		for index := range iterations {
			var err error
			if index%2 == 0 {
				err = service.UpdateUsers(
					[]string{"bob", "alice"},
					[]string{"password-a", "password-b"},
				)
			} else {
				err = service.UpdateUsers(
					[]string{"alice", "bob"},
					[]string{"password-a", "password-b"},
				)
			}
			if err != nil {
				errors <- err
				return
			}
		}
	})
	for reader := range readerCount {
		group.Go(func() {
			<-start
			for index := range iterations {
				password := "password-a"
				if (reader+index)%2 != 0 {
					password = "password-b"
				}
				if err := authenticateTrojanService(service, password); err != nil {
					errors <- err
					return
				}
			}
		})
	}
	close(start)
	group.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}

	require.Len(t, handler.users, authCount)
	close(handler.users)
	for user := range handler.users {
		require.Contains(t, []string{"alice", "bob"}, user)
	}
}
