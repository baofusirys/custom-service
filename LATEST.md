### 当前版本：v0.6.1 · 2026-05-27

> 本文件是 AI 接手项目时的「第一站」。看完这一份再去看 CHANGELOG，别凭印象答。

---

## 当前部署坐标
> 部署到你自己服务器后，把下面占位换成你的实际值，方便后续 AI / 队友接手时一眼定位

- 服务器：`<你的服务器 IP>:22 / root`
- 远端代码目录：`/custom-service/`（或任意目录，与 docker-compose 上下文匹配即可）
- 远端数据目录：`/srv/cs-data/{logs,uploads,ssl}`（铁律：必须在代码仓库外，详见 [CLAUDE.md 数据安全铁律]）
- **远端 .env 路径**：`/srv/cs-data/.env`（[061] 起永久搬到仓库目录外，避免被 rsync/sftp 部署误删）
- **启动命令**：`cd /custom-service && docker compose --env-file /srv/cs-data/.env up -d --build`
- 入口：
  - 管理后台 `https://<你的域名>/admin/`
  - Widget 演示 `https://<你的域名>/widget/demo.html`
  - 健康检查 `https://<你的域名>/api/health`
- 超管账号：首次启动时由 `.env` 的 `ADMIN_BOOTSTRAP_USERNAME` / `ADMIN_BOOTSTRAP_PASSWORD` 创建，**登录后第一件事改密**
- 状态：`docker compose ps` 应看到 backend / mysql / redis / admin / widget / nginx / coturn 全部 Up；mysql / redis / backend 应为 healthy

---

## 一句话介绍
一套企业级、可嵌入任何网页的自托管在线客服系统。访客端是一段 JS（<script src> 引入即用，iframe 隔离，不污染宿主页样式），客服后台是 Vue 3 + Element Plus，后端是 Go + WebSocket，单机即可承载万级并发长连接。

## 最新代码在哪个目录
- 本地开发：你的本地 git clone 目录
- 服务器上：`/custom-service/`（或你自己选的目录，部署时 rsync/sftp 全量同步到这里）

## 过期 / 归档目录
- 暂无（v0.1.0 是首版）

## 关键开关位置
| 用途 | 文件 | 位置 |
| --- | --- | --- |
| 服务总配置（端口/JWT/DB/Redis） | `.env`（部署时基于 `.env.example` 生成） | 根目录 |
| 全局时区 | `backend/internal/config/config.go` | `LoadTimezone()` |
| WSS 心跳/读写超时 | `backend/internal/ws/hub.go` | 文件顶部常量 |
| 限流参数（按 IP / 按访客） | `backend/internal/security/ratelimit.go` | 文件顶部常量 |
| 文件上传大小上限 | `backend/internal/config/config.go` | `MaxUploadSize` |
| 数据库自动迁移开关 | `backend/internal/db/migrate.go` | 启动时强制执行，无开关 |
| Widget 默认主题色 | `widget/src/config.ts` | `defaultTheme` |
| Nginx 限流 / 防 DDoS | `nginx/conf.d/default.conf` | `limit_req_zone` / `limit_conn_zone` 段 |
| WebRTC TURN/STUN（CoTURN）| `turn/turnserver.conf.tmpl` / `.env` 的 `TURN_*` | 端口 3478/5349 + relay 49152-49200 |
| TURN 短期凭证生成 | `backend/internal/service/turn.go` | HMAC-SHA1，24h TTL |

## 部署坐标
- 部署方式：把整个仓库目录 rsync/sftp 上传到服务器后，进入目录执行 **`docker compose up -d --build`**，一条命令完成。
- 默认开放端口：
  - `80/443` → Nginx 入口（HTTP 自动 301 跳 HTTPS，WSS 走 443）
  - 其余服务一律不对外，仅在 docker 内网通信
- 数据卷（**严禁动**）：
  - `cs_mysql_data`（named volume，MySQL 数据）
  - `cs_redis_data`（named volume，Redis AOF）
- 宿主机绑定目录（**仓库目录外，不会被部署清空**）：
  - `/srv/cs-data/logs/`（所有模块日志，长效存储）
  - `/srv/cs-data/uploads/`（访客/客服上传的图片、文件）
- 管理后台入口：`https://<your-domain>/admin/`
- 默认超管账号：首次启动从 `.env` 的 `ADMIN_BOOTSTRAP_USERNAME` / `ADMIN_BOOTSTRAP_PASSWORD` 创建；首次登录后必须改密。

## 集成方（别人嵌入自己网站）怎么用
一行代码搞定，详见 `docs/INTEGRATION.md`：
```html
<script src="https://<your-domain>/widget/loader.js"
        data-cs-endpoint="wss://<your-domain>/ws"
        data-cs-site="default" defer></script>
```

## 最近 3 次重大改动摘要
- **[065] 2026-05-27 v0.6.1**：修复 admin 未读 badge=2 但实际只有 1 条访客消息的虚高问题 + 新增「已联系」过滤 tab。根因：`store.InsertMessage` 默认把所有非 agent 消息（包含 sys 类型的 page_navigation / voice_call 状态事件）都累加到 `unread_agent`，导致客服看到 badge 数字比真实访客消息多。改动：① `backend/internal/store/store.go InsertMessage` 末尾改成 switch m.Sender 显式三分支（visitor / agent / sys / default），sys 只刷 updated_at 不算未读；② 新建 `backend/migrations/006_recalibrate_unread_agent.sql` 用 LEFT JOIN 子查询按 last_read_agent_at 之后的真实 visitor 消息数校准历史数据，启动时自动跑；③ `ListOpenConversations` SELECT 加 EXISTS 子查询返 `has_visitor_msg` 字段透传到 `/api/agent/conversations` JSON；④ `admin/src/views/Console.vue` 加 filterMode ref + 「全部 / 已联系」segmented el-radio-group + computed filteredConvs 客户端过滤 0 网络请求；WSS 收 fromVisitor chat 时实时 set has_visitor_msg=true。边界：部署后 badge 可能从虚高 5 突降到真实 1，正常修正。
- **[064] 2026-05-27 v0.6.0**：解决集成方 fakami 商城反馈 [068] iOS 客服 App 12h token 过期后 401 死循环 7 小时不停的严重问题。同模式 [054]/[058] 的 token 续期版。改动：① backend 新增 `/api/agent/login/refresh` endpoint（grace 24h，agent 仍 active 校验，audit log）+ `security.ParseAgentTokenAllowExpired` + 区分 `ErrTokenExpired/ErrTokenInvalid/ErrTokenMalformed` 错误类型，middleware/AgentAuth/AgentWS handler 返回 code=40102（expired）/40103（invalid）让客户端区分；② mobile_app `http_client.dart` 加 Dio 401 interceptor + refresh lock + authFailedStream；③ `ws_client.dart` token 改可变 + 连接前主动检查 exp < 5min refresh + isConnecting 锁；④ `app_state.dart` 订阅 authFailedStream 自动 logout 走 Consumer 路由跳 LoginPage；⑤ admin Vue `http.js` / `ws.js` 同样模式 401 interceptor + refresh。
- **[063] 2026-05-26 v0.5.1**：admin 工作台点击会话「标记已读慢 2-3 秒」体感优化（乐观 UI + 并行 RPC）。

## AI 接手必读顺序
1. 本文件（LATEST.md）
2. `CHANGELOG.md` 最近 5~10 条
3. 用户问到的模块的 `README.md`（每个子模块都有）
4. 真正动到的代码
