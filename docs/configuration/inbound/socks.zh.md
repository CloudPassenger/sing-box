`socks` 入站是一个 socks4, socks4a 和 socks5 服务器.

### 结构

```json
{
  "type": "socks",
  "tag": "socks-in",

  ... // 监听字段

  "users": [
    {
      "username": "admin",
      "password": "admin"
    }
  ],
  "speed_test": "allow"
}
```

### 监听字段

参阅 [监听字段](/zh/configuration/shared/listen/)。

### 字段

#### users

SOCKS 用户

如果为空则不需要验证。

#### speed_test

参阅 [私有测速](/zh/configuration/shared/private-speedtest/)。
