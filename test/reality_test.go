package main

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/require"
)

func TestReality(t *testing.T) {
	user, _ := uuid.NewV4()
	startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Tag:  "mixed-in",
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: clientPort,
					},
				},
			},
			{
				Type: C.TypeVLESS,
				Options: &option.VLESSInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					Users: []option.VLESSUser{{UUID: user.String()}},
					InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{
						TLS: &option.InboundTLSOptions{
							Enabled:    true,
							ServerName: "google.com",
							Reality: &option.InboundRealityOptions{
								Enabled: true,
								Handshake: option.InboundRealityHandshakeOptions{
									ServerOptions: option.ServerOptions{
										Server:     "google.com",
										ServerPort: 443,
									},
								},
								ShortID:    []string{"0123456789abcdef"},
								PrivateKey: "UuMBgl7MXTPx9inmQp2UC7Jcnwc6XYbwDNebonM-FCc",
							},
						},
					},
				},
			},
		},
		Outbounds: []option.Outbound{
			{
				Type: C.TypeDirect,
			},
			{
				Type: C.TypeVLESS,
				Tag:  "ss-out",
				Options: &option.VLESSOutboundOptions{
					ServerOptions: option.ServerOptions{
						Server:     "127.0.0.1",
						ServerPort: serverPort,
					},
					UUID: user.String(),
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
						TLS: &option.OutboundTLSOptions{
							Enabled:    true,
							ServerName: "google.com",
							Reality: &option.OutboundRealityOptions{
								Enabled:   true,
								ShortID:   "0123456789abcdef",
								PublicKey: "jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0",
							},
							UTLS: &option.OutboundUTLSOptions{
								Enabled: true,
							},
						},
					},
				},
			},
		},
		Route: &option.RouteOptions{
			Rules: []option.Rule{
				{
					Type: C.RuleTypeDefault,
					DefaultOptions: option.DefaultRule{
						RawDefaultRule: option.RawDefaultRule{
							Inbound: []string{"mixed-in"},
						},
						RuleAction: option.RuleAction{
							Action: C.RuleActionTypeRoute,

							RouteOptions: option.RouteActionOptions{
								Outbound: "ss-out",
							},
						},
					},
				},
			},
		},
	})
	testSuit(t, clientPort, testPort)
}

func TestRealityFallbackLimit(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "localhost")
	payload := make([]byte, 384*1024)
	for i := range payload {
		payload[i] = byte(i)
	}

	backendListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	backendPort := uint16(backendListener.Addr().(*net.TCPAddr).Port)
	backendServer := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Length", F.ToString(len(payload)))
		_, _ = writer.Write(payload)
	})}
	go func() {
		_ = backendServer.ServeTLS(backendListener, certPem, keyPem)
	}()
	t.Cleanup(func() {
		_ = backendServer.Close()
	})

	user, _ := uuid.NewV4()
	startInstance(t, option.Options{
		Inbounds: []option.Inbound{{
			Type: C.TypeVLESS,
			Options: &option.VLESSInboundOptions{
				ListenOptions: option.ListenOptions{
					Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
					ListenPort: serverPort,
				},
				Users: []option.VLESSUser{{UUID: user.String()}},
				InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{
					TLS: &option.InboundTLSOptions{
						Enabled:    true,
						ServerName: "localhost",
						Reality: &option.InboundRealityOptions{
							Enabled: true,
							Handshake: option.InboundRealityHandshakeOptions{
								ServerOptions: option.ServerOptions{
									Server:     "127.0.0.1",
									ServerPort: backendPort,
								},
							},
							ShortID:    []string{"0123456789abcdef"},
							PrivateKey: "UuMBgl7MXTPx9inmQp2UC7Jcnwc6XYbwDNebonM-FCc",
							FallbackLimit: &option.InboundRealityFallbackLimit{
								SamplingPeriod: badoption.Duration(time.Second),
								DownMbps:       common.Ptr(1.0),
								DownBurstMbps:  common.Ptr(1.0),
								UpMbps:         common.Ptr(16.0),
								UpBurstMbps:    common.Ptr(16.0),
							},
						},
					},
				},
			},
		}},
		Outbounds: []option.Outbound{{Type: C.TypeDirect}},
	})

	conn, err := tls.Dial("tcp", "127.0.0.1:"+F.ToString(serverPort), &tls.Config{
		ServerName:         "localhost",
		InsecureSkipVerify: true,
	})
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"))
	require.NoError(t, err)

	start := time.Now()
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, payload, body)
	require.GreaterOrEqual(t, time.Since(start), 1500*time.Millisecond)
}
