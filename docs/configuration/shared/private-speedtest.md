---
icon: material/new-box
---

!!! quote "Since sing-box 1.14.0"

!!! warning ""

    It's a private protocol invited by Hysteria 2, not part of the official Hysteria 2 speedtest implementation.

The private speedtest protocol lets an already authenticated client measure download and upload speed against the
server directly, without proxying to an external destination.

### Structure

```json
{
  "speed_test": "allow"
}
```

`speed_test` is a field on the inbound object itself, not a nested object.

### Fields

#### speed_test

Controls how the inbound handles private speedtest requests.

Available values:

* `disable` (default, or omitted): the inbound does not serve speedtest requests; a request is rejected by the core
  router.
* `allow`: the inbound serves speedtest requests locally.
* `reject`: the inbound replies with a protocol-level rejection instead of serving the test.

### Supported inbounds

| Inbound             | Notes                                          |
|----------------------|-------------------------------------------------|
| [AnyTLS](/configuration/inbound/anytls/)         |                                                  |
| [Hysteria](/configuration/inbound/hysteria/)     |                                                  |
| [Hysteria2](/configuration/inbound/hysteria2/)   |                                                  |
| [Mixed](/configuration/inbound/mixed/)           | Only reachable via its SOCKS listener; HTTP CONNECT to the same inbound cannot carry the magic destination |
| [Naive](/configuration/inbound/naive/)           |                                                  |
| [Shadowsocks](/configuration/inbound/shadowsocks/) | Not supported when `destinations` is configured |
| [SOCKS](/configuration/inbound/socks/)           |                                                  |
| [Trojan](/configuration/inbound/trojan/)         |                                                  |
| [TUIC](/configuration/inbound/tuic/)             |                                                  |
| [VLESS](/configuration/inbound/vless/)           | Including VLESS Encryption and XHTTP transport  |
| [VMess](/configuration/inbound/vmess/)           |                                                  |

Direct, Tun, Redirect, TProxy, HTTP, TrustTunnel, and ShadowTLS do not accept an arbitrary client-chosen destination
through a channel compatible with the magic FQDN and therefore cannot receive speedtest requests. HTTP and
TrustTunnel are excluded because both tunnel through an HTTP/1.1 or HTTP/2 CONNECT request whose target host becomes
`@SpeedTest`; the `@` character is not a valid `Host`/`:authority` byte (RFC 7230, RFC 9113), so the request is
rejected or its host is stripped before the magic destination can be recognized. A ShadowTLS detour's inner inbound
(e.g. VMess or Trojan) can enable `speed_test` itself.

### Client usage

```shell
sing-box -c config.json tools speedtest --outbound proxy --data-size 67108864
```

| Flag              | Description                                                     |
|--------------------|-------------------------------------------------------------------|
| `--outbound, -o`  | Outbound tag to test through, default outbound if omitted.        |
| `--skip-upload`   | Skip the upload test.                                              |
| `--skip-download` | Skip the download test.                                           |
| `--use-bytes`     | Report decimal bytes per second instead of decimal bits per second. |
| `--quiet`         | Suppress progress output.                                          |
| `--data-size`     | Data size for download and upload tests, in bytes.                 |
| `--timeout`       | Limit duration for each direction.                                 |

### Protocol details

The client connects to the destination `@SpeedTest` on any supported outbound; the server intercepts it before the
connection reaches routing.

#### Request format

| Type (byte)              | Data length (u32be) |
|---------------------------|----------------------|
| `0x01` download / `0x02` upload | requested length |

#### Response format

| Status (byte)                 | Message length (u16be) | Message  |
|--------------------------------|--------------------------|----------|
| `0x00` OK / `0x01` error       | length of message        | variable |

#### Upload summary format

Sent by the server after it has fully received an upload test's data, with no status prefix.

| Duration (u32be, milliseconds) | Received length (u32be) |
|----------------------------------|----------------------------|
