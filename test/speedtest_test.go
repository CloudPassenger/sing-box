package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/netip"
	"testing"
	"time"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/common/speedtest"
	Xbadoption "github.com/sagernet/sing-box/common/xray/json/badoption"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/require"
)

const speedTestDataSize = 64 * 1024

// runSpeedTestAgainstOutbound drives one download and one upload private
// speedtest round through outbound, and asserts each finishes with the
// exact requested byte count.
func runSpeedTestAgainstOutbound(t *testing.T, dialer N.Dialer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	downloadConn, err := dialer.DialContext(ctx, N.NetworkTCP, M.Socksaddr{Fqdn: speedtest.MagicAddress})
	require.NoError(t, err)
	var downloadEnded bool
	var downloadTransferred uint32
	err = speedtest.DownloadTest(ctx, downloadConn, speedTestDataSize, func(duration time.Duration, transferred uint32, end bool) {
		if end {
			downloadEnded = true
			downloadTransferred = transferred
		}
	})
	require.NoError(t, err)
	require.True(t, downloadEnded)
	require.Equal(t, uint32(speedTestDataSize), downloadTransferred)

	uploadConn, err := dialer.DialContext(ctx, N.NetworkTCP, M.Socksaddr{Fqdn: speedtest.MagicAddress})
	require.NoError(t, err)
	var uploadEnded bool
	var uploadTransferred uint32
	err = speedtest.UploadTest(ctx, uploadConn, speedTestDataSize, func(duration time.Duration, transferred uint32, end bool) {
		if end {
			uploadEnded = true
			uploadTransferred = transferred
		}
	})
	require.NoError(t, err)
	require.True(t, uploadEnded)
	require.Equal(t, uint32(speedTestDataSize), uploadTransferred)
}

func runSpeedTestRejected(t *testing.T, dialer N.Dialer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialer.DialContext(ctx, N.NetworkTCP, M.Socksaddr{Fqdn: speedtest.MagicAddress})
	require.NoError(t, err)
	err = speedtest.DownloadTest(ctx, conn, speedTestDataSize, func(duration time.Duration, transferred uint32, end bool) {})
	require.Error(t, err)
	require.Contains(t, err.Error(), "Disallow")
}

func runSpeedTestDisabled(t *testing.T, dialer N.Dialer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// The core router rejects the magic destination before any regular
	// proxying, so depending on the protocol this surfaces either as a
	// dial-time handshake failure (e.g. SOCKS5 replies with a connect
	// rejection code) or as a later read failure once the connection is
	// established. Either way it must not be the protocol-level "Disallow"
	// produced by an explicit reject.
	conn, err := dialer.DialContext(ctx, N.NetworkTCP, M.Socksaddr{Fqdn: speedtest.MagicAddress})
	if err != nil {
		require.NotContains(t, err.Error(), "Disallow")
		return
	}
	err = speedtest.DownloadTest(ctx, conn, speedTestDataSize, func(duration time.Duration, transferred uint32, end bool) {})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "Disallow")
}

// mustOutbound looks up the outbound registered with tag on instance,
// bypassing box routing entirely so the private speedtest magic address
// reaches the outbound implementation directly.
func mustOutbound(t *testing.T, instance *box.Box, tag string) N.Dialer {
	t.Helper()
	out, loaded := instance.Outbound().Outbound(tag)
	require.True(t, loaded)
	return out
}

func TestPrivateSpeedTest(t *testing.T) {
	t.Run("anytls", testSpeedTestAnyTLS)
	t.Run("hysteria", testSpeedTestHysteria)
	t.Run("hysteria2", testSpeedTestHysteria2)
	t.Run("mixed", testSpeedTestMixed)
	t.Run("http", testSpeedTestHTTP)
	t.Run("trusttunnel", testSpeedTestTrustTunnel)
	t.Run("shadowsocks_single", testSpeedTestShadowsocksSingle)
	t.Run("shadowsocks_multi", testSpeedTestShadowsocksMulti)
	t.Run("socks", testSpeedTestSocks)
	t.Run("trojan", testSpeedTestTrojan)
	t.Run("tuic", testSpeedTestTUIC)
	t.Run("vless", testSpeedTestVLESS)
	t.Run("vless_encryption_xhttp", testSpeedTestVLESSEncryptionXHTTP)
	t.Run("vmess", testSpeedTestVMess)
	t.Run("reject", testSpeedTestReject)
	t.Run("disabled", testSpeedTestDisabled)
}

