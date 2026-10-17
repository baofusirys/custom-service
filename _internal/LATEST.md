### 当前版本：v0.9.1（已部署生产，含多人大厅）· App 3.2.0 (9) Flutter 版 · 2026-10-18（北京时间）

> 本文件是维护交接的「第一站」。看完这一份再去看 `_internal/CHANGELOG.md`，别凭印象答。

---

## 实际部署实例（爷爷自用，2026-07-06 核实）
> 排查 App/客服问题时看这里，别被下面的模板占位坐标误导。
- **生产（App 实际连的就是这台）**：`发卡密国内服 49.233.156.149`（MCP 配置 [3]，2026-10-14 核对）。同机跑 custom_service 全栈（cs-backend 等，镜像 `crpi-…aliyuncs.com/baofusir/cs-*:<IMAGE_TAG>`，版本由 `/fakami/.env` 与 `/srv/cs-data/.env` 的 `IMAGE_TAG` 控制，编排在 `/fakami/docker-compose.yml`）**＋ 发卡密 fakami 业务栈**。后端真实日志：`/srv/cs-data/logs/backend/{business,raw_ws,security,audit}.log`。
- **测试服（custom_service 当前已下线）**：`38.76.193.68`（MCP [5]，2026-10-14 核对）。2026-08-23 21:17 已恢复同机 `weixian-douxiaoyin`，其 7 个常驻服务全部 healthy；`custom_service` 容器数为 0，但 `cs_mysql_data`、`cs_redis_data`、证书卷和 `/srv/cs-data/` 均完整保留，可随时重新切回。公网 `https://maihaocs.icu/healthz` 返回 200。
- **App（Flutter，[115] 起）源码**：家中 Mac `/Users/chengmeiran/code/custom_service_flutter`（独立 git 仓库，无远端，初始快照 9180242）。构建机为 MCP 配置「Mac开发机-iOS构建」（192.168.3.6）。旧 SwiftUI 版目录已按爷爷要求删除，完整归档（含 git 历史，已校验可恢复）在 Mac `~/code/_archive/custom_service_swift-20261015-001756.tar.gz`。
  - 当前版本 3.2.0 (9)（唯一来源 `pubspec.yaml` 的 `version`），[119] 起有「大厅」页签（`lib/features/hall/`、`lib/ui/hall/`，依赖 livekit_client 2.13.1）；[118] 起界面按爷爷提供的设计稿改版（设计系统在 `lib/ui/kit/`）；分层 lib/core、domain、features、ui，组合根 `lib/app/app_model.dart`；后端写死 `lib/core/config.dart` 的 `productionBackendURL` = `https://k7m2x9p3.xn--5kroo.com`。包名 `com.chengmeiran.customservice` 与钥匙串条目不变（原生通道 `ios/Runner/AppDelegate.swift` 的 cs/keychain），从旧版升级免重新登录。
  - 测试：Mac 本机隔离测试环境 `~/cs-testenv`（只监听 127.0.0.1，凭证见其 `secrets/test.env`）；App 单测 `flutter test test`，模拟器端到端 `scripts/run_e2e.sh <标签>`（管理员 / 普通客服 / 大厅三轮，大厅轮需测试环境 LiveKit 已启动；测试后端地址仅模拟器生效）。测试环境 LiveKit：Mac `~/cs-testenv/livekit.sh`（随 `testenv.sh start` 启动，127.0.0.1:17880）。测试后端二进制用本仓库 `_internal/scripts/build-testenv-backend.ps1` 构建；接口/数据复位/造数/列表耗时/浏览器联调脚本在 `_internal/scripts/testenv/`。CocoaPods 的 WebRTC 二进制需经 Mac 本机代理下载（见 App 仓库 README）。
  - 签名：免费 Personal Team `4JZ8K387NU`（唯一来源 `ios/Runner.xcodeproj` 的 `DEVELOPMENT_TEAM`），描述文件 7 天有效；当前手机上是 3.2.0 (9)（装机序号 4092，2026-10-18 01:31 安装，已回读确认），有效期至 `2026-10-21T02:25:04+08:00`。
  - 自动续签：App 仓库 `scripts/auto_reinstall.sh` + launchd `com.chengmeiran.customservice.autoinstall`（每天 9/14/21 点，已改指向 Flutter 脚本），日志 Mac `~/Library/Logs/custom_service_app/auto_reinstall.log`（旧版历史在同目录 `auto_reinstall-swift-history.log`）；钥匙串口令见 Mac `~/.config/custom_service_swift/keychain_password`（600，不入库）。
  - 待办：轮换 Mac 登录密码（旧口令曾明文入 Mac 本地 git 历史）后同步更新口令文件。
