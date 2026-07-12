`socks` inbound is a socks4, socks4a, socks5 server.

### Structure

```json
{
  "type": "socks",
  "tag": "socks-in",

  ... // Listen Fields

  "users": [
    {
      "username": "admin",
      "password": "admin"
    }
  ],
  "speed_test": "allow"
}
```

### Listen Fields

See [Listen Fields](/configuration/shared/listen/) for details.

### Fields

#### users

SOCKS users.

No authentication required if empty.

#### speed_test

See [Private Speed Test](/configuration/shared/private-speedtest/) for details.
