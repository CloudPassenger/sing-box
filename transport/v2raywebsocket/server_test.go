package v2raywebsocket

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/ws"

	"github.com/stretchr/testify/require"
)

type hostConstraintTestHandler struct {
	connections chan net.Conn
}

func (h *hostConstraintTestHandler) NewConnectionEx(
	_ context.Context,
	conn net.Conn,
	_ M.Socksaddr,
	_ M.Socksaddr,
	_ N.CloseHandlerFunc,
) {
	h.connections <- conn
}

func TestServerRequestHostConstraint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		configuredHost string
		requestHost    string
		wantAccepted   bool
	}{
		{
			name:           "matching host normalizes case trailing dot and port",
			configuredHost: "Front.Example.",
			requestHost:    "FRONT.EXAMPLE:443",
			wantAccepted:   true,
		},
		{
			name:           "matching IPv6 host strips brackets and port",
			configuredHost: "[2001:DB8::1]",
			requestHost:    "[2001:db8::1]:443",
			wantAccepted:   true,
		},
		{
			name:           "different host is rejected before transport handler",
			configuredHost: "front.example",
			requestHost:    "other.example",
			wantAccepted:   false,
		},
		{
			name:           "non-empty root host does not disable validation",
			configuredHost: ".",
			requestHost:    "other.example",
			wantAccepted:   false,
		},
		{
			name:           "empty host remains unrestricted",
			configuredHost: "",
			requestHost:    "unrestricted.example:8443",
			wantAccepted:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := &hostConstraintTestHandler{connections: make(chan net.Conn, 1)}
			server, err := NewServer(
				context.Background(),
				logger.NOP(),
				option.V2RayWebsocketOptions{RequestHost: test.configuredHost, Path: "/ws"},
				nil,
				handler,
			)
			require.NoError(t, err)

			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			serveResult := make(chan error, 1)
			go func() {
				serveResult <- server.Serve(listener)
			}()
			t.Cleanup(func() {
				require.NoError(t, server.Close())
				select {
				case err := <-serveResult:
					require.True(t, errors.Is(err, http.ErrServerClosed), "Serve() error = %v", err)
				case <-time.After(time.Second):
					t.Error("WebSocket server did not stop")
				}
			})

			headers := badoption.HTTPHeader{
				"Host": badoption.Listable[string]{test.requestHost},
			}
			client, err := NewClient(
				context.Background(),
				N.SystemDialer,
				M.SocksaddrFromNet(listener.Addr()).Unwrap(),
				option.V2RayWebsocketOptions{Path: "/ws", Headers: headers},
				nil,
			)
			require.NoError(t, err)

			dialContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			connection, err := client.DialContext(dialContext)
			if !test.wantAccepted {
				require.Error(t, err)
				var statusError ws.StatusError
				require.ErrorAs(t, err, &statusError)
				require.Equal(t, ws.StatusError(http.StatusBadRequest), statusError)
				select {
				case accepted := <-handler.connections:
					_ = accepted.Close()
					t.Fatal("rejected Host reached the transport handler")
				default:
				}
				return
			}

			require.NoError(t, err)
			t.Cleanup(func() { _ = connection.Close() })
			select {
			case accepted := <-handler.connections:
				_ = accepted.Close()
			case <-time.After(5 * time.Second):
				t.Fatal("accepted WebSocket handshake did not reach the transport handler")
			}
		})
	}
}
