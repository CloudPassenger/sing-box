### 结构

```json
{
  "type": "vless",
  "tag": "vless-in",

  ... // 监听字段

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
  "decryption": "",
  "speed_test": "allow"
}
```

### 监听字段

参阅 [监听字段](/zh/configuration/shared/listen/)。

### 字段

#### users

==必填==

VLESS 用户。

#### users.uuid

==必填==

VLESS 用户 ID。

#### users.flow

VLESS 子协议。

可用值：

* `xtls-rprx-vision`

#### decryption

VLESS 加密解密密钥。

使用 `sing-box generate vless-enc` 生成密钥对。

密钥格式为 `mlkem768x25519plus.native.<ttl>.<私钥>`。

| 算法         | 命令                             |
|--------------|----------------------------------|
| X25519       | `sing-box generate vless-enc`    |
| ML-KEM-768   | `sing-box generate vless-enc -m` |

!!! note ""

    设置环境变量 `SING_VMESS_ENCRYPTION_DISABLE_AES=1` 以禁用 AES。

#### tls

TLS 配置, 参阅 [TLS](/zh/configuration/shared/tls/#入站)。

#### multiplex

参阅 [多路复用](/zh/configuration/shared/multiplex#入站)。

#### transport

V2Ray 传输配置，参阅 [V2Ray 传输层](/zh/configuration/shared/v2ray-transport/)。

#### speed_test

参阅 [私有测速](/zh/configuration/shared/private-speedtest/)。

同时覆盖 VLESS Encryption 与 XHTTP 传输层。
