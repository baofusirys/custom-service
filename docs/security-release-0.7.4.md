# v0.7.4 客服系统安全补丁报告

发布日期：2026-10-05（北京时间）

## 影响范围与修复版本

- 受影响版本：v0.7.3 及更早版本。风险包括恶意 HTML/XSS、WebSocket query JWT 泄露、全开放 CORS、附件类型伪装、未授权文件读取、会话无法立即吊销及部分会话对象越权。
- 修复版本：v0.7.4。
- 本补丁只在当前仓库和本地隔离进程中验证，未连接用户正在运行其他项目的服务器，也未执行远程 Docker、数据库或生产破坏性测试。

## 补丁内容

1. 后端消息、附件名、媒体 URL 和页面地址统一清洗；前端使用文本节点/Vue 插值，危险页面地址不再生成链接。
2. 上传同时校验扩展名、服务端嗅探 MIME 与 magic bytes；HTML、SVG、脚本扩展名不在白名单。历史文件下载加 `nosniff`，非图片统一 `Content-Disposition: attachment`。
3. 文件下载需要短期能力令牌或有效 JWT，并校验文件所属会话和客服账号；路径穿越被拒绝。
4. WebSocket 禁止 `?token=`，浏览器使用 `Sec-WebSocket-Protocol`，原生客户端可用 `Authorization`；Origin 使用明确白名单。
5. 客服 JWT 改为 30 分钟有效期，加入账号 `token_version`。退出、改密、禁用账号或应急递增版本后，旧令牌立即失效；刷新同样校验版本。
6. 登录增加 IP/账号/令牌维度限流，连续失败 5 次锁定 15 分钟，失败 3 次后要求本地 SVG 验证码；关键事件写入安全/审计日志并脱敏。
7. 会话读取、接管、关闭和上传重新执行服务端对象权限检查；CORS 仅接受 `.env` 的明确来源，禁止 `*` 和任意 Origin 反射。
8. 页面、回调和站点 URL 拒绝 `javascript:`、回环、私网、链路本地、组播、CGNAT 和云元数据地址；静态站点禁止通过 SPA fallback 返回源码、Dockerfile、转储和备份文件。
9. 管理后台浏览器不再把 JWT 写入 `localStorage`，登录 Cookie 使用 `HttpOnly` 和 `SameSite=Strict`；移动端保留短期内存令牌兼容。

## 数据库迁移与回滚

- `backend/migrations/011_security_hardening.sql`：为 `agents` 增加 `token_version`，为 `files` 增加 `access_token_hash` 及索引。迁移仍由服务启动自动执行。
- `backend/migrations/down/011_security_hardening.sql`：对应回滚脚本。执行前必须恢复迁移前备份，并确认旧代码已停止；本地补丁未在生产库执行回滚。
- 这是加法迁移，不删除业务数据；现有文件的 `access_token_hash` 为空时仍可使用已鉴权 JWT 访问，新的上传使用随机能力令牌。

## 本地安全验收

以下检查在 Windows PowerShell 7（pwsh）当前仓库完成：

- Go：`go test ./...`、`go vet ./...` 通过。
- Go 依赖：使用 Go 1.26.7 执行 `govulncheck ./...`，可达代码 0 个漏洞；未调用路径仍有上游模块公告，保留在扫描原始输出中。
- Node 依赖：升级 Admin 的 axios 到 1.20.0，`npm audit --omit=dev` 为 0 vulnerabilities。
- Admin：`npm test`、`npm run build` 通过。
- Widget：现有 Node 测试通过。
- XSS：`<img src=x onerror=alert(1)>`、事件属性、危险 SVG 被清洗或拒绝；消息展示不使用 `v-html`。
- Widget：`chat.html` 的应用逻辑已拆到同源 `chat.js`，入口不再含内联脚本，CSP 可保持 `script-src 'self'`。
- 伪装图片：`.jpg` + 非 JPEG 文件头被拒；真实 JPEG 通过；`.html/.svg` 被拒。
- WebSocket：带 `?token=` 返回 400；子协议令牌路径不进入 URL；raw WSS 日志对 token、密码、Cookie 和文件能力令牌脱敏。
- CORS：未配置明确来源或配置 `*` 时启动即拒绝；不匹配的来源不返回 `Access-Control-Allow-Origin`。
- 越权：未接管或他人接管的会话读取、接管、关闭、上传返回 403；文件下载必须匹配会话/账号。
- SSRF：回环、私网、169.254.169.254、localhost、非 HTTP(S) 地址被拒。
- 吊销：退出、改密、禁用账号递增 `token_version`；旧 JWT 在 API、刷新和 WSS 入口均返回会话失效。
- 敏感静态文件：SPA fallback 对 `.env`、`requirements.txt`、`Dockerfile`、SQL/备份压缩包返回 404。

服务器验收（容器健康、真实 HTTPS/WSS、Nginx 返回头、迁移日志、备份恢复和重启演练）本次未执行，原因是该服务器当前用于其他项目；上线前应在独立临时 Compose/临时数据库环境执行同一验收清单。

## 令牌泄露应急方案

1. 立即停止暴露入口或切换维护页，保留脱敏后的安全日志和 trace_id。
2. 在数据库事务中递增受影响账号的 `token_version`；若无法确定账号，轮换 `.env` 的 `JWT_SECRET`，使全部 JWT 失效，然后按固定三步重新部署。
3. 轮换 `DATA_AES_KEY`、Redis/MySQL/SMTP/TURN 等可能一并暴露的凭证；旧凭证先在签发端作废，再写入 `server/.env`，不得写入 Markdown、Git 或日志。
4. 管理后台浏览器令牌只保存在 `HttpOnly; SameSite=Strict` 的 `cs_session` Cookie；移动端仍使用短期内存令牌。清理 Cookie、WSS 连接和缓存的文件能力 URL，强制重新登录；核对登录、吊销、上传和设置变更审计事件。
6. 在隔离环境用本报告的验收用例复测；确认全部旧令牌返回 401 后，才恢复公网入口。
