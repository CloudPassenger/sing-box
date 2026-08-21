# XHTTP Local Validation

This directory contains local client/server setups used to smoke-test the
migrated XHTTP transport on the `stable` branch.

## Files

- `server.json`, `client.json`: VLESS over XHTTP in `stream-up` mode
- `server-stream-one.json`, `client-stream-one.json`: VLESS over XHTTP in `stream-one` mode
- `server-trojan.json`, `client-trojan.json`: Trojan over XHTTP in `stream-up` mode
- `server-vmess.json`, `client-vmess.json`: VMess over XHTTP in `stream-up` mode
- `server-download.json`, `client-download.json`: VLESS with independent download leg config
- `server-packet-up.json`, `client-packet-up.json`: VLESS over XHTTP in `packet-up` mode
- `server-h3.json`, `client-h3.json`: VLESS over XHTTP with `h3`
- `client-xmux.json`: VLESS client with aggressive xmux reuse settings for smoke tests
- `cert.pem`, `key.pem`: local self-signed TLS assets for h2 testing

## Generate local TLS assets

```bash
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -keyout examples/xhttp/key.pem \
  -out examples/xhttp/cert.pem \
  -subj "/CN=localhost"
```

## Smoke test flow

1. Start a local target server:

```bash
python3 -m http.server 18080 --bind 127.0.0.1
```

2. Start the XHTTP server:

```bash
./sing-box run -c examples/xhttp/server.json
```

3. Start the XHTTP client:

```bash
./sing-box run -c examples/xhttp/client.json
```

4. Send a request through the local mixed proxy:

```bash
curl --proxy http://127.0.0.1:2080 http://127.0.0.1:18080
```

If the migrated transport is working, the request reaches the local HTTP server
through the XHTTP tunnel.

## Additional smoke tests

### VLESS stream-one

```bash
./sing-box run -c examples/xhttp/server-stream-one.json
./sing-box run -c examples/xhttp/client-stream-one.json
curl --proxy http://127.0.0.1:2081 http://127.0.0.1:18080
```

### Trojan stream-up

```bash
./sing-box run -c examples/xhttp/server-trojan.json
./sing-box run -c examples/xhttp/client-trojan.json
curl --proxy http://127.0.0.1:2082 http://127.0.0.1:18080
```

### VMess stream-up

```bash
./sing-box run -c examples/xhttp/server-vmess.json
./sing-box run -c examples/xhttp/client-vmess.json
curl --proxy http://127.0.0.1:2083 http://127.0.0.1:18080
```

### VLESS with download leg

```bash
./sing-box run -c examples/xhttp/server-download.json
./sing-box run -c examples/xhttp/client-download.json
curl --proxy http://127.0.0.1:2084 http://127.0.0.1:18080
```

### VLESS packet-up

```bash
./sing-box run -c examples/xhttp/server-packet-up.json
./sing-box run -c examples/xhttp/client-packet-up.json
curl --proxy http://127.0.0.1:2085 http://127.0.0.1:18080
```

### VLESS h3

```bash
./sing-box run -c examples/xhttp/server-h3.json
./sing-box run -c examples/xhttp/client-h3.json
curl --proxy http://127.0.0.1:2086 http://127.0.0.1:18080
```

## Xray-core interoperability testing

In addition to the sing-box-only smoke tests above, each subdirectory below
pairs a sing-box config with an equivalent Xray-core config for the same
XHTTP behavior, so the two implementations can be tested against each other
directly (not just against themselves). This exercises the protocol-level
parity changes made on this branch against a real `XTLS/Xray-core` build,
rather than only against another sing-box instance.

### Layout

```
<scenario>/sing-box/server.json              # sing-box server
<scenario>/sing-box/client.json               # sing-box client -> sing-box server (baseline)
<scenario>/sing-box/client-to-xray.json       # sing-box client -> xray-core server (cross)
<scenario>/xray-core/server.json              # xray-core server
<scenario>/xray-core/client.json              # xray-core client -> xray-core server (baseline)
<scenario>/xray-core/client-to-singbox.json   # xray-core client -> sing-box server (cross)
```

Scenarios (all under `examples/xhttp/`):

