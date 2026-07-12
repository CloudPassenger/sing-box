---
icon: material/new-box
---

!!! quote "自 sing-box 1.14.0 起"

!!! warning ""

    这是 Hysteria 2 发起的私有协议，不是官方 Hysteria 2 测速实现的一部分。

私有测速协议允许已通过认证的客户端直接对服务器测量下载和上传速度，而无需代理到外部目标。

### 结构

```json
{
  "speed_test": "allow"
}
```

`speed_test` 是入站对象自身的字段，不是嵌套对象。

### 字段

#### speed_test

控制入站如何处理私有测速请求。

可选值：

* `disable`（默认，或省略）：入站不处理测速请求；请求会被核心路由拒绝。
* `allow`：入站在本地处理测速请求。
* `reject`：入站返回协议级拒绝，而不处理测速。

### 支持的入站

| 入站                   | 说明                          |
|------------------------|-------------------------------|
| [AnyTLS](/zh/configuration/inbound/anytls/)         |                                |
| [Hysteria](/zh/configuration/inbound/hysteria/)     |                                |
| [Hysteria2](/zh/configuration/inbound/hysteria2/)   |                                |
| [Mixed](/zh/configuration/inbound/mixed/)           | 仅能通过其 SOCKS 监听端触发；对同一入站的 HTTP CONNECT 无法携带该魔法目标 |
| [Naive](/zh/configuration/inbound/naive/)           |                                |
| [Shadowsocks](/zh/configuration/inbound/shadowsocks/) | 配置 `destinations` 时不支持 |
| [SOCKS](/zh/configuration/inbound/socks/)           |                                |
| [Trojan](/zh/configuration/inbound/trojan/)         |                                |
| [TUIC](/zh/configuration/inbound/tuic/)             |                                |
| [VLESS](/zh/configuration/inbound/vless/)           | 包含 VLESS Encryption 与 XHTTP 传输层 |
| [VMess](/zh/configuration/inbound/vmess/)           |                                |

Direct、Tun、Redirect、TProxy、HTTP、TrustTunnel 和 ShadowTLS 都无法以与该魔法 FQDN 兼容的方式接受客户端任意选择的目标，
因此无法接收测速请求。HTTP 与 TrustTunnel 被排除是因为它们都通过 HTTP/1.1 或 HTTP/2 CONNECT 隧道，目标主机会变成
`@SpeedTest`；`@` 字符不是合法的 `Host`/`:authority` 字节（RFC 7230、RFC 9113），请求会在识别出魔法目标之前被拒绝或
清空主机字段。ShadowTLS 分流后的内层入站（例如 VMess 或 Trojan）可以自行启用 `speed_test`。

### 客户端用法

```shell
sing-box -c config.json tools speedtest --outbound proxy --data-size 67108864
```

| 参数                | 描述                                            |
|---------------------|-------------------------------------------------|
| `--outbound, -o`   | 用于测试的出站标签，省略时使用默认出站。         |
| `--skip-upload`    | 跳过上传测试。                                   |
| `--skip-download`  | 跳过下载测试。                                   |
| `--use-bytes`      | 使用十进制字节每秒代替十进制比特每秒。            |
| `--quiet`          | 抑制进度输出。                                   |
| `--data-size`      | 下载和上传测试的数据大小，单位为字节。            |
| `--timeout`        | 每个方向的限制时长。                             |

### 协议详情

客户端通过任意受支持的出站连接目标 `@SpeedTest`；服务器在连接进入路由之前拦截它。

#### 请求格式

| 类型（字节）                     | 数据长度（u32be） |
|----------------------------------|--------------------|
| `0x01` 下载 / `0x02` 上传        | 请求长度            |

#### 响应格式

| 状态（字节）                    | 消息长度（u16be）  | 消息     |
|----------------------------------|---------------------|----------|
| `0x00` OK / `0x01` 错误         | 消息长度             | 可变长   |

#### 上传摘要格式

服务器在完全接收上传测试数据后发送，无状态前缀。

| 时长（u32be，毫秒）              | 接收长度（u32be）   |
|-----------------------------------|------------------------|