func testSpeedTestAnyTLS(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeAnyTLS,
				Tag:  "anytls-in",
				Options: &option.AnyTLSInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					Users: []option.AnyTLSUser{{Name: "sekai", Password: "password"}},
					InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{
						TLS: &option.InboundTLSOptions{
							Enabled:         true,
							ServerName:      "example.org",
							CertificatePath: certPem,
							KeyPath:         keyPem,
						},
					},
					InboundSpeedTestOptions: option.InboundSpeedTestOptions{SpeedTest: "allow"},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeAnyTLS,
				Tag:  "anytls-out",
				Options: &option.AnyTLSOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
					Password:      "password",
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
						TLS: &option.OutboundTLSOptions{Enabled: true, ServerName: "example.org", Insecure: true},
					},
				},
			},
		},
	})
	runSpeedTestAgainstOutbound(t, mustOutbound(t, instance, "anytls-out"))
}

func testSpeedTestHysteria(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeHysteria,
				Tag:  "hysteria-in",
				Options: &option.HysteriaInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					UpMbps:   100,
					DownMbps: 100,
					Users:    []option.HysteriaUser{{AuthString: "password"}},
					InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{
						TLS: &option.InboundTLSOptions{
							Enabled:         true,
							ServerName:      "example.org",
							CertificatePath: certPem,
							KeyPath:         keyPem,
						},
					},
					InboundSpeedTestOptions: option.InboundSpeedTestOptions{SpeedTest: "allow"},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeHysteria,
				Tag:  "hysteria-out",
				Options: &option.HysteriaOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
					UpMbps:        100,
					DownMbps:      100,
					AuthString:    "password",
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
						TLS: &option.OutboundTLSOptions{Enabled: true, ServerName: "example.org", CertificatePath: certPem},
					},
				},
			},
		},
	})
	runSpeedTestAgainstOutbound(t, mustOutbound(t, instance, "hysteria-out"))
}

func testSpeedTestHysteria2(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeHysteria2,
				Tag:  "hy2-in",
				Options: &option.Hysteria2InboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					Users: []option.Hysteria2User{{Password: "password"}},
					InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{
						TLS: &option.InboundTLSOptions{
							Enabled:         true,
							ServerName:      "example.org",
							CertificatePath: certPem,
							KeyPath:         keyPem,
						},
					},
					InboundSpeedTestOptions: option.InboundSpeedTestOptions{SpeedTest: "allow"},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeHysteria2,
				Tag:  "hy2-out",
				Options: &option.Hysteria2OutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
					Password:      "password",
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
						TLS: &option.OutboundTLSOptions{Enabled: true, ServerName: "example.org", CertificatePath: certPem},
					},
				},
			},
		},
	})
	runSpeedTestAgainstOutbound(t, mustOutbound(t, instance, "hy2-out"))
}

func testSpeedTestMixed(t *testing.T) {
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Tag:  "mixed-in",
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					InboundSpeedTestOptions: option.InboundSpeedTestOptions{SpeedTest: "allow"},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeSOCKS,
				Tag:  "mixed-out",
				Options: &option.SOCKSOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
				},
			},
		},
	})
	runSpeedTestAgainstOutbound(t, mustOutbound(t, instance, "mixed-out"))
}

func testSpeedTestHTTP(t *testing.T) {
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeHTTP,
				Tag:  "http-in",
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					InboundSpeedTestOptions: option.InboundSpeedTestOptions{SpeedTest: "allow"},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeHTTP,
				Tag:  "http-out",
				Options: &option.HTTPOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
				},
			},
		},
	})
	runSpeedTestAgainstOutbound(t, mustOutbound(t, instance, "http-out"))
}

func testSpeedTestTrustTunnel(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeTrustTunnel,
				Tag:  "trusttunnel-in",
				Options: &option.TrustTunnelInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					Users:   []auth.User{{Username: "sekai", Password: "password"}},
					Network: N.NetworkTCP,
					InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{
						TLS: &option.InboundTLSOptions{
							Enabled:         true,
							ServerName:      "example.org",
							ALPN:            []string{"h2"},
							CertificatePath: certPem,
							KeyPath:         keyPem,
						},
					},
					InboundSpeedTestOptions: option.InboundSpeedTestOptions{SpeedTest: "allow"},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeTrustTunnel,
				Tag:  "trusttunnel-out",
				Options: &option.TrustTunnelOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
					Username:      "sekai",
					Password:      "password",
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
						TLS: &option.OutboundTLSOptions{
							Enabled:         true,
							ServerName:      "example.org",
							ALPN:            []string{"h2"},
							CertificatePath: certPem,
						},
					},
				},
			},
		},
	})
	runSpeedTestAgainstOutbound(t, mustOutbound(t, instance, "trusttunnel-out"))
}