| Scenario | `mode` | What it exercises |
| --- | --- | --- |
| `packet-up/` | `packet-up` | header-placed uplink data, randomized `uplink_chunk_size` |
| `stream-up/` | `stream-up` | baseline bidirectional streaming |
| `stream-one/` | `stream-one` | single-request full-duplex mode |
| `h3/` | `auto` (ALPN `h3`) | QUIC/HTTP-3 transport |
| `mixed-placement/` | `packet-up` | `session_placement=header` + `seq_placement=path` (arbitrary combination, the core bug fixed on this branch) |
| `padding-cookie/` | `stream-up` | `x_padding_obfs_mode` + `x_padding_placement=cookie` (server response padding cookie, previously never applied) |
| `uplink-auto/` | `packet-up` | server left on default `uplink_data_placement=auto`, client forces `header` |
| `session-id-table/` | `stream-up` | custom `session_id_table`/`session_id_length` (`Base62`, 8-12 chars) |
| `uplink-chunk-size/` | `packet-up` | cookie-placed uplink data with a small `uplink_chunk_size` range, forcing multi-chunk reassembly |

All scenarios share the same UUID/TLS cert as the base smoke tests above and
use ports in the `28000-28019`/`13000-13019` range; see each `server.json`
for the exact port.

### Xray-core specifics

- Xray-core's `freedom` (`direct`) outbound blocks private/loopback IPs by
  default when reached from a proxy inbound (anti-SSRF safeguard). Since
  these test targets are all `127.0.0.1`, every `xray-core/server.json` sets
  `"finalRules": [{"action": "allow", "ip": ["0.0.0.0/0", "::/0"]}]` on its
  freedom outbound. **Do not copy this into a production config** — it
  disables that safeguard entirely.
- Xray-core removed `tlsSettings.allowInsecure`; client configs pin the test
  certificate instead via `tlsSettings.pinnedPeerCertSha256` (see
  `openssl x509 -in examples/xhttp/cert.pem -noout -fingerprint -sha256`).
- Xray-core config field names are camelCase (`sessionIDPlacement`,
  `uplinkDataPlacement`, ...) while sing-box uses snake_case
  (`session_placement`, `uplink_data_placement`, ...); the two config trees
  are otherwise structurally equivalent field-for-field.

### Running a scenario

```bash
# terminal 1: local HTTP target
python3 -m http.server 18080 --bind 127.0.0.1

# terminal 2/3: pick a server (sing-box or xray-core)
./sing-box run -c examples/xhttp/<scenario>/sing-box/server.json
# or
xray run -c examples/xhttp/<scenario>/xray-core/server.json

# terminal 4: pick a client, matching the server you started
./sing-box run -c examples/xhttp/<scenario>/sing-box/client-to-xray.json      # sing-box -> xray-core
xray run -c examples/xhttp/<scenario>/xray-core/client-to-singbox.json       # xray-core -> sing-box

curl --proxy http://127.0.0.1:<client-local-port> http://127.0.0.1:18080
```

### Results (sing-box HEAD `71dbccdab` + xray-core `main` @ `d5bc58d`)

Every scenario was run with all four client/server combinations (sing-box
client + sing-box server; sing-box client + xray-core server; xray-core
client + xray-core server; xray-core client + sing-box server) via `curl`
through the local mixed/socks inbound. All 36 combinations (9 scenarios x 4
combinations) passed:

- `mixed-placement`: confirms arbitrary `session_placement`/`seq_placement`
  combinations interop correctly in both directions (this branch's fix for
  the "both must be path, or both must be non-path" limitation).
- `padding-cookie`: a raw `curl -v` against the sing-box server directly
  confirms the response actually carries `Set-Cookie: x_padding=...` (this
  branch's fix for cookie-placement response padding never being applied).
- `uplink-chunk-size`: verified beyond a plain `curl` GET by POSTing random
  9000-byte payloads (larger than `sc_max_each_post_bytes`, forcing multiple
  POSTs, each split into randomized 64-128 byte cookie chunks) through all
  four combinations to a local echo server and comparing SHA-256 digests of
  the round-tripped body — confirms multi-post, multi-chunk reassembly is
  byte-exact across both implementations in both directions.
- `h3`: confirmed over real QUIC (ALPN `h3`) in all four combinations.

Note: the initial `uplink-chunk-size` config used
`sc_max_each_post_bytes: 32768` with 64-128 byte cookie chunks, which
base64-encodes to a single request carrying a ~43 KB `Cookie` header — far
past either implementation's default 8192-byte header limit, causing the
request to hang. This was a test-config mistake, not a transport bug; the
committed config uses `sc_max_each_post_bytes: 3000` instead.