- **发布到生产/下游**：v0.9.1 已推送 GitHub（标签 v0.9.1）并于 2026-10-18 01:13 部署生产（只更新 8 个 cs 容器含新增 cs-livekit，部署前手动备份 `/srv/cs-backup/manual/cs-before-0.9.1-20261018-005706.sql.gz`）；客服入口 `https://k7m2x9p3.卖号.com`（cs-nginx 经 fakami-caddy 反代，大厅信令 `/lk/rtc`）。生产实例为腾讯云轻量应用服务器 `lhins-rba8ijgo`（ap-beijing-6）。
  - **cs-livekit 特殊点**：阿里云 ACR 仓库 `baofusir/cs-livekit` 为私有，镜像在服务器本机构建，编排用独立变量 `CS_LIVEKIT_IMAGE_TAG`（/fakami/.env）+ `pull_policy: never`；只升级 IMAGE_TAG 不影响它。详见 `_internal/reports/hall-deploy.md` 第 10 节。
  - **媒体端口**：UDP 7882 / TCP 7881 已在轻量防火墙与宿主机 iptables `DOCKER-USER`（`/etc/iptables/rules.v4`，注释 cs-livekit-media）放行。coturn 端口同样被 DOCKER-USER 拦截（既有问题，待爷爷决定）。
  - **爷爷本地 fakami 项目需同步**：compose 的 cs-livekit 段与 cs-backend 3 个 LIVEKIT 变量、.env 的 IMAGE_TAG=0.9.1 与 CS_LIVEKIT_API_KEY / CS_LIVEKIT_API_SECRET / CS_LIVEKIT_IMAGE_TAG（值从服务器 /fakami/.env 取），否则下次部署 fakami 会把客服退回旧版或缺大厅。
- **待发布**：无。

## 当前部署坐标
> 部署到你自己服务器后，把下面占位换成你的实际值，方便后续 AI / 队友接手时一眼定位

- 服务器：`<你的服务器 IP>:22 / root`
- 远端代码目录：`/custom-service/`（或任意目录，与 docker-compose 上下文匹配即可）
- 远端数据目录：`/srv/cs-data/{logs,uploads,ssl}`（铁律：必须在代码仓库外，详见 [CLAUDE.md 数据安全铁律]）
- **远端 .env 路径**：`/srv/cs-data/.env`（[061] 起永久搬到仓库目录外，避免被 rsync/sftp 部署误删）
- **最近一次本地备份留存**：未记录；超过 7 天视为未完成，需补做并登记。
- **启动命令**：`cd /custom-service && docker compose --env-file /srv/cs-data/.env up -d --build`
- **国内镜像源（pull 加速，可选）**：`REGISTRY_BASE=crpi-saarj7fitzff243d.cn-zhangjiakou.personal.cr.aliyuncs.com/baofusir`（阿里云张家口个人版，镜像公开免登录；配 `docker-compose.production.yml` + `docker compose pull` 用。CI 已配 4 个 ALIYUN_* Secret 自动双推 GHCR+阿里云。详见 CHANGELOG [074][075]）
- 入口：
  - 管理后台 `https://<你的域名>/admin/`
  - Widget 演示 `https://<你的域名>/widget/demo.html`
  - 健康检查 `https://<你的域名>/api/health`
