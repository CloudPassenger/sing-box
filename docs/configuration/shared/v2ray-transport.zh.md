V2Ray Transport 是 v2ray 发明的一组私有协议，并污染了其他协议的名称，如 clash 中的 `trojan-grpc`。

### 结构

```json
{
  "type": ""
}
```

可用的传输协议：

* HTTP
* WebSocket
* QUIC
* gRPC
* HTTPUpgrade
* XHTTP

!!! warning "与 v2ray-core 的区别"

    * 没有 TCP 传输层, 纯 HTTP 已合并到 HTTP 传输层。
    * 没有 mKCP 传输层。
    * 没有 DomainSocket 传输层。

!!! note ""

    当内容只有一项时，可以忽略 JSON 数组 [] 标签。

### HTTP

```json
{
  "type": "http",
  "host": [],
  "path": "",
  "method": "",
  "headers": {},
  "idle_timeout": "15s",
  "ping_timeout": "15s"
}
```

!!! warning "与 v2ray-core 的区别"

    不强制执行 TLS。如果未配置 TLS，将使用纯 HTTP 1.1。

#### host

主机域名列表。

如果设置，客户端将随机选择，服务器将验证。

#### path

!!! warning

    V2Ray 文档称服务端和客户端的路径必须一致，但实际代码允许客户端向路径添加任何后缀。
    sing-box 使用与 V2Ray 相同的行为，但请注意，该行为在 `WebSocket` 和 `HTTPUpgrade` 传输层中不存在。

HTTP 请求路径

服务器将验证。

#### method

HTTP 请求方法

如果设置，服务器将验证。

#### headers

HTTP 请求的额外标头

如果设置，服务器将写入响应。

#### idle_timeout

在 HTTP2 服务器中：

指定闲置客户端应在多长时间内使用 GOAWAY 帧关闭。PING 帧不被视为活动。

在 HTTP2 客户端中：

如果连接上没有收到任何帧，指定一段时间后将使用 PING 帧执行健康检查。需要注意的是，PING 响应被视为已接收的帧，因此如果连接上没有其他流量，则健康检查将在每个间隔执行一次。如果值为零，则不会执行健康检查。

默认使用零。

#### ping_timeout

在 HTTP2 客户端中：

指定发送 PING 帧后，在指定的超时时间内必须接收到响应。如果在指定的超时时间内没有收到 PING 帧的响应，则连接将关闭。默认超时持续时间为 15 秒。

### WebSocket

```json
{
  "type": "ws",
  "path": "",
  "headers": {},
  "max_early_data": 0,
  "early_data_header_name": ""
}
```

#### path

HTTP 请求路径

服务器将验证。

#### headers

HTTP 请求的额外标头

如果设置，服务器将写入响应。

#### max_early_data

请求中允许的最大有效负载大小。默认启用。

#### early_data_header_name

默认情况下，早期数据在路径而不是标头中发送。

要与 Xray-core 兼容，请将其设置为 `Sec-WebSocket-Protocol`。

它需要与服务器保持一致。

### QUIC

```json
{
  "type": "quic"
}
```

!!! warning "与 v2ray-core 的区别"

    没有额外的加密支持：
    它基本上是重复加密。 并且 Xray-core 在这里与 v2ray-core 不兼容。

### gRPC

