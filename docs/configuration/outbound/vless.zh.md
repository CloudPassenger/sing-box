### 结构

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

  ... // 拨号字段
}
```

### 字段

#### server

==必填==

服务器地址。

#### server_port

==必填==

服务器端口。

#### uuid

==必填==

VLESS 用户 ID。

#### flow

VLESS 子协议。

可用值：

* `xtls-rprx-vision`

#### network

启用的网络协议。

`tcp` 或 `udp`。

默认所有。

#### tls

TLS 配置, 参阅 [TLS](/zh/configuration/shared/tls/#出站)。

#### packet_encoding

UDP 包编码，默认使用 xudp。

| 编码         | 描述            |
|------------|---------------|
| (空)        | 禁用            |
| packetaddr | 由 v2ray 5+ 支持 |
| xudp       | 由 xray 支持     |

#### multiplex

参阅 [多路复用](/zh/configuration/shared/multiplex#出站)。

#### transport

V2Ray 传输配置，参阅 [V2Ray 传输层](/zh/configuration/shared/v2ray-transport/)。

#### encryption

VLESS 加密密钥。

使用 `sing-box generate vless-enc` 生成密钥对。

密钥格式为 `mlkem768x25519plus.native.<模式>.<公钥>`。

| 算法         | 命令                             |
|--------------|----------------------------------|
| X25519       | `sing-box generate vless-enc`    |
| ML-KEM-768   | `sing-box generate vless-enc -m` |

!!! note ""

    设置环境变量 `SING_VMESS_ENCRYPTION_DISABLE_AES=1` 以禁用 AES。

### 拨号字段

参阅 [拨号字段](/zh/configuration/shared/dial/)。
