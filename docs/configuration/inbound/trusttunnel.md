---
icon: material/new-box
---

!!! question "Since sing-box 1.14.0"

### Structure

```json
{
  "type": "trusttunnel",
  "tag": "trusttunnel-in",

  ... // Listen Fields

  "users": [
    {
      "username": "trust",
      "password": "tunnel"
    }
  ],
  "quic_congestion_control": "bbr",
  "network": ["tcp", "udp"],
  "tls": {},
  "speed_test": "allow"
}
```

### Listen Fields

See [Listen Fields](/configuration/shared/listen/) for details.

### Fields

#### users

==Required==

TrustTunnel user list.

#### users.username

==Required==

TrustTunnel username.

#### users.password

==Required==

TrustTunnel user password.

#### quic_congestion_control

QUIC congestion control algorithm.

| Algorithm | Description |
|-----------|-------------|
| `bbr` | BBR |
| `bbr_standard` | BBR (Standard version) |
| `bbr2` | BBRv2 |
| `bbr_variant` | BBRv2 (An experimental variant) |
| `cubic` | CUBIC |
| `reno` | New Reno |

`bbr` is used by default.

#### network

Network list.

Available values:

- `tcp` (HTTP/2)
- `udp` (HTTP/3)

Both are enabled by default.

When `udp` is enabled, `tls` must be enabled.

#### speed_test

See [Private Speed Test](/configuration/shared/private-speedtest/) for details.

#### tls

Inbound TLS configuration, see [TLS](/configuration/shared/tls/#inbound).

