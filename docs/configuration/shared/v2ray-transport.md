V2Ray Transport is a set of private protocols invented by v2ray, and has contaminated the names of other protocols, such
as `trojan-grpc` in clash.

### Structure

```json
{
  "type": ""
}
```

Available transports:

* HTTP
* WebSocket
* QUIC
* gRPC
* HTTPUpgrade
* XHTTP

!!! warning "Difference from v2ray-core"

    * No TCP transport, plain HTTP is merged into the HTTP transport.
    * No mKCP transport.
    * No DomainSocket transport.

!!! note ""

    You can ignore the JSON Array [] tag when the content is only one item

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

!!! warning "Difference from v2ray-core"

    TLS is not enforced. If TLS is not configured, plain HTTP 1.1 is used.

#### host

List of host domain.

The client will choose randomly and the server will verify if not empty.

#### path

!!! warning

    V2Ray's documentation says that the path between the server and the client must be consistent, 
    but the actual code allows the client to add any suffix to the path.
    sing-box uses the same behavior as V2Ray, but note that the behavior does not exist in `WebSocket` and `HTTPUpgrade` transport.

Path of HTTP request.

The server will verify.

#### method

Method of HTTP request.

The server will verify if not empty.

#### headers

Extra headers of HTTP request.

The server will write in response if not empty.

#### idle_timeout

In HTTP2 server:

Specifies the time until idle clients should be closed with a GOAWAY frame. PING frames are not considered as activity.

In HTTP2 client:

Specifies the period of time after which a health check will be performed using a ping frame if no frames have been
received on the connection.Please note that a ping response is considered a received frame, so if there is no other
traffic on the connection, the health check will be executed every interval. If the value is zero, no health check will
be performed.

Zero is used by default.

#### ping_timeout

In HTTP2 client:

Specifies the timeout duration after sending a PING frame, within which a response must be received.
If a response to the PING frame is not received within the specified timeout duration, the connection will be closed.
The default timeout duration is 15 seconds.

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

Path of HTTP request.

The server will verify.

#### headers

Extra headers of HTTP request.

The server will write in response if not empty.

#### max_early_data

Allowed payload size is in the request. Enabled if not zero.

#### early_data_header_name

Early data is sent in path instead of header by default.

To be compatible with Xray-core, set this to `Sec-WebSocket-Protocol`.

It needs to be consistent with the server.

### QUIC

```json
{
  "type": "quic"
}
```

!!! warning "Difference from v2ray-core"

    No additional encryption support:
    It's basically duplicate encryption. And Xray-core is not compatible with v2ray-core in here.

### gRPC