!!! note ""

    默认安装不包含标准 gRPC (兼容性好，但性能较差), 参阅 [安装](/zh/installation/build-from-source/#构建标记)。

```json
{
  "type": "grpc",
  "service_name": "TunService",
  "idle_timeout": "15s",
  "ping_timeout": "15s",
  "permit_without_stream": false
}
```

#### service_name

gRPC 服务名称。

#### idle_timeout

在标准 gRPC 服务器/客户端：

如果传输在此时间段后没有看到任何活动，它会向客户端发送 ping 请求以检查连接是否仍然活动。

在默认 gRPC 服务器/客户端：

它的行为与 HTTP 传输层中的相应设置相同。

#### ping_timeout

在标准 gRPC 服务器/客户端：

经过一段时间之后，客户端将执行 keepalive 检查并等待活动。如果没有检测到任何活动，则会关闭连接。

在默认 gRPC 服务器/客户端：

它的行为与 HTTP 传输层中的相应设置相同。

#### permit_without_stream

在标准 gRPC 客户端：

如果启用，客户端传输即使没有活动连接也会发送 keepalive ping。如果禁用，则在没有活动连接时，将忽略 `idle_timeout` 和 `ping_timeout`，并且不会发送 keepalive ping。

默认禁用。

### HTTPUpgrade

```json
{
  "type": "httpupgrade",
  "host": "",
  "path": "",
  "headers": {}
}
```

#### host

主机域名。

服务器将验证。

#### path

HTTP 请求路径

服务器将验证。

#### headers

HTTP 请求的额外标头。

如果设置，服务器将写入响应。

### XHTTP

!!! note ""

    XHTTP 是一种 Xray 风格的 HTTP 传输层。它通过同一套 V2Ray transport 入口供 VLESS、VMess 和 Trojan 使用。

!!! warning "作用范围"

    * `h3` 需要使用 `with_quic` 构建，因为当 TLS ALPN 设置为 `h3` 时，传输层会切换到基于 QUIC 的 HTTP/3。
    * `packet-up` 会改变上行数据的承载方式，投入生产前应先与目标客户端和服务端完成联调。
    * `download` 会在客户端创建一条副下载链路。它适合单独配置下载路径，不适合在不同服务器之间共享服务端会话状态。

```json
{
  "type": "xhttp",
  "mode": "auto",
  "host": "",
  "path": "",
  "headers": {},
  "x_padding_bytes": "100-1000",
  "no_grpc_header": false,
  "no_sse_header": false,
  "sc_max_each_post_bytes": 1000000,
  "sc_min_posts_interval_ms": 30,
  "sc_max_buffered_posts": 30,
  "sc_stream_up_server_secs": "20-80",
  "server_max_header_bytes": 8192,
  "trusted_x_forwarded_for": [],
  "xmux": {},
  "x_padding_obfs_mode": false,
  "x_padding_key": "x_padding",
  "x_padding_header": "X-Padding",
  "x_padding_placement": "queryInHeader",
  "x_padding_method": "repeat-x",
  "uplink_http_method": "POST",
  "session_placement": "path",
  "session_key": "",
  "session_id_table": "",
  "session_id_length": 0,
  "seq_placement": "path",
  "seq_key": "",
  "uplink_data_placement": "auto",
  "uplink_data_key": "",
  "uplink_chunk_size": 0,
  "download": {}
}
```

#### mode

XHTTP 传输模式。

可用值：

* `auto`
* `packet-up`
* `stream-up`
* `stream-one`

默认为 `auto`。

`stream-one` 使用单个请求流。

`stream-up` 将上传和下载拆成独立请求。

`packet-up` 将上行数据拆分为离散的 HTTP 上传请求，并允许使用 `body` 以外的 `uplink_data_placement`。

#### host

请求 URL 使用的主机名。

客户端优先级为 `host` > TLS `server_name` > 出站 `server`。

如果设置，服务器会验证。

#### path

基础请求路径。

规范化后路径始终以 `/` 开头；当 `session_placement` 或 `seq_placement` 为 `path`（默认值）时也会以 `/` 结尾，因为该斜杠用于分隔基础路径与附加的会话/序列号片段；否则不会追加。查询参数可以直接附加在该字段中，例如 `/xhttp/?ed=1`。

#### headers

额外 HTTP 请求头。

这里不接受 `Host` 头，请使用 `host` 字段。

#### x_padding_bytes

填充长度范围。

该字段是必需的，XHTTP 不允许禁用 padding。

默认值为 `100-1000`。

#### no_grpc_header

不在客户端上传请求中发送 `Content-Type: application/grpc`。

默认禁用。

#### no_sse_header

不在服务端下载响应中发送 `Content-Type: text/event-stream`。

默认禁用。

#### sc_max_each_post_bytes

每个上传请求体的最大大小。

默认值为 `1000000`。

它必须大于 `0`。上传数据会先按该大小拆分为多个分块后再发送。

#### sc_min_posts_interval_ms

上传 POST 请求之间的最小间隔，单位为毫秒。

默认值为 `30`。

#### sc_max_buffered_posts

服务端缓冲的最大上传请求数。

默认值为 `30`。

#### sc_stream_up_server_secs

服务端在 `stream-up` 模式下保持上传链路存活时使用的填充刷新间隔。

默认值为 `20-80`。

#### server_max_header_bytes

服务端 HTTP 请求头的最大大小。

默认值为 `8192`。

#### trusted_x_forwarded_for

被信任为证明 `X-Forwarded-For` 由自身反向代理（如 nginx）设置、而非由客户端伪造的标头名称列表。

若为空，收到的 `X-Forwarded-For` 永远不会被信任；将使用真实的连接地址，并记录一条警告日志。

若已设置，仅当请求同时携带列表中至少一个标头时，收到的 `X-Forwarded-For` 才会被信任；否则将被视为可能的伪造行为，并记录一条错误日志。

#### xmux

可选的 XHTTP 连接复用设置。

如果省略，sing-box 默认使用 `max_connections: 6`（多个独立连接，而非单个长期复用的连接；对专门针对 XHTTP 的 DPI / 审查特征识别而言，后者是更强的信号）。

结构：

```json
{
  "max_concurrency": 0,
  "max_connections": 6,
  "c_max_reuse_times": 0,
  "h_max_request_times": "600-900",
  "h_max_reusable_secs": "1800-3000",
  "h_keep_alive_period": 0
}
```

`max_connections` 与 `max_concurrency` 不能同时使用。

#### x_padding_obfs_mode

使用配置的 placement 和 method 来写入 XHTTP padding，而不是默认兼容行为。

默认禁用。

#### x_padding_key

在 `query`、`queryInHeader` 或 cookie placement 中使用的 padding 键名。

默认值为 `x_padding`。

#### x_padding_header

基于 header 的 padding placement 使用的头名称。

默认值为 `X-Padding`。

#### x_padding_placement

Padding 的放置位置。

可用值：

* `queryInHeader`
* `cookie`
* `header`
* `query`

默认值为 `queryInHeader`。

#### x_padding_method

Padding 编码方式。

可用值：

* `repeat-x`
* `tokenish`

默认值为 `repeat-x`。

#### uplink_http_method

客户端上传时使用的 HTTP 方法。

默认值为 `POST`。

`GET` 仅在 `packet-up` 模式下可用。

#### session_placement

XHTTP 会话标识的位置。

可用值：

* `path`
* `cookie`
* `header`
* `query`

默认值为 `path`。

#### session_key

当 `session_placement` 不为 `path` 时使用的字段名。

默认情况下，`cookie` 和 `query` 使用 `x_session`，`header` 使用 `X-Session`。

#### session_id_table

生成会话标识时使用的字符集，替代随机 UUID。

预定义简写名称（`ALPHABET`、`Alphabet`、`BASE36`、`Base62`、`HEX`、`alphabet`、`base36`、`hex`、`number`）会展开为对应字符集；其他任何非空值将按原样作为字符集使用，且只能包含 ASCII 字符。

若为空，会话标识为随机 UUID。

#### session_id_length

设置 `session_id_table` 时，生成会话标识的长度范围。

`session_id_table` 与 `session_id_length` 的组合必须能提供至少 2^31 个不同的标识，且 `session_id_length.from` 必须大于 `0`。

#### seq_placement

上传序列号的位置。

可用值：

* `path`
* `cookie`
* `header`
* `query`

默认值为 `path`。

#### seq_key

当 `seq_placement` 不为 `path` 时使用的字段名。

默认情况下，`cookie` 和 `query` 使用 `x_seq`，`header` 使用 `X-Seq`。

#### uplink_data_placement

包上传时数据的承载位置。

可用值：

* `auto`
* `body`
* `cookie`
* `header`

默认值为 `auto`。

`auto` 可同时接受来自 header、cookie 与 body 的数据（按此顺序拼接），因此单个入站可以同时服务采用不同承载位置的客户端。`cookie` 和 `header` 仅在 `packet-up` 模式下可用。

#### uplink_data_key

当 `uplink_data_placement` 不为 `body` 时使用的字段名。

默认情况下，`cookie` 使用 `x_data`，`header` 使用 `X-Data`。

#### uplink_chunk_size

当数据被拆分到 headers 或 cookies 中时使用的分块大小。

默认情况下，`cookie` 使用 `3072`，`header` 使用 `4096`。

小于 `64` 的值会被规范化为 `64`。

#### download

客户端可选的副下载链路。

结构：

```json
{
  "server": "",
  "server_port": 0,
  "detour": "",
  "host": "",
  "path": "",
  "x_padding_bytes": "100-1000",
  "tls": {},
  "xmux": {}
}
```

它接受与主链路相同的基础 XHTTP 字段，另外还支持：

* `server`
* `server_port`
* `detour`
* `tls`

适用于为下载流量指定单独的出站路径或 TLS 配置。
