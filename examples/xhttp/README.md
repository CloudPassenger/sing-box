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
