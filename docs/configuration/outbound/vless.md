### Structure

```json
{
  "type": "vless",
  "tag": "vless-out",
  
  "server": "127.0.0.1",
  "server_port": 1080,
  "uuid": "bf000d23-0752-40b4-affe-68f7707a9661",
  "flow": "xtls-rprx-vision",
  "network": "tcp",
  "tls": {},
  "packet_encoding": "",
  "multiplex": {},
  "transport": {},
  "encryption": "",

  ... // Dial Fields
}
```

### Fields

#### server

==Required==

The server address.

#### server_port

==Required==

The server port.

#### uuid

==Required==

VLESS user id.

#### flow

VLESS Sub-protocol.

Available values:

* `xtls-rprx-vision`

#### network

Enabled network

One of `tcp` `udp`.

Both is enabled by default.

#### tls

TLS configuration, see [TLS](/configuration/shared/tls/#outbound).

#### packet_encoding

UDP packet encoding, xudp is used by default.

| Encoding   | Description           |
|------------|-----------------------|
| (none)     | Disabled              |
| packetaddr | Supported by v2ray 5+ |
| xudp       | Supported by xray     |

#### multiplex

See [Multiplex](/configuration/shared/multiplex#outbound) for details.

#### transport

V2Ray Transport configuration, see [V2Ray Transport](/configuration/shared/v2ray-transport/).

#### encryption

VLESS encryption key.

Use `sing-box generate vless-enc` to generate a key pair.

The key format is `mlkem768x25519plus.native.<mode>.<public_key>`.

| Algorithm    | Command                          |
|--------------|----------------------------------|
| X25519       | `sing-box generate vless-enc`    |
| ML-KEM-768   | `sing-box generate vless-enc -m` |

!!! note ""

    Set environment variable `SING_VMESS_ENCRYPTION_DISABLE_AES=1` to disable AES.

### Dial Fields

See [Dial Fields](/configuration/shared/dial/) for details.