func testSpeedTestShadowsocksSingle(t *testing.T) {
	password := mkBase64(t, 16)
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeShadowsocks,
				Tag:  "ss-in",
				Options: &option.ShadowsocksInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					Method:                  "2022-blake3-aes-128-gcm",
					Password:                password,
					InboundSpeedTestOptions: option.InboundSpeedTestOptions{SpeedTest: "allow"},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeShadowsocks,
				Tag:  "ss-out",
				Options: &option.ShadowsocksOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
					Method:        "2022-blake3-aes-128-gcm",
					Password:      password,
				},
			},
		},
	})
	runSpeedTestAgainstOutbound(t, mustOutbound(t, instance, "ss-out"))
}

func testSpeedTestShadowsocksMulti(t *testing.T) {
	serverPassword := mkBase64(t, 16)
	userPassword := mkBase64(t, 16)
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeShadowsocks,
				Tag:  "ss-in",
				Options: &option.ShadowsocksInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					Method:   "2022-blake3-aes-128-gcm",
					Password: serverPassword,
					Users: []option.ShadowsocksUser{
						{Name: "sekai", Password: userPassword},
					},
					InboundSpeedTestOptions: option.InboundSpeedTestOptions{SpeedTest: "allow"},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeShadowsocks,
				Tag:  "ss-out",
				Options: &option.ShadowsocksOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
					Method:        "2022-blake3-aes-128-gcm",
					Password:      serverPassword + ":" + userPassword,
				},
			},
		},
	})
	runSpeedTestAgainstOutbound(t, mustOutbound(t, instance, "ss-out"))
}

func testSpeedTestSocks(t *testing.T) {
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeSOCKS,
				Tag:  "socks-in",
				Options: &option.SocksInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					InboundSpeedTestOptions: option.InboundSpeedTestOptions{SpeedTest: "allow"},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeSOCKS,
				Tag:  "socks-out",
				Options: &option.SOCKSOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
				},
			},
		},
	})
	runSpeedTestAgainstOutbound(t, mustOutbound(t, instance, "socks-out"))
}

func testSpeedTestTrojan(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeTrojan,
				Tag:  "trojan-in",
				Options: &option.TrojanInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					Users: []option.TrojanUser{{Password: "password"}},
					InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{
						TLS: &option.InboundTLSOptions{
							Enabled:         true,
							ServerName:      "example.org",
							CertificatePath: certPem,
							KeyPath:         keyPem,
						},
					},
					InboundSpeedTestOptions: option.InboundSpeedTestOptions{SpeedTest: "allow"},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeTrojan,
				Tag:  "trojan-out",
				Options: &option.TrojanOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
					Password:      "password",
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
						TLS: &option.OutboundTLSOptions{Enabled: true, ServerName: "example.org", CertificatePath: certPem},
					},
				},
			},
		},
	})
	runSpeedTestAgainstOutbound(t, mustOutbound(t, instance, "trojan-out"))
}

func testSpeedTestTUIC(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeTUIC,
				Tag:  "tuic-in",
				Options: &option.TUICInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					Users: []option.TUICUser{{UUID: uuid.Nil.String()}},
					InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{
						TLS: &option.InboundTLSOptions{
							Enabled:         true,
							ServerName:      "example.org",
							CertificatePath: certPem,
							KeyPath:         keyPem,
						},
					},
					InboundSpeedTestOptions: option.InboundSpeedTestOptions{SpeedTest: "allow"},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeTUIC,
				Tag:  "tuic-out",
				Options: &option.TUICOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
					UUID:          uuid.Nil.String(),
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
						TLS: &option.OutboundTLSOptions{Enabled: true, ServerName: "example.org", CertificatePath: certPem},
					},
				},
			},
		},
	})
	runSpeedTestAgainstOutbound(t, mustOutbound(t, instance, "tuic-out"))
}

func testSpeedTestVLESS(t *testing.T) {
	user, _ := uuid.NewV4()
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeVLESS,
				Tag:  "vless-in",
				Options: &option.VLESSInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					Users:                   []option.VLESSUser{{UUID: user.String()}},
					InboundSpeedTestOptions: option.InboundSpeedTestOptions{SpeedTest: "allow"},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeVLESS,
				Tag:  "vless-out",
				Options: &option.VLESSOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
					UUID:          user.String(),
				},
			},
		},
	})
	runSpeedTestAgainstOutbound(t, mustOutbound(t, instance, "vless-out"))
}