!!! note ""

    standard gRPC has good compatibility but poor performance and is not included by default, see [Installation](/installation/build-from-source/#build-tags).

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

Service name of gRPC.

#### idle_timeout

In standard gRPC server/client:

If the transport doesn't see any activity after a duration of this time,
it pings the client to check if the connection is still active.

In default gRPC server/client:

It has the same behavior as the corresponding setting in HTTP transport.

#### ping_timeout

In standard gRPC server/client:

The timeout that after performing a keepalive check, the client will wait for activity.
If no activity is detected, the connection will be closed.

In default gRPC server/client:

It has the same behavior as the corresponding setting in HTTP transport.

#### permit_without_stream

In standard gRPC client:

If enabled, the client transport sends keepalive pings even with no active connections.
If disabled, when there are no active connections, `idle_timeout` and `ping_timeout` will be ignored and no keepalive
pings will be sent.

Disabled by default.

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

Host domain.

The server will verify if not empty.

#### path

Path of HTTP request.

The server will verify.

#### headers

Extra headers of HTTP request.

The server will write in response if not empty.

### XHTTP

!!! note ""

    XHTTP is an Xray-style HTTP transport. It is supported by the same V2Ray transport entry used by VLESS, VMess and Trojan.

!!! warning "Scope"

    * `h3` requires a build with `with_quic`, because the transport switches to HTTP/3 over QUIC when TLS ALPN is set to `h3`.
    * `packet-up` changes how uplink data is carried. Validate it with the target client and server before using it in production.
    * `download` creates a secondary HTTP leg on the client side. It is intended for a separately configured download path, not for sharing server-side session state across different servers.

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
  "xmux": {},
  "x_padding_obfs_mode": false,
  "x_padding_key": "x_padding",
  "x_padding_header": "X-Padding",
  "x_padding_placement": "queryInHeader",
  "x_padding_method": "repeat-x",
  "uplink_http_method": "POST",
  "session_placement": "path",
  "session_key": "",
  "seq_placement": "path",
  "seq_key": "",
  "uplink_data_placement": "body",
  "uplink_data_key": "",
  "uplink_chunk_size": 0,
  "download": {}
}
```

#### mode

XHTTP transport mode.

Available values:

* `auto`
* `packet-up`
* `stream-up`
* `stream-one`

Defaults to `auto`.

`stream-one` uses a single request stream.

`stream-up` uses separate upload and download requests.

`packet-up` splits uplink data into discrete HTTP uploads and enables `uplink_data_placement` values other than `body`.

#### host

Host used in the request URL.

Client priority is `host` > TLS `server_name` > outbound `server`.

The server validates it when set.

#### path

Base request path.

The normalized path always starts and ends with `/`. Query parameters can be appended directly in this field, for example `/xhttp/?ed=1`.

#### headers

Extra HTTP headers.

The `Host` header is not accepted here. Use `host` instead.

#### x_padding_bytes

Padding size range.

This field is required. XHTTP rejects disabled padding.

Defaults to `100-1000`.

#### no_grpc_header

Do not send `Content-Type: application/grpc` on client upload requests.

Disabled by default.

#### no_sse_header

Do not send `Content-Type: text/event-stream` on server download responses.

Disabled by default.

#### sc_max_each_post_bytes

Maximum size of each upload request body.

Defaults to `1000000`.

It must be greater than `8192`.

#### sc_min_posts_interval_ms

Minimum interval between upload POST requests in milliseconds.

Defaults to `30`.

#### sc_max_buffered_posts

Maximum number of buffered upload posts kept by the server.

Defaults to `30`.

#### sc_stream_up_server_secs

Padding flush interval used by the server in `stream-up` mode when it keeps the upload side alive.

Defaults to `20-80`.

#### xmux

Optional XHTTP connection reuse settings.

If omitted, sing-box enables a conservative default xmux profile.

Structure:

```json
{
  "max_concurrency": 1,
  "max_connections": 0,
  "c_max_reuse_times": 0,
  "h_max_request_times": "600-900",
  "h_max_reusable_secs": "1800-3000",
  "h_keep_alive_period": 0
}
```

`max_connections` and `max_concurrency` cannot be used together.

#### x_padding_obfs_mode

Apply XHTTP padding using the configured placement and method instead of the default compatibility behavior.

Disabled by default.

#### x_padding_key

Padding key name used by `query`, `queryInHeader`, or cookie placements.

Defaults to `x_padding`.

#### x_padding_header

Header name used by header-based padding placement.

Defaults to `X-Padding`.

#### x_padding_placement

Padding placement.

Available values:

* `queryInHeader`
* `cookie`
* `header`
* `query`

Defaults to `queryInHeader`.

#### x_padding_method

Padding encoding method.

Available values:

* `repeat-x`
* `tokenish`

Defaults to `repeat-x`.

#### uplink_http_method

HTTP method used for client uploads.

Defaults to `POST`.

`GET` is only accepted in `packet-up` mode.

#### session_placement

Where to place the XHTTP session identifier.

Available values:

* `path`
* `cookie`
* `header`
* `query`

Defaults to `path`.

#### session_key

Field name used when `session_placement` is not `path`.

Defaults to `x_session` for `cookie` and `query`, and `X-Session` for `header`.

#### seq_placement

Where to place the upload sequence value.

Available values:

* `path`
* `cookie`
* `header`
* `query`

Defaults to `path`.

When `session_placement` is `path`, `seq_placement` must also be `path`.

#### seq_key

Field name used when `seq_placement` is not `path`.

Defaults to `x_seq` for `cookie` and `query`, and `X-Seq` for `header`.

#### uplink_data_placement

Where packet upload payload is stored.

Available values:

* `body`
* `cookie`
* `header`

Defaults to `body`.

`cookie` and `header` are only accepted in `packet-up` mode.

#### uplink_data_key

Field name used when `uplink_data_placement` is not `body`.

Defaults to `x_data` for `cookie` and `X-Data` for `header`.

#### uplink_chunk_size

Chunk size used when payload is split into headers or cookies.

Defaults to `3072` for `cookie` and `4096` for `header`.

Values below `64` are normalized to `64`.

#### download

Optional secondary download leg for the client.

Structure:

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

It accepts the same base XHTTP fields as the primary leg, plus:

* `server`
* `server_port`
* `detour`
* `tls`

Use it when download traffic needs a separate outbound path or TLS profile.
