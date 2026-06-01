### 当前版本：v0.6.5 · 2026-06-01

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
- **[069] 2026-06-01 v0.6.5**：iOS 客服 App 接听后 17s 无声 → 修复 race / 异常静默 + backend 5s 看门狗 + voice_finished reason。起因：集成方 [073] 工单 iOS 客服接听后浮窗显示「通话中」但访客完全听不到客服，访客 17s 后 ICE 超时强断、客服端永不弹错。根因三层叠加：① mobile `_onOffer` `setRemoteDescription / createAnswer / setLocalDescription` 三步同一 try/catch 异常静默；② APNs 冷启 race：offer 先到、`_pc==null`、`_onIce` candidate 直接 drop → DTLS 永远握不上；③ accept() mic preflight 缺失，后端从未感知失败。改动 3 个文件 5 patch：① mobile `voice_controller.dart` Patch 1 三阶段独立 try/catch + sdp 空值校验 + `voice_signal_error{phase, reason, call_id, agent_id}` 上报；② Patch 2 accept() mic preflight + `_classifyMicError` 归一 5 种 reason + `voice_accept_failed` 上报；③ Patch 4 新增 `_prepareForIncomingCall` + `_pcReady` + `_earlyIceQueue` 缓存早到 ICE candidate + setRemote 后 flush + 所有 `await _pc!.xxx` 全包独立 try/catch；④ backend `hub.go` Patch 3 加 `pendingAccepts/acceptTimers sync.Map` + 5s 看门狗 + `fireAcceptWatchdog` LoadAndDelete 原子 dedup + voice_answer/end/reject 取消看门狗；⑤ `service.go` Patch 5 `codeToText(code, reason, durSec)` 优先按 reason 渲染 9 种中文 + `OnVoiceCallFinished` 签名加 reason + SenderRef 升级为 `voice:reason` 形式。验证：go build/vet PASS；真机三脚本：mic 关 / kill App 接听 / 故意丢 answer。
- **[068] 2026-06-01 v0.6.4**：修复客服发消息串台严重 bug（敏感账密泄露给陌生访客）。起因：爷爷截图反馈客服在 A 会话输了账密草稿未发 → 切到 B 会话 textarea 仍留 A 草稿 → 误按回车发给 B → critical 数据泄露。根因：`admin/src/views/Console.vue` 的 `draft = ref('')` 是全局单例 + pickConv 切会话不清 draft + sendText 用 `activeConv.value.id` 实时读存在 race。改动：① admin Console.vue 改成 `drafts = ref({})` per-conv 字典 + 新 computed `currentDraft/currentPendingFiles` + sendText 入口 `const sendingConvId = activeConv.value?.id` snapshot 锁定 + ws.send / messages.push 守卫全用 snapshot + uploadAndSendFile 改签名 (file, convId) + addPendingFile/removePendingFile/clearPendingFor 全部 per-conv（blob URL revoke 防内存泄漏）+ pickFile/onPasteDraft 入口 snapshot；② `mobile_app/lib/state/app_state.dart` sendChat/uploadAndSendFile 入口加 `convIdSnap/textSnap` snapshot + 乐观渲染 `if (activeConv?.id == convIdSnap)` 守卫范式对齐 admin（mobile 因 ChatPage push 隔离原本不串台，仅做防御性硬化）。TODO：backend `hub.go case "chat"` agent 路径需加 `agentInConv(c.ID, e.ConvID)` 校验防恶意客户端伪造 conv_id。
- **[067] 2026-06-01 v0.6.3**：「已联系」口径收紧——voice 通话事件不再算主动联系。起因：[065] 把 sys voice 事件也算 has_visitor_msg=true，导致访客只点来电立即挂掉也被误判「已联系」。改动 3 端齐改：① `backend/internal/store/store.go` ListOpenConversations EXISTS 子句去掉 `OR (m.sender='sys' AND m.sender_ref='voice')`，只保留 `m.sender='visitor'`；② `admin/src/views/Console.vue` isContacted 去掉 `unread>0` 兜底，严格只信 `has_visitor_msg`；③ `mobile_app/lib/api/models.dart` Conversation.isContacted getter 同步去 unread 兜底；④ `mobile_app/lib/state/app_state.dart` WSS onMessage 删除 voice sys → hasVisitorMsg 翻牌逻辑（voice 仍刷新预览/排序，只是不算已联系）。验证：访客只点接听立即挂掉 → 三端 isContacted=false；真发文字 → 三端立刻 true。

## AI 接手必读顺序
1. 本文件（LATEST.md）
2. `CHANGELOG.md` 最近 5~10 条
3. 用户问到的模块的 `README.md`（每个子模块都有）
4. 真正动到的代码