// testSpeedTestVLESSEncryptionXHTTP proves the private speedtest router is
// installed regardless of VLESS Encryption and XHTTP transport, the two
// superpower-only VLESS extensions.
func testSpeedTestVLESSEncryptionXHTTP(t *testing.T) {
	user, _ := uuid.NewV4()
	serverKey, clientKey := generateVlessX25519KeyPairForTest(t)
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeVLESS,
				Tag:  "vless-enc-in",
				Options: &option.VLESSInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					Users:      []option.VLESSUser{{UUID: user.String()}},
					Decryption: "mlkem768x25519plus.native.600s." + serverKey,
					Transport: &option.V2RayTransportOptions{
						Type: C.V2RayTransportTypeXHTTP,
						XHTTPOptions: option.V2RayXHTTPOptions{
							Mode: "stream-one",
							V2RayXHTTPBaseOptions: option.V2RayXHTTPBaseOptions{
								Path:          "/xhttp",
								XPaddingBytes: Xbadoption.Range{From: 64, To: 256},
							},
						},
					},
					InboundSpeedTestOptions: option.InboundSpeedTestOptions{SpeedTest: "allow"},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeVLESS,
				Tag:  "vless-enc-out",
				Options: &option.VLESSOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
					UUID:          user.String(),
					Encryption:    "mlkem768x25519plus.native.0rtt." + clientKey,
					Transport: &option.V2RayTransportOptions{
						Type: C.V2RayTransportTypeXHTTP,
						XHTTPOptions: option.V2RayXHTTPOptions{
							Mode: "stream-one",
							V2RayXHTTPBaseOptions: option.V2RayXHTTPBaseOptions{
								Path:          "/xhttp",
								XPaddingBytes: Xbadoption.Range{From: 64, To: 256},
							},
						},
					},
				},
			},
		},
	})
	runSpeedTestAgainstOutbound(t, mustOutbound(t, instance, "vless-enc-out"))
}

func testSpeedTestVMess(t *testing.T) {
	user, _ := uuid.NewV4()
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeVMess,
				Tag:  "vmess-in",
				Options: &option.VMessInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					Users:                   []option.VMessUser{{UUID: user.String()}},
					InboundSpeedTestOptions: option.InboundSpeedTestOptions{SpeedTest: "allow"},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeVMess,
				Tag:  "vmess-out",
				Options: &option.VMessOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
					UUID:          user.String(),
					Security:      "auto",
				},
			},
		},
	})
	runSpeedTestAgainstOutbound(t, mustOutbound(t, instance, "vmess-out"))
}

func testSpeedTestReject(t *testing.T) {
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeSOCKS,
				Tag:  "socks-in",
				Options: &option.SocksInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					InboundSpeedTestOptions: option.InboundSpeedTestOptions{SpeedTest: "reject"},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeSOCKS,
				Tag:  "socks-out",
				Options: &option.SOCKSOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
				},
			},
		},
	})
	runSpeedTestRejected(t, mustOutbound(t, instance, "socks-out"))
}

func testSpeedTestDisabled(t *testing.T) {
	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeSOCKS,
				Tag:  "socks-in",
				Options: &option.SocksInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeSOCKS,
				Tag:  "socks-out",
				Options: &option.SOCKSOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: serverPort},
				},
			},
		},
	})
	runSpeedTestDisabled(t, mustOutbound(t, instance, "socks-out"))
}

// generateVlessX25519KeyPairForTest mirrors cmd_generate_vless.go's
// generateVlessX25519 to build a matching decryption/encryption pair
// in-process for the test module.
func generateVlessX25519KeyPairForTest(t *testing.T) (serverKey, clientKey string) {
	t.Helper()
	privateKey := make([]byte, 32)
	_, err := rand.Read(privateKey)
	require.NoError(t, err)
	privateKey[0] &= 248
	privateKey[31] &= 127
	privateKey[31] |= 64
	x25519PrivateKey, err := ecdh.X25519().NewPrivateKey(privateKey)
	require.NoError(t, err)
	publicKey := x25519PrivateKey.PublicKey().Bytes()
	serverKey = base64.RawURLEncoding.EncodeToString(privateKey)
	clientKey = base64.RawURLEncoding.EncodeToString(publicKey)
	return
}
