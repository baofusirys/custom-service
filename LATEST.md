### 当前版本：v0.7.1 · 2026-07-06

> 本文件是 AI 接手项目时的「第一站」。看完这一份再去看 CHANGELOG，别凭印象答。

---

## 实际部署实例（爷爷自用，2026-07-06 核实）
> 排查 App/客服问题时看这里，别被下面的模板占位坐标误导。
- **生产（App 实际连的就是这台）**：`发卡密国内服 49.233.156.149`（MCP 配置 [8]）。同机跑 custom_service 全栈（cs-backend 等，镜像 `crpi-…aliyuncs.com/baofusir/cs-*:latest`）**＋ 发卡密 fakami 业务栈**。后端真实日志：`/srv/cs-data/logs/backend/{business,raw_ws,security,audit}.log`。
- **测试服（当前已启动，无真实流量）**：`38.76.193.68`（MCP [10]）。2026-08-22 临时停止同机 `weixian-douxiaoyin` 后，已用 `/custom-service` 现有代码执行 `docker compose --env-file /srv/cs-data/.env up -d --build`；外网 `/api/health` 返回 200，运行版本为 `v0.7.0`（落后本地 `v0.7.1`，未上传覆盖本地代码）。
- **App(SwiftUI) 源码**：在 Mac `192.168.1.75`（MCP [18]）`~/code/custom_service_swift`（独立 git 仓库；本 Windows 仓库里的 `mobile_app` 是已存档旧 Flutter 版，[089] 起弃用）。构建装机走 `auto_reinstall.sh` / launchd。
- **后端升级到该生产**：`git push`（触发 CI 出 `:latest` 镜像）→ 该服务器 `cd <cs 代码目录> && docker compose pull cs-backend && docker compose up -d cs-backend`（只动 cs-backend，不碰 fakami）。

## 当前部署坐标
> 部署到你自己服务器后，把下面占位换成你的实际值，方便后续 AI / 队友接手时一眼定位

- 服务器：`<你的服务器 IP>:22 / root`
- 远端代码目录：`/custom-service/`（或任意目录，与 docker-compose 上下文匹配即可）
- 远端数据目录：`/srv/cs-data/{logs,uploads,ssl}`（铁律：必须在代码仓库外，详见 [CLAUDE.md 数据安全铁律]）
- **远端 .env 路径**：`/srv/cs-data/.env`（[061] 起永久搬到仓库目录外，避免被 rsync/sftp 部署误删）
- **启动命令**：`cd /custom-service && docker compose --env-file /srv/cs-data/.env up -d --build`
- **国内镜像源（pull 加速，可选）**：`REGISTRY_BASE=crpi-saarj7fitzff243d.cn-zhangjiakou.personal.cr.aliyuncs.com/baofusir`（阿里云张家口个人版，镜像公开免登录；配 `docker-compose.production.yml` + `docker compose pull` 用。CI 已配 4 个 ALIYUN_* Secret 自动双推 GHCR+阿里云。详见 CHANGELOG [074][075]）
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

## 最近重大改动摘要（倒序，最新在上）
- **[097] 2026-08-22 运维切换**：测试服 `38.76.193.68` 已安全 down 同机 `weixian-douxiaoyin`（未带 `-v`，命名卷保留），随后构建启动 `custom-service`；7 个服务均 Up，后端/MySQL/Redis healthy，内外网健康接口均 200。服务器现有代码为 v0.7.0，与本地 v0.7.1 有版本差异。
- **[096] 2026-07-06 v0.7.1**：修复 App 发消息转圈后「未送达」。根因：WSS 重连产生的新连接后端 `c.ConvID` 为空，聊天界面没对新连接重新 `/assign`，发消息命中后端空 ConvID 分支被拒（只回 error 不回 ack）→ App 干等 12s 标红。生产日志（发卡密国内服）14:03/14:04 两条 `agent_msg_no_conv` 坐实。双保险：① 后端 hub.go/service.go 自愈（agent chat 带 conv 且 `AgentOwnsConv` 校验归属通过就补 attach，伪造被 SQL 挡，不破坏 [077]/[068] 防串台）；② App（Mac a2e532a）onAlive 重连先重挂会话再重发 + 处理 error 自愈 + onForeground 1.5s 防抖。后端需 pull 重部署、App 需重装。
- **[095] 2026-06-18 v0.7.0**：修复从通知进入 App 立即发消息转圈（仅 App）。根因 WSS 后台挂起「假活」（wsAlive 仍 true）。改 WSManager.start 幂等强制新鲜连接 + App.swift scenePhase/onOpenURL → onForeground 强制重连 + Store.onForeground 对账。Mac 1a3635f。

## AI 接手必读顺序
1. 本文件（LATEST.md）
2. `CHANGELOG.md` 最近 5~10 条
3. 用户问到的模块的 `README.md`（每个子模块都有）
4. 真正动到的代码
