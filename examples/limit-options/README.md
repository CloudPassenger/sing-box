# Limit Options Local Validation

This directory contains quick local setups for validating the `limit-options`
rule action in realistic flows.

The examples are split by transport purpose:

- VLESS over TLS for TCP-oriented verification
- Shadowsocks for native UDP verification

VLESS is kept for the TCP path because this branch already ships working VLESS
examples. UDP verification uses Shadowsocks on purpose, because its packet path
is native UDP instead of UDP-over-TCP.

## Files

- `server-basic.json`, `client-domain.json`: client-side domain shaping before routing to the remote tunnel
- `server-user.json`, `client-alice.json`, `client-bob.json`: server-side per-user limits over the existing VLESS example tunnel
- `client-alice-forward-vless.json`, `client-bob-forward-vless.json`: local port-forward clients for fast TCP `iperf3` validation through VLESS
- `server-user-ss.json`: server-side per-user limits on a Shadowsocks inbound for native UDP verification
- `client-alice-forward-ss.json`, `client-bob-forward-ss.json`: local port-forward clients for fast TCP or UDP `iperf3` validation through Shadowsocks
- `server-stacked.json`, `client-stacked.json`: server-side stacked `limit-options` using both `user` and `domain_suffix`

All VLESS examples reuse the local self-signed TLS assets from `examples/xhttp`.

## Generate local TLS assets

Only needed for the VLESS examples.

```bash
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -keyout examples/xhttp/key.pem \
  -out examples/xhttp/cert.pem \
  -subj "/CN=localhost"
```

## Quick smoke checks

Run a local HTTP server first:

```bash
python3 -m http.server 18080 --bind 127.0.0.1
```

Server-side per-user rules still use `auth_user`, so they match the proxy
account name instead of the local process username.

```bash
./sing-box run -c examples/limit-options/server-user.json
./sing-box run -c examples/limit-options/client-alice.json
curl --proxy http://127.0.0.1:2090 http://127.0.0.1:18080
```

Switch to Bob:

```bash
./sing-box run -c examples/limit-options/client-bob.json
curl --proxy http://127.0.0.1:2091 http://127.0.0.1:18080
```

## TCP throughput validation with iperf3

This path uses VLESS over TLS and a local `direct` inbound as a port forwarder.
The local forwarded port is easier to target from `iperf3` than a SOCKS or HTTP
proxy port.

### Alice over VLESS

Terminal 1:

```bash
./sing-box run -c examples/limit-options/server-user.json
```

Terminal 2:

```bash
./sing-box run -c examples/limit-options/client-alice-forward-vless.json
```

Terminal 3:

```bash
iperf3 -s -p 18082
```

Terminal 4:

```bash
iperf3 -c 127.0.0.1 -p 19090 -R -t 12
```

Expected result: steady-state receive rate settles close to Alice's configured
`8 Mbps` total limit, with some extra throughput during the first interval due
to the configured burst.

### Bob over VLESS

Swap only the client forwarder and local port:

```bash
./sing-box run -c examples/limit-options/client-bob-forward-vless.json
iperf3 -c 127.0.0.1 -p 19091 -R -t 12
```

Expected result: steady-state receive rate settles near Bob's configured
`20 Mbps` total limit.

## Native UDP validation with iperf3

This path uses Shadowsocks because its outer transport is native UDP. The same
`auth_user` rules are enforced on the Shadowsocks inbound user names `alice` and
`bob`.

### Alice over Shadowsocks UDP

Terminal 1:

```bash
./sing-box run -c examples/limit-options/server-user-ss.json
```

Terminal 2:

```bash
./sing-box run -c examples/limit-options/client-alice-forward-ss.json
```

Terminal 3:

```bash
iperf3 -s -p 18082
```

Terminal 4:

```bash
iperf3 -u -c 127.0.0.1 -p 19190 -b 100M -t 5
```

Expected result: the sender still tries to push `100 Mbps`, but the receiver's
effective throughput is much lower and trends near Alice's `8 Mbps` limit. High
packet loss is normal here because UDP has no backpressure.

### Bob over Shadowsocks UDP

Swap only the client forwarder and local port:

```bash
./sing-box run -c examples/limit-options/client-bob-forward-ss.json
iperf3 -u -c 127.0.0.1 -p 19191 -b 100M -t 5
```

Expected result: the receiver gets materially more throughput than Alice and
trends toward Bob's `20 Mbps` limit, again with loss if the offered load stays
far above the limit.

## Stacked server-side limits

The server first applies a user-scoped total limit, then an additional rule-
scoped limit for `localhost` traffic. Both limits are active at the same time.

```bash
./sing-box run -c examples/limit-options/server-stacked.json
./sing-box run -c examples/limit-options/client-stacked.json
curl --proxy http://127.0.0.1:2092 http://127.0.0.1:18080
```

## Client-side domain shaping before routing

The client applies `limit-options` to `localhost` traffic first, then routes the
same traffic to the remote VLESS server. Other traffic falls back to `direct`.

```bash
./sing-box run -c examples/limit-options/server-basic.json
./sing-box run -c examples/limit-options/client-domain.json
curl --proxy http://127.0.0.1:2093 http://127.0.0.1:18080
```