- 超管账号：首次启动时由 `.env` 的 `ADMIN_BOOTSTRAP_USERNAME` / `ADMIN_BOOTSTRAP_PASSWORD` 创建，**登录后第一件事改密**
- 状态：`docker compose ps` 应看到 backend / mysql / redis / admin / widget / nginx / coturn / livekit 全部 Up 且 healthy

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
| 服务总配置（端口/JWT/DB/Redis/CORS） | `.env`（部署时基于 `.env.example` 生成） | 根目录 |
| 全局时区 | `backend/internal/config/config.go` | `LoadTimezone()` |
| WSS 心跳/读写超时 | `backend/internal/ws/hub.go` | 文件顶部常量 |
| 消息 ACK/回执/离线补发 | `backend/internal/service/message_pipeline.go` | 整个模块 |
| Web 消息状态/缓存幂等合并 | `admin/src/modules/messageState.js` | 整个模块 |
| Web 多标签页状态同步 | `admin/src/modules/messageStatusSync.js` | 整个模块 |
| 限流参数（按 IP / 按访客） | `backend/internal/security/ratelimit.go` | 文件顶部常量 |
| 文件上传大小上限 | `backend/internal/config/config.go` | `MaxUploadSize` |
| 数据库自动迁移开关 | `backend/internal/db/migrate.go` | 启动时强制执行，无开关 |
| 安全补丁与验收报告 | `docs/security-release-0.7.4.md` | XSS/JWT/上传/SSRF/越权验收 |
| Widget 默认主题色 | `widget/src/config.ts` | `defaultTheme` |
| Nginx 限流 / 防 DDoS | `nginx/conf.d/default.conf` | `limit_req_zone` / `limit_conn_zone` 段 |
| WebRTC TURN/STUN（CoTURN）| `turn/turnserver.conf.tmpl` / `.env` 的 `TURN_*` | 端口 3478/5349 + relay 49152-49200 |
| TURN 短期凭证生成 | `backend/internal/service/turn.go` | HMAC-SHA1，24h TTL |
| 0.8.0 新功能接口契约 | `_internal/reports/api-0.8.0-features.md` | 全文 |
| 回收站保留期 / 撤回时限 | `backend/internal/store/features_deletion.go` / `features_messages.go` | `RecycleRetention` / `RecallWindow` |
| 自动清理执行时间与任务锁 | `backend/internal/service/features.go` | `autoCleanHour` / `StartMaintenance` |
| 会话列表两阶段查询与排序口径 | `backend/internal/store/conversation_list.go` | 文件头注释 |
| 会话已读权限（与查看口径一致） | `backend/internal/store/store.go` | `AgentCanReadConversation` |
| 客服消息投递会话校验（防串台） | `backend/internal/ws/hub_features.go` | `bindAgentConv` |
| 接管规则（他人在线只读 / 离线可接管） | `backend/internal/handler/http.go` | `AssignSelf` |
| 多人大厅四端契约 / 生产接入 | `_internal/reports/hall-contract.md` / `hall-deploy.md` | 全文 |
| 大厅开关与上限（视频、开麦、摄像头、人数） | 后台「系统设置 → 多人大厅」；校验在 `backend/internal/hall/settings.go` | `hall_*` 五个设置键 |
| LiveKit 服务配置与入口校验 | `livekit/livekit.yaml` / `livekit/entrypoint.sh` | 全文 |
| 公共限流（固定窗口） | `backend/internal/security/ratelimit.go` | `allow` |

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
- **[119] 2026-10-18（北京时间）多人大厅上线（v0.9.1、App 3.2.0(9)）**：自托管 LiveKit；访客进站自动入厅、默认关麦能听；客服管理；视频开关；公共限流固定窗口修复；生产外网实测语音互通。
- **[118] 2026-10-15（北京时间）App 3.1.0(8) 按新设计稿改版全部界面**：只改界面、功能不变，设计稿里本 App 没有的功能不加；新图标；真机已安装。
- **[117] 2026-10-15（北京时间）修复客服消息串台**：服务端按客户端声明的会话投递并逐条校验、绝不改投；他人在线接待只读、离线可接管；App 3.0.1、网页后台同步；后端 0.8.2。

## 维护交接阅读顺序
1. 本文件（`_internal/LATEST.md`）
2. `_internal/CHANGELOG.md` 最近 5~10 条（更早条目见 `_internal/CHANGELOG-archive/`）
3. 用户问到的模块的 `README.md`（每个子模块都有）
4. 真正动到的代码
