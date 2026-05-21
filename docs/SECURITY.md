# 安全机制清单

> 爷爷铁律：「所有安全机制不要形同虚设，你要帮我验证！要对安全机制进行『绕过』类型的测试」。
> 下面每一项都附带「如何验证 / 如何尝试绕过」。

## 1. 防 SQL 注入

- **机制**：100% 使用 `database/sql` 的 `?` 占位符；DSN 关闭 `interpolateParams`（驱动不在客户端拼 SQL，全部走服务端 prepared statement）。
- **额外侦测**：`security.DetectSQLInjection` 对访客输入做模式匹配，发现可疑 payload 时记录 + 计数（达到阈值触发 IP 拉黑），但不依赖它来拦截。
- **绕过测试**：
  ```bash
  curl -X POST https://<域名>/api/agent/login \
    -H 'Content-Type: application/json' \
    -d '{"username":"admin'\'' OR 1=1 --","password":"x"}'
  # 期望：返回 40105 账号或密码错误 (而非登录成功 / SQL 报错)
  ```
- **验证 my.cnf**：`local_infile=0`（防 LOAD DATA LOCAL INFILE 扩面）。

## 2. 防 XSS

- 所有访客 / 客服文本消息走 `bluemonday.StrictPolicy()`，剥光所有 HTML / JS。
- 前端展示用 `textContent` / Vue 默认转义 / chat.html 内 `escape()`。
- **绕过测试**：发送 `<img src=x onerror=alert(1)>` 应被服务端剥成 `` 或纯文本。

## 3. 防 DDoS / 防同 IP 暴力

- Nginx 层：`limit_req_zone`（API 20 r/s / 登录 2 r/s / WSS 5 r/s）+ `limit_conn_zone`（单 IP 最多 200 并发）
- Backend 层：按 IP 的 HTTP 限流（60 req/min 默认，可调）+ 按 IP 的 WSS 握手限流（5 次/min）
- 累计违规阈值（24h 内 200 次）→ Redis 自动拉黑 24h
- **绕过测试**：
  ```bash
  # 100 次连发登录
  for i in $(seq 1 100); do
    curl -X POST -H 'Content-Type: application/json' \
      -d '{"username":"admin","password":"x"}' \
      https://<域名>/api/agent/login &
  done
  # 期望：Nginx 层先被 429；后端只见到 ~120 次（2 r/s * 60s burst）
  # 实际行为：随后 IP 在 Redis 累计被记 violation；达到阈值即 24h 拉黑。
  ```

## 4. 防访客刷消息

- 单访客每分钟 10 条上限（`SECURITY_VISITOR_MSG_PM`）。超限：服务端发送 `error` 帧给该访客 + 记 violation。
- **验证**：用一份 visitor_token 在 60s 内连发 15 条；第 11 条开始应收到 error。

## 5. 密码 / Token 加密

- 客服密码：bcrypt cost=12（每次约 250ms，对抗爆破）
- 访客 IP 持久化：AES-256-GCM 加密（密钥从 `DATA_AES_KEY` env 注入，仓库 0 明文）
- JWT：HS256，secret 至少 32 字符（启动时强制校验，弱 secret 直接拒绝启动）

## 6. WebSocket 安全

- 握手时校验 visitor_token / agent_token；过期立即拒绝
- 服务端盖时间戳（防客户端伪造 TS）
- 服务端盖发送者（`from = "visitor:<vid>"` 来自连接，不信任客户端）
- 心跳 30s，pongWait 70s；任何客户端 70s 不回 pong 即断开
- 单消息上限 64KB（防内存炸）

## 7. 文件上传

- 上限默认 20 MB（`BACKEND_MAX_UPLOAD_MB`），Nginx `client_max_body_size 25m`
- 嗅探 mime（`http.DetectContentType`），不信任客户端声明
- 类型白名单（图片 + 常见办公文档）；不在白名单 → 415
- 存储路径按 `2026/05/21/<uuid>.ext` 分桶，禁路径穿越
- 下载 endpoint 检查路径（拒 `..`）

## 8. 安全响应头

- `X-Content-Type-Options: nosniff`
- `X-Frame-Options: SAMEORIGIN`（admin 页不许被外部 iframe）
- `Referrer-Policy: strict-origin-when-cross-origin`
- `Strict-Transport-Security: max-age=31536000`（HTTPS 模式）

## 9. 审计

- 所有管理操作（登录、创建账号、禁用账号、关闭会话）落 `audit_logs` 表 + `audit.log` 文件
- 所有 HTTP 访问落 `business.log`
- 所有安全告警（限流、注入嫌疑、拉黑）落 `security.log`
- 所有 WSS 收发原始报文落 `raw_ws.log`

## 自检脚本

执行 `docs/security-selftest.sh`（见同目录）会自动跑下面所有用例并报告 PASS/FAIL。

## 红蓝对抗式自我验证（爷爷要求的「绕过」测试）

| 攻击向量 | 预期被拦截在 |
| --- | --- |
| `' OR 1=1 --` 注入 | DSN 关 interpolateParams + ? 占位符（攻击不会被拼进 SQL）|
| `<script>alert(1)</script>` 注入到聊天 | bluemonday 在服务端剥光；前端 escape 二次防御 |
| 单 IP 100 次/秒 调 /api/* | Nginx `limit_req` 限速 |
| 单 IP 100 次 WSS 握手 | Nginx `ws_rps` + Backend `IPWSHandshakePM` 双层 |
| 巨大文件上传（100 MB） | Nginx `client_max_body_size 25m` → 413 |
| 上传 .exe 伪装为 .png | `http.DetectContentType` 嗅探后拒绝 |
| 伪造 visitor_token | JWT HS256 校验失败 → 401 |
| 伪造 admin JWT (alg=none) | 服务端硬编码 `t.Method.Alg() != "HS256"` 拒绝 |
| 路径穿越 `/files/../../../etc/passwd` | handler 检查 `..` 直接 400 |
