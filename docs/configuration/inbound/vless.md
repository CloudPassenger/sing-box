### Structure

```json
{
  "type": "vless",
  "tag": "vless-in",

  ... // Listen Fields

  "users": [
    {
      "name": "sekai",
      "uuid": "bf000d23-0752-40b4-affe-68f7707a9661",
      "flow": ""
    }
  ],
  "tls": {},
  "multiplex": {},
  "transport": {},
  "decryption": ""
}
```

### Listen Fields

See [Listen Fields](/configuration/shared/listen/) for details.

### Fields

#### users

==Required==

VLESS users.

#### users.uuid

==Required==

VLESS user id.

#### users.flow

VLESS Sub-protocol.

Available values:

* `xtls-rprx-vision`

#### decryption

VLESS encryption decryption key.

Use `sing-box generate vless-enc` to generate a key pair.

The key format is `mlkem768x25519plus.native.<ttl>.<private_key>`.

| Algorithm    | Command                          |
|--------------|----------------------------------|
| X25519       | `sing-box generate vless-enc`    |
| ML-KEM-768   | `sing-box generate vless-enc -m` |

!!! note ""

    Set environment variable `SING_VMESS_ENCRYPTION_DISABLE_AES=1` to disable AES.

#### tls

TLS configuration, see [TLS](/configuration/shared/tls/#inbound).

#### multiplex

See [Multiplex](/configuration/shared/multiplex#inbound) for details.

#### transport

V2Ray Transport configuration, see [V2Ray Transport](/configuration/shared/v2ray-transport/).
