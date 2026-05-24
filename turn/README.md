# turn/ — CoTURN 模块

## 这模块干嘛

WebRTC P2P 直连在严格 NAT / VPN / 公司防火墙后会失败。
CoTURN 在服务器上跑一个 STUN + TURN relay，**通话失败时所有 RTP 走中继**，
保证通话 100% 接通（牺牲带宽换可达性）。

参考：[035] 引入原因——iPhone 挂 VPN 时 ICE 全是 `198.18.0.1` / `127.0.0.1`，
跟访客（中国电信公网）无法 P2P 打洞，15 秒后超时报「连接失败」。

## 依赖关系

```
谁会调用它（作为 TURN client）：
  - widget   (访客浏览器 WebRTC)
  - admin    (客服 Web 浏览器 WebRTC)
  - mobile_app (客服 iPhone flutter_webrtc)
  ↓
  通话前都先调 backend GET /api/turn-credential 拿短期凭证

它会调用谁：
  - 复用 cs_ssl_data 命名卷里的 maihaocs.icu Let's Encrypt 证书（5349/TLS 用）
  - 跟 backend 共享 TURN_STATIC_AUTH_SECRET（环境变量），不走网络通信
```

## 关键开关 / 配置

| 配置 | 位置 | 说明 |
|---|---|---|
| `TURN_EXTERNAL_IP` | `.env` | 服务器公网 IP，必填。否则 srflx 报内网地址，打洞失败 |
| `TURN_REALM` | `.env` | 必填，例如 `maihaocs.icu`。也用来找证书：`/etc/coturn/ssl-src/${TURN_REALM}.pem` |
| `TURN_STATIC_AUTH_SECRET` | `.env` | 必填，跟 backend 共享的 HMAC 密钥。一旦泄露，任何人能用我们的 TURN 中继 |
| 监听端口 | `turn/turnserver.conf.tmpl:9-10` | 3478 + 5349 |
| relay 端口范围 | `turn/turnserver.conf.tmpl:14-15` | 49152-49200 (49 个端口) |
| 配额 | `turn/turnserver.conf.tmpl:56-58` | 全局 200 / 单用户 10 |

## 已知的坑 / 历史遗留

1. **必须 network_mode: host**（docker-compose.yml 里已配）。原因：CoTURN UDP relay
   端口范围 49152-49200 走 docker bridge 时 docker-proxy 会成为性能瓶颈，并且 IP
   伪装会让 srflx 上报错误的 IP。host 模式直连宿主网卡，零损耗。
2. **TLS 证书首次启动可能没有**：entrypoint.sh 会自动生成 30 天临时自签证书占位，
   保证 turnserver 起得来（5349/TLS 端口仍可监听只是浏览器不信任）。后续 nginx
   acme.sh 申请到真证书后重启 coturn 容器即生效。
3. **denied-peer-ip 必须保留**：CoTURN 默认允许 client 让服务器去访问任意 IP，
   形成 SSRF 风险。conf 里已显式拒绝所有 RFC 1918/保留段。
4. **静态密钥泄露**：一旦 `TURN_STATIC_AUTH_SECRET` 泄露，应立刻 `.env` 改值 +
   `docker compose up -d --build coturn backend` 重启两侧同步密钥。

## 短期凭证算法（与 backend 共享）

```
timestamp = unix_time() + 86400           # 24h 过期
username  = "<timestamp>:<userid>"        # 例如 "1779712345:visitor-abc"
password  = base64(HMAC-SHA1(static-auth-secret, username))
```

CoTURN 收到 client 上报 `<username, password>` 后：
1. 解析 username 里的 timestamp，验证未过期
2. 用 `static-auth-secret` HMAC 重算 password，比对相等就放行

backend 实现见 `backend/internal/service/turn.go`。

## 上次重大改动

- **2026-05-24** 引入 ([035])。原因：iPhone 端 VPN 导致 WebRTC P2P 失败。
- 详见 CHANGELOG.md [035]。
