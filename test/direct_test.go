package main

import (
	"context"
	"net"
	"net/netip"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/protocol/socks"

	"github.com/stretchr/testify/require"
)

func TestProxyProtocol(t *testing.T) {
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
				Type: C.TypeDirect,
				Options: &option.DirectInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:        common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort:    serverPort,
						ProxyProtocol: true,
					},
					OverrideAddress: "127.0.0.1",
					OverridePort:    testPort,
				},
			},
		},
		Outbounds: []option.Outbound{
			{
				Type: C.TypeDirect,
			},
			{
				Type: C.TypeDirect,
				Tag:  "proxy-out",
				Options: &option.DirectOutboundOptions{
					DialerOptions: option.DialerOptions{
						ProxyProtocol: 2,
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
								Outbound: "proxy-out",
								RawRouteOptionsActionOptions: option.RawRouteOptionsActionOptions{
									OverrideAddress: "127.0.0.1",
									OverridePort:    serverPort,
								},
							},
						},
					},
				},
			},
		},
	})
	testTCP(t, clientPort, testPort)
}

func TestProxyProtocolAcceptNoHeader(t *testing.T) {
	startProxyProtocolDirectInstance(t, false, true)
	testTCP(t, clientPort, testPort)
}

func TestProxyProtocolRejectNoHeader(t *testing.T) {
	startProxyProtocolDirectInstance(t, false, false)
	dialer := socks.NewClient(N.SystemDialer, M.ParseSocksaddrHostPort("127.0.0.1", clientPort), socks.Version5, "", "")
	err := testPingPongWithConn(t, testPort, func() (net.Conn, error) {
		return dialer.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddrHostPort("127.0.0.1", testPort))
	})
	require.Error(t, err)
}

func startProxyProtocolDirectInstance(t *testing.T, sendProxyProtocol bool, acceptNoHeader bool) {
	proxyProtocol := option.ProxyProtocolVersion(0)
	if sendProxyProtocol {
		proxyProtocol = 2
	}
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
				Type: C.TypeDirect,
				Options: &option.DirectInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:                      common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort:                  serverPort,
						ProxyProtocol:               true,
						ProxyProtocolAcceptNoHeader: acceptNoHeader,
					},
					OverrideAddress: "127.0.0.1",
					OverridePort:    testPort,
				},
			},
		},
		Outbounds: []option.Outbound{
			{
				Type: C.TypeDirect,
			},
			{
				Type: C.TypeDirect,
				Tag:  "proxy-out",
				Options: &option.DirectOutboundOptions{
					DialerOptions: option.DialerOptions{
						ProxyProtocol: proxyProtocol,
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
								Outbound: "proxy-out",
								RawRouteOptionsActionOptions: option.RawRouteOptionsActionOptions{
									OverrideAddress: "127.0.0.1",
									OverridePort:    serverPort,
								},
							},
						},
					},
				},
			},
		},
	})
}
