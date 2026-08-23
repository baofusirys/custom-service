# CHANGELOG

> 每次代码改动完成后，必须立刻在文件顶部追加一条。时间用北京时间绝对格式（YYYY-MM-DD HH:mm）。

---

## [103] 2026-08-23 11:09 — v0.7.3 全量部署测试服并完成重连与多连接验收

**起因 / 需求**：将 [102] 的下游消息状态修复实际部署，验证别名 ACK、重连补发和多连接同步不是只在单元测试中成立。

**做了什么**：确认数据均位于 named volume 或 `/srv/cs-data/` 后，清空并全量上传代码，仅执行 `docker compose up -d --build`；通过公网 TLS 建立访客与三个客服连接，覆盖首次发送、delivery/read、断线重连及同 ID 重发。

**验证 / 注意**：版本 0.7.3、7 服务 Up、核心服务 healthy、三入口 200、日志 ERROR=0；首次 ACK 11.62ms、重连幂等 ACK 7.71ms、双连接均收到回执、数据库仅 1 行；非法 `client_id` 返回 error 且落库 0 行。仅测试服部署，v0.7.3 未打 tag、未上传镜像、生产服未更新。

## [102] 2026-08-23 11:00 — 修复离线重发 ID 归并与多连接消息状态同步 · v0.7.3

**起因 / 需求**：下游确认 `local-*` 离线消息重连补发后已在约 9ms 落库并收到 `persisted` ACK，但旧气泡因 ID 失配永久停在“发送中”，多连接状态也会分叉。

**做了什么**：建立 `local-* ↔ m2-*` 持久别名与唯一 ACK reducer，HTTP/WSS/outbox 一对一幂等归并；重连改为 hello 后先 HTTP 对账再限频补发；新增同账号多连接 ACK/送达广播及浏览器 BroadcastChannel + storage 双通道状态同步。

**验证 / 注意**：修复前新增用例因缺少 alias reducer 直接失败；修复后管理端 10 项测试、生产构建、Go 全量测试/vet、Hub 100 轮并发重复测试、Widget 4 项测试、生产 npm audit 均通过。无 schema 迁移；v0.7.3 尚未部署测试服、未创建 tag、未上传镜像。

## [101] 2026-08-22 19:08 — v0.7.2 七镜像完成 GHCR 与阿里云 ACR 双源发布

**起因 / 需求**：爷爷明确授权正式推送，并要求生成可直接交给下游执行的版本升级通知。

**做了什么**：原子推送 main 与不可变 `v0.7.2` tag；确认 GHCR 7 个 `0.7.2` 镜像已生成；修复 ACR attestation 兼容问题后，从 GHCR 向 ACR 提升同版本 amd64 镜像。

**验证 / 注意**：ACR 提升 run `32569490774` 为 7/7 success，每项均核对源/目标 image ID 一致；生产服与下游尚未拉取，需将 `IMAGE_TAG=0.7.2` 后按既有 compose 流程更新，禁止使用 `latest`。

## [100] 2026-08-22 19:03 — 修复阿里云 ACR 拒绝 OCI provenance 导致版本发布失败

**起因 / 需求**：`v0.7.2` tag 与 GHCR 镜像已发布，但 7 个 CI job 在推送阿里云 ACR 时均被 `unknown manifest class for application/vnd.oci.empty.v1+json` 拒绝。

**做了什么**：Buildx 明确关闭 ACR 不兼容的 provenance/SBOM 附加清单；新增独立、参数校验、7 镜像并行的 ACR 提升流水线，从不可变 GHCR 版本复制 amd64 镜像并核对 image ID，不移动既有 Git tag。

**注意事项 / 遗留**：应用代码与 `v0.7.2` 内容未变化；须推送本提交、等待 main CI 成功，再手动触发 `Promote Version to Aliyun ACR` 输入 `0.7.2`，以实际 7/7 成功为发布完成标准。

## [099] 2026-08-22 18:52 — v0.7.2 全量部署测试服并完成真实消息链路验收

**起因 / 需求**：将 [098] 修复实际部署到测试服务器，证明落库后 ACK、幂等重发与页面入口不是只在单元测试中成立。

**做了什么**：清空并全量上传代码后仅执行 `docker compose up -d --build`；没有 `down`、没有触碰命名卷或 `/srv` 数据；完成公网入口、日志脱敏、容器/数据库/宿主机时区及 WSS 重发链路验收。

**验证**：7 服务均 Up，backend/MySQL/Redis healthy；健康接口、后台、Widget 均 200；经 `maihaocs.icu` 公网 TLS/WSS 验证首次 ACK=`persisted`、同 ID 第二次 ACK=`duplicate=true`、数据库 `COUNT(*)=1`；Go/Vue/Widget 自动化测试已在 [098] 通过。

**注意事项 / 遗留**：未发布 GitHub/GHCR/阿里云 ACR；远端 `govulncheck` 隔离容器首轮 768MB OOM、第二轮未形成结论，保留 [098] 本地扫描结论。现有 compose 仍缺 stdout 轮转、资源上限与 `no-new-privileges`，Coturn 使用 host/root，宿主机公网监听 3306/37425，CSP 缺失；测试触发两条 APNs 过期令牌 410 警告，均需独立加固。

## [098] 2026-08-22 18:16 — 修复 Web 消息延迟与虚假状态，建立可验证的可靠投递协议 · v0.7.2

**起因 / 需求**：诊断确认访客缓存键迁移遗漏导致 HTTP 新消息不刷新，并存在 ACK 早于落库、`delivered_ws` 固定真、逐条已读缺失、离线无补发及 WSS JWT 进入长期日志等问题。

**做了什么**：统一访客维度缓存与持久 outbox；新增密码学稳定 ID、事务幂等落库后 ACK、有界并发、单调 persisted/delivered/read、回执归属校验、离线补发与去重；Nginx/后端日志脱敏并统一 `+08:00` JSON Lines；生产镜像改用 `IMAGE_TAG=0.7.2` 明确版本。

**安全 / 依赖**：参数化 SQL 与跨会话伪造回执测试通过；生产 npm 依赖审计为 0，高危 Go 模块已升级，构建器锁定 Go 1.26.7；本机 Go 1.26.5 扫描只剩标准库项，部署后须在固定构建器内复扫。Vite 开发依赖仍有 moderate 公告，修复需破坏性升 Vite 8，生产依赖不受影响。

**注意事项 / 遗留**：无 schema 迁移；测试服尚待全量部署和真实 WSS/日志验收；本次未上传 GitHub/GHCR/阿里云 ACR，发布版本 tag 必须另获爷爷明确同意。

---

## [097] 2026-08-22 17:18 — 测试服务器切换运行项目并完成真实验收 · v0.7.1

**起因 / 需求**：爷爷准备复现并反馈 Bug，需要确认测试服实际运行内容，并将同机其他项目停下后启动 `custom-service`。

**做了什么**：确认 `weixian-douxiaoyin` 占用 80/443 后，以不带 `-v` 的 `docker compose down` 安全停止并保留全部命名卷；随后在 `/custom-service` 使用外置 `/srv/cs-data/.env` 执行 `docker compose up -d --build`。7 个客服系统服务均 Up，后端/MySQL/Redis healthy，HTTP 301 跳 HTTPS，内外网 `/api/health` 均返回 200；宿主机、容器、数据库时间一致为北京时间。

**注意事项 / 遗留**：测试服现有代码返回 `v0.7.0`，落后本地 `v0.7.1`，本次未上传覆盖代码；现有远端 Compose 缺少资源上限、stdout 日志轮转和 `no-new-privileges`，宿主机 MySQL 仍公网监听 3306，后续需单独加固。本次只做运行状态切换，未修改业务代码与持久化数据。

---

## [096] 2026-07-06 14:40 — 修复 App 发消息转圈后「未送达」：重连后会话没重挂 → App+后端双保险自愈 · v0.7.1

**起因**：爷爷反馈 App 每次发消息都卡很久、最后「未送达·重发」。查生产日志坐实（App 实际连的是**发卡密国内服 49.233.156.149** 的 cs-backend，不是 38.76.193.68 测试服）：14:03、14:04 两条 `agent_msg_no_conv`，前后 22s 内该 agent 连了 10 条 WSS。

**根因**：WSS 断线重连（[095] 回前台/点通知强制重连）产生「全新连接」，后端 `c.ConvID` 为空；聊天界面虽开着却没对新连接重新 `/assign`，发消息命中 `PreprocessAgentMessage` 空 ConvID 分支被拒——后端只回 `error` 不回 `ack`，App 干等 12s ackTimeout 才标红；且 App 从不处理 `error`。附带：`onOpenURL`+`scenePhase.active` 双触发导致回前台重连风暴。

**做了什么**：① 后端 hub.go/service.go 新增自愈——agent chat 若 `c.ConvID` 空但带了 `conv` 且 `AgentOwnsConv` 校验归属通过，补 attach（伪造/越权被 SQL 挡，不破坏 [077]/[068] 防串台）；② App（Mac 仓库 a2e532a）onAlive 重连先 `reattachActiveConv`(/assign) 再重发、onEnvelope 处理 `error` 做 3s 节流自愈、onForeground 加 1.5s 防抖。

**验证/注意**：后端 golang:1.22 容器 `go build`+`go vet` PASS；App 模拟器 `xcodebuild` BUILD SUCCEEDED。**遗留动作**：后端要 push→CI 出镜像→在发卡密国内服 `docker compose pull && up -d`（见回复升级文案）；App 待下次自动/手动重装生效。两端各自单独就能修好，一起上=双保险。

---

## [095] 2026-06-18 21:39 — 修复从通知进入 App 立即发消息转圈：回前台强制重连 WSS 解决「假活」（仅 App）· v0.7.0

**起因 / 需求**

爷爷反馈 App bug：消息推送到通知助手 → 点通知进 App → **立即点那个客户进聊天发消息 → 100% 转圈发不出去**；但返回列表、再点一次同一客户进聊天，就能发了。

**根因（WSS 连接"假活"）**

手机在后台时，WSS 长连接被 iOS 系统挂起/断开，但客户端这边没察觉、**仍以为连着**（`wsAlive=true`，"假活"）。从通知唤醒后立即发消息 → 消息发进这个假活连接的黑洞 → 服务器收不到 → 没 ACK 回来 → **一直转圈**，要等心跳（30s）超时才发现断了去重连。"返回再点一次"时往往已经重连好了，所以能发。

代码层：`App.swift` 的 `onOpenURL` 是**空实现**；回前台只有 `if !wsAlive` 才重连，假活时 `wsAlive` 仍 true 所以**不重连**。

**改了什么（App 3 文件，Mac 1a3635f）**

- `WSManager.start`：先 `cancel` 旧 task + `invalidate` ping 再连——让 `start` 幂等且**强制新鲜连接**，不信任后台挂起的假活 socket。
- `App.swift`：**App 级** `scenePhase active` + `onOpenURL` → `store.onForeground()`（放 App 顶层，在聊天界面回前台也能覆盖）。
- `Store.swift`：新增 `onForeground` = 强制重连 WSS（start 先断旧）+ `refreshLatest` 对账拉最新。

配合 [084]：重连窗口（未 ready）发消息 `sendDict` 返 false 标红，ready 后 `resendPending` 自动重发。

**业务流程对比**

- 改动前：从通知进入 → 立即发消息 → 进假活连接黑洞 → 转圈最多 30 秒发不出。
- 改动后：回前台/从通知进入 → 强制断旧重连 → 几百毫秒 ready 后正常发（或短暂标红后自动重发），不再卡。

**触发场景与边界 + 验证**

- 触发：点推送通知进 App、App 回前台 → 强制重连 WSS（不论 wsAlive 真假）+ 对账。
- 边界：start 幂等（每次先 cancel 旧 task，不累积多连接）；重连窗口发消息靠 [084] 标红 + 自动重发兜底。
- 验证：`xcodebuild BUILD SUCCEEDED` + 装机 seq 3772；爷爷真机实测从通知进入立即发消息。仅 App，后端/Web/服务器零变更。

---

## [094] 2026-06-16 10:09 — App 7天免费证书自动重签装机：Mac launchd 每天高频重试（仅 App 构建环境）· v0.7.0

**起因 / 需求**

App 又到 7 天该重装了——爷爷先让重装（已装好，seq 3596，新有效期到 2026-06-23），并问"能不能 7 天自动重新安装"。

**查明根因**

当前 App 用**免费个人 Apple 账号**签名（证书 `Apple Development: baofusir@gmail.com`，team 名"华 张"），provisioning profile **只签 7 天**（创建 06-16 → 过期 06-23）。**7 天是 Apple 对免费账号的硬性规定**，免费签名下无法绕过。付费 Apple Developer Program($99/年)可签 1 年——爷爷选了「免费 + Mac 定时自动重装」方案。

**改了什么（Mac custom_service_swift 仓库 fa976a9 + launchd）**

- `auto_reinstall.sh`（新增）：自动重装脚本——设备在线检查 → 解锁钥匙串 → 重新签名编译 → 装机；全程日志记 `auto_reinstall.log`（爷爷日志铁律）。iPhone 不在线则本次跳过、不报警。
- `launchd.autoinstall.plist`（新增，留痕；实际部署在 `~/Library/LaunchAgents/`）：每天 **9点/14点/21点** 三次触发。
- 已 `launchctl load` 生效。

**为什么这样设计（高频重试 = 坚固）**

- 每天 3 次 × 7 天 = **21 次机会**，只要这 7 天里 iPhone 有任意一次在家 Wi-Fi 在线，就自动刷新有效期，App 永不过期。
- launchd 错过触发（Mac 睡眠）会在唤醒后补跑。
- 比"每 6 天才跑一次"坚固得多（那种当天 iPhone 不在线就漏，App 直接过期）。

**前提与边界**

- 前提：Mac 保持开机 + iPhone 与 Mac 在同一局域网(家 Wi-Fi) + iPhone 解锁。
- 边界：iPhone 不在线则跳过等下次；免费账号签名 + Apple 通信偶尔失败也只是这次跳过、下次再来。
- 验证：手动跑脚本 9 秒跑通（装机 seq 3604），launchd 已加载。**纯 App 构建环境，后端/Web/服务器零变更。**

---

## [093] 2026-06-12 23:57 — 修复 App 下拉刷新卡顿：格式化器全局复用 + 排序预解析 + 刷新走增量合并（仅 App）· v0.7.0

**起因 / 需求**

爷爷反馈：两个列表界面（全部/已联系）下拉刷新拉数据时，App 界面出现卡顿。

**根因（三个叠加，全在主线程）**

1. `parseDate` 每次调用**新建一个 ISO8601DateFormatter**（创建约 1ms/个，很贵）；[092] 的合并排序比较器里每比较一对会话调 2 次 `parseDate` → 几十条会话排序比较几百次 → **一次下拉刷新主线程创建上千个格式化器**。
2. 列表每行时间 `fmtConvTime`/`fmtMsgTime` 每次渲染都新建 DateFormatter，重绘几十行又叠几十个。
3. `.refreshable` 下拉刷新走老的 `refreshConvs` **整列表重置**（数组整体替换）→ SwiftUI 全列表 diff 重建。

**改了什么（App 3 文件，Mac c31f482）**

- `Models.swift`：格式化器**全局只建一次、永久复用**（`_isoFrac/_isoPlain` + 4 个 DateFormatter + `_csCal/_csTZ` 东八区；iOS7+ 两类 formatter 线程安全）；`parseDate/fmtConvTime/fmtMsgTime` 改用；新增 `nowISOString()`。
- `Store.swift`：`mergeLatestPage` **排序前先把时间预解析好**再排（比较器零解析）；`bumpConv` 复用全局 formatter；`refreshLatest` 在已联系 tab 上即使列表还空也对账。
- `Views.swift`：下拉刷新 `.refreshable` 改走 `refreshLatest(force:true)` **增量对账合并**——只动有变化的行，不整列表重置。

**业务流程对比**

- 改动前：下拉刷新 → 主线程被上千次格式化器创建 + 全列表重建堵住 → 界面卡顿。
- 改动后：格式化器零新建 + 排序零重复解析 + 列表只更新变化行 → 下拉刷新主线程开销降约两个数量级，顺滑。

**触发场景与边界 + 验证**

- 触发：两个列表下拉刷新 / 进入对账 / WSS 重连对账，全部受益。
- 边界：全局 formatter 统一锁 Asia/Shanghai（时区行为与之前一致）；增量合并保留已加载分页。
- 验证：`xcodebuild BUILD SUCCEEDED` + 装机 seq 3476；爷爷真机下拉实测。仅 App 改动，后端/Web/服务器零变更。

---

## [092] 2026-06-12 23:32 — 进入/返回/回前台/重连 列表都对账拉最新：推拉结合保证不漏（App + Web）· v0.7.0

**起因 / 需求**

爷爷发现两个列表界面缺「任何时候进入都显示最新」的兜底逻辑——尤其 WSS 断线那几秒来的消息、从聊天界面返回列表时不刷新，列表会是旧的、漏消息。要求：既快又不漏、最坚固。

**方案：「推拉结合 · 进入即对账 · 先缓存秒开」三层保障**

1. **实时层（推）**：WSS 在线时新消息实时 bump 到列表（[091] 已做）——实时主力。
2. **对账层（拉）**：进入界面 / 从聊天返回 / 回前台 / WSS 重连，都拉「最新第一页(50条)」对账，补 WSS 漏的、断线期间的。**最新消息必在按时间倒序的第一页**，所以只拉 50 条即可补全，传输小、快。
3. **体验层（缓存）**：进入先显缓存（列表本就在显示旧数据，0 转圈秒开），后台静默拉第一页，增量合并（不整替换、不丢已加载分页、按时间重排），用户无感。

**改了什么**

- App `Store.swift`：新增 `refreshLatest(force)`（节流 0.5s，force 跳过节流必刷）+ `mergeLatestPage`（拉第一页 → `map` 去重合并 → `parseDate` 按时间重排，保留已加载分页）；`onAlive` 重连 → `refreshLatest(force)`。
- App `Views.swift`：`onAppear`（进入/返回）、`scenePhase active`（回前台）、`ChatView.onDisappear`（从聊天返回）都调 `refreshLatest`。
- Web `Console.vue`：WSS `onOpen` 重连 → `refreshConvs` + `refreshSideTotals` 对账（补断线期间漏的）；切 tab（watch filterMode）/ onMounted / 5 分钟定时已有。

**业务流程对比**

- 改动前：WSS 断线那几秒的消息漏掉 + 从聊天返回列表不刷新 → 列表显示旧的、漏消息。
- 改动后：进入 / 返回 / 回前台 / 重连 任一时机都拉最新第一页对账 → **推漏了拉兜底，不漏**；先显缓存秒开、后台静默刷新，不卡。

**触发场景与边界 + 验证**

- 触发：切 tab 进入、从聊天返回、App 回前台、WSS 重连 → 拉最新第一页合并；节流 0.5s 防快速切换抖动。
- 边界：增量合并不丢已滚动加载的分页；最新消息保证在第一页所以不漏；先显缓存 0 转圈。
- 验证：App `BUILD SUCCEEDED` + 装机 seq 3468；Web `vite build`。

---

## [091] 2026-06-08 05:44 — 发/收消息后会话列表本地实时更新 + 上浮（App + Web）· v0.7.0

**起因 / 需求**

爷爷反馈：在 App 聊天界面给访客发消息后，回到会话列表，列表**不实时更新**（最后一条预览 / 时间 / 排序都不变），要等刷新拉后端才更新。WS 收到访客消息也应该实时变。

**根因**

- App `sendText`/`uploadAndSend`：只 `append` 到聊天 `msgs`，**完全不更新 `convs` 会话列表**（预览/时间/排序全靠回界面时 refreshConvs 拉后端）。
- App `onEnvelope`（WSS 收消息）：只更新 `convs`、**不更新 `contactedConvs`（[088] 后有两个列表）**，也**不更新 `updated_at`**（时间不变）。
- Web `sendText`：更新了 `last_message`/`updated_at`，但**没上浮**到列表顶部。

**改了什么**

- App `Store.swift`：新增 `bumpConv(convId, sender, preview, addUnread)`——同时更新 `convs`(全部) + `contactedConvs`(已联系) 两个列表的 `last_message` + `updated_at`(ISO now) + 上浮到顶；`sendText`/`uploadAndSend` 发完调用（本地实时）；`onEnvelope` 收消息统一走 `bumpConv`（补齐 contactedConvs + 时间）。
- Web `Console.vue`：`sendText` 发完加上浮（`splice` + `unshift`）。

**业务流程对比**

- 改动前：客服发消息后会话列表纹丝不动，要等回界面刷新才显示；图2 看到"我：嗯"05:36 但最新"你好"05:37 没顶上来。
- 改动后：发完**立即**预览更新 + 时间更新 + 排到列表最前；WSS 收访客消息同样实时（含已联系列表）。

**触发场景与边界 + 验证**

- 客服发文字/图片 → 列表该会话立即更新预览+时间+上浮；WSS 收访客消息 → 同步更新（非当前会话 unread+1）。
- 边界：bumpConv 同时维护两个列表（同一客户在全部/已联系都更新）；ISO8601 时间被 parseDate 正确解析显示东八区。
- 验证：App `BUILD SUCCEEDED` + 装机 seq 3252；Web `vite build`。

---

## [090] 2026-06-08 04:58 — 修复点开客户只显最新一段会话：详情按客户聚合显示完整历史对话（后端+Web+App）· v0.7.0

**起因 / 需求**

[088] 列表已按客户(visitor)聚合，但点开后消息接口仍按单个 conv_id 加载——只显示该客户**最新一段会话**（常常只有系统消息），其历史会话段里的真实聊天/通话全看不到。下游实测：访客 18 段会话，真实对话在某旧段，点开却只看到最新段的系统消息。「列表按客户聚合了，详情也得按客户聚合」。

**改了什么**

后端（store + handler + 路由）：
- `store.go` 新增 `ListMessagesByVisitor(visitorID, before, after, limit)`：`JOIN conversations` 按 `visitor_id` 查该客户**所有会话段**的消息，按 `created_at` 排成一条完整时间流；read 状态按每条消息「所属那段会话」的 `last_read_*_at` 各算各的；分页同 ListMessages(after 增量/before 翻页/default 最新)。
- `http.go` 新增 handler `ListMessagesByVisitor`；`main.go` 注册路由 `GET /agent/visitor/:vid/messages`。

Web（admin）：
- `Console.vue` `loadMessages(convID)` → 改为按 `visitorID` 加载（调 `/agent/visitor/:vid/messages`），缓存键统一用 visitor_id；`pickConv` 传 `c.visitor_id`；WSS 落盘缓存键改 visitor_id。**发送/接管/已读仍用 conv_id**（最新会话）。

App（custom_service_swift）：
- `Store.swift` `openConv`：用该会话的 `visitor_id` 拉「全部会话段」完整历史（缓存键仍用 conv_id，与 WSS 一致）。

**业务流程对比**

- 改动前：点开客户 → 只拉最新一段会话的消息 → 历史真实对话/通话全看不到。
- 改动后：点开客户 → 按 visitor_id 拉其所有会话段消息，合并成一条完整时间流，从头到尾都能看到（含已结束会话段的聊天/通话）。

**触发场景与边界 + 验证**

- 点开任意客户：显示其名下所有会话段的消息时间流；发消息仍进当前会话(conv_id)。
- 边界：read 跨会话段各按所属会话 last_read 算；WSS 实时新消息按 conv==当前会话追加(历史段不再有新消息)；走 conversations.visitor_id + messages.conv_id 索引。
- 验证：后端 `go build` 通过；Web `vite build` 通过；App `xcodebuild BUILD SUCCEEDED`（CODE_SIGNING_ALLOWED=NO 验证代码编译通过）。**App 装机待设备连接稳定补做**（凌晨 USB 通道不稳，签名/装机被 Connection reset 打断，非代码问题）。

---

## [089] 2026-06-07 22:52 — 存档：旧 Flutter 版 App 毛玻璃试验半成品提交留痕（仅 mobile_app）· v0.7.0

**起因 / 需求**

源代码管理面板长期挂着 6 个 `mobile_app/` 未提交改动（本轮对话开始前就遗留的，非本次任务产生）。爷爷决定**提交保留、留痕**。

**说明**

`mobile_app/` 是**旧的 Flutter App**。客服 App 已全面转 SwiftUI 重写（Mac `~/code/custom_service_swift` 独立仓库，已迭代到 [088]），Flutter 版自 [072] 后即搁置。这批改动是当初在 Flutter 版上试「毛玻璃」效果的半成品。

**改了什么（仅 mobile_app，不影响后端 / Web / SwiftUI App）**

- `mobile_app/lib/widgets/glass.dart`（新增 60 行，毛玻璃 widget）
- `mobile_app/lib/pages/chat_page.dart` / `conversations_page.dart` / `home_page.dart`（试玻璃效果改动）
- `mobile_app/pubspec.yaml` / `pubspec.lock`（加了一个依赖）

**边界**

纯存档留痕，Flutter 版不再迭代（App 走 SwiftUI）。不触碰任何后端 / Web / 数据库 / 部署。

---

## [088] 2026-06-07 22:28 — 列表口径按爷爷最终定义重构：删「待回复」，「全部/已联系」都按客户聚合（后端+Web+App）· v0.7.0

**起因 / 需求**

爷爷最终敲定列表口径（推翻 [085][086][087] 的中间方案）：
1. 「待回复」tab **不需要，删掉**——列表只保留「全部 / 已联系」。
2. 「已联系」真义 = 访客**主动操作过**（手动打字 / 发图片 / 打语音电话），**不管客服是否回复**；纯浏览(page_navigation)、系统自动问候(greeting)、访客进入 都不算。
3. 「全部」= **所有来过的访客**，不管人走没走（关浏览器/关机/离线）、会话开着还是已关(open/closed)、说没说话。
4. 「全部」「已联系」都**按客户(访客)聚合，一人一条**（同一人多次来访/会话被拆段，合并成一条显示最新）。

**改了什么**

后端（migration 010 新增 + store + http）：
- `migrations/010_visitor_engaged.sql`（新增）：加 `visitor_engaged` 标记列（访客主动发消息/图片 或 打语音电话）+ 回填 + 索引；幂等。
- `store.go` `InsertMessage`：访客消息 → 置 `visitor_engaged=1`；voice 通话事件(sys + sender_ref 'voice%') → 也置 1。
- `store.go` 新增 `listVisitorAggregated(onlyContacted)`：按 visitor_id 窗口函数去重、每人取最新会话、跨 open/closed，每行带 contacted。→ `ListAllVisitorConversations`(全部) / `ListContactedConversations`(已联系，filter visitor_engaged=1)。
- `store.go`：`CountAllVisitors`(全部去重客户) / `CountContactedVisitors`(改 visitor_engaged)；**删** `ListOpenConversations`/`ListPendingConversations`/`CountPendingConversations`/`CountOpenConversations`。
- `http.go`：`mode=all`→ListAll、`mode=contacted`→ListContacted、**删** `mode=pending` 分支 + total。

Web（admin）：
- `Console.vue`：tab 三个→两个「全部 / 已联系」，删 pendingTotal/待回复 button/refreshSideTotals 的 pending 预取；空列表文案改「暂无访客 / 暂无主动联系过的客户」。

App（custom_service_swift）：
- `Store.swift`：删 pendingConvs/pendingTotal/分页状态/loadConvs pending 分支/reloadPending/refreshPendingTotal。
- `Views.swift`：segmented 三段→两段；TabView 删待回复页；onChange 删 pending。

**业务流程对比**

- 改动前([085]~[087])：已联系=客服回复过(漏新客户)→ 加待回复 tab 补救(又误收浏览/问候/voice)。三个 tab，口径绕。
- 改动后：两个 tab。**全部**=所有来过的客户(含只逛的/已走的/已关的)，一人一条；**已联系**=访客真动过手的客户(打字/图片/语音)，不管客服回没回、不管会话结没结束，一人一条。

**触发场景与边界 + 验证**

- 进「已联系」：访客发过文字/图片 或 打过语音电话(visitor_engaged=1)。不进：纯浏览/系统问候/访客进入(均不置 visitor_engaged)。
- 「全部」：所有有过会话的访客(含 visitor_engaged=0 的只浏览客户)，按客户聚合一人一条，跨 open/closed。
- 边界：窗口函数 ROW_NUMBER 一客户一条；voice 含未接/秒挂也算主动联系；migration 010 幂等(全新库0行/可重跑)；filter 代码常量拼接非用户输入、limit/offset 参数化无注入。
- 验证：后端 `go build`、Web `vite build`、App `xcodebuild BUILD SUCCEEDED` 全通过；部署后查「已联系」=COUNT(DISTINCT visitor WHERE visitor_engaged=1)、「全部」=COUNT(DISTINCT visitor)。

---

## [087] 2026-06-07 21:55 — 修复「待回复」误收纯浏览/问候/voice 会话：必须有访客真实消息打底（后端）· v0.7.0

**起因 / 需求**

爷爷发现 [086]「待回复」口径用了裸 `unread_agent>0`，可能把"只有系统消息（自动问候、浏览记录 page_navigation、voice 通话事件）但 unread 被污染"的会话误算进待回复。代码核查证实：现在 `InsertMessage`（[065]起）只有访客真实消息(sender='visitor')才 +1 unread，sys 消息不加；但**历史脏数据**（[065]修复前 unread 被 sys 污染的老会话）会让 [086] 口径误报。

**改了什么（store.go 2 处）**

`ListPendingConversations` + `CountPendingConversations` 的 WHERE 加前提：
```
AND EXISTS(SELECT 1 FROM messages m WHERE m.conv_id=c.id AND m.sender='visitor')
AND (c.unread_agent>0 OR c.agent_replied=0)
```
即「待回复」**必须有访客真实消息(sender='visitor')打底**，再看未读/未回复。不再依赖 unread_agent 的纯净度。

**业务流程对比**

- 改动前：访客只浏览了页面 / 只收到自动问候 / 只打了个 voice 没发消息 → 若 unread 被历史脏数据污染 → 误进「待回复」骚扰客服。
- 改动后：只有访客**真的发过文字/图片消息**且(没回复完 或 有未读)才进「待回复」。纯浏览、纯问候、纯进入、纯 voice 一律不进。

**触发场景与边界 + 验证**

- 不进待回复：page_navigation(sender='sys' sender_ref='page:')、greeting(sys)、visitor_enter(不入 messages 表)、voice 事件(sys sender_ref='voice') —— 均无 sender='visitor' 真实消息。
- 进待回复：访客发过文字/媒体(sender='visitor') 且 (unread>0 或 agent_replied=0)。
- 验证：后端 `go build` 通过；部署后「待回复」数应排除那些只有系统消息的会话。

---

## [086] 2026-06-07 20:06 — 修复 Bug④「[085] 致新客户咨询全部漏接」：新增「待回复」工作队列 tab（后端+Web+App）· v0.7.0

**起因 / 需求**

[085] 把「已联系」改为「客服回复过(agent_replied=1)的访客」，**严重副作用**：新客户首次咨询(从未被回复)永远不进「已联系」，客服只看「已联系」会漏掉所有新客户。实测最近 2 天 6 个有访客消息的会话，5 个 agent_replied=0、只在「全部」可见(含未读 3、未读 1 的真实待回复客户)。

**方案选择（爷爷给①②③三选一/组合）**

- ② 放宽「已联系」为 agent_id 非空 OR 回复过 —— **否决**：新客户没人接管时 agent_id 仍为 NULL，救不了漏接。
- ① 新增「待回复」筛选标签 + ③ 有未读强制置顶 —— **采纳**：给客服一个专门的「谁在等我回」工作队列。

**改了什么（后端 store/http + Web + App）**

口径：「待回复」= open 且（`unread_agent>0` 有未读 **OR** `agent_replied=0` 且访客发过真实消息）。排序 `unread_agent DESC, updated_at DESC`(未读强制靠前，不被新访客挤走=期望③)。

- `store.go`：新增 `ListPendingConversations`(分页) + `CountPendingConversations`(红点)。
- `http.go`：`ListConversations` 加 `mode=pending` 分支(列表 + total)。
- `Console.vue`：会话列表 tab 从 2 个变 3 个「全部 / 待回复 / 已联系」，「待回复」数字 >0 时**红色加粗**；异步并行预取待回复+已联系总数(`refreshSideTotals`)。
- App `Store.swift`：新增 `pendingConvs`/`pendingTotal` 独立分页 + `reloadPending`/`refreshPendingTotal`；`Views.swift`：segmented 加「待回复」段 + TabView 加待回复页 + 触底加载。

**业务流程对比**

- 改动前([085])：新客户发消息但没被回复 → 不进「已联系」→ 客服盯「已联系」漏接，只能在「全部」里大海捞针。
- 改动后：新客户发消息 → 立即进「待回复」(红色数字提醒)，未读的强制置顶；客服回复后该客户进「已联系」。三个视图分工：全部=总览 / 待回复=待办 / 已联系=客户档案。

**触发场景与边界 + 验证**

- 触发：访客发消息且(有未读 或 该客户从没被回复) → 进「待回复」，未读多的在最前。
- 边界：客服回复后(agent_replied=1 且 unread=0)移出待回复(下次刷新/切 tab 生效)；纯浏览没发消息的访客不进待回复(不打扰)。
- 验证：后端 `go build` 通过；Web `vite build` 通过；App `xcodebuild BUILD SUCCEEDED`；部署后查「待回复」数 = 测试服有访客消息未回复的会话数(应含 5 个 agent_replied=0 + 有未读的)。

---

## [085] 2026-06-06 20:08 — 修复 Bug③「已联系」严重失真(接待450只显4)：口径重构 + 滚动分页 + 会话超时阈值可配（后端+Web+App）· v0.7.0

**起因 / 需求**

上游用他们生产服务器真实数据测出：实际接待过 450 个客户，工作台「已联系」只显示 4 个（Web 与 iOS App 一致）。

**根因（两层叠加）**

1. 后端 `GET /api/agent/conversations` 写死只返回最近 200 条 open 会话，前端本地切「已联系」。
2. 访客离开超 30 分钟再进入触发 `EnsureFreshConversation(...,30)`：关旧会话 + 新建，新会话 ① 不带 agent_id（NULL）② 没有历史 visitor 消息(消息留在旧 closed 会话)→`has_visitor_msg=false`③ `updated_at` 最新挤进 200 窗口最前，把老的「已接待」会话挤出。证据：846/850(99.5%) closed 会话 closed_at 与同访客下一会话 started_at 精确衔接（进入即重建）。

**改了什么**

爷爷决策：「已联系」口径 = **客服真正回复过(sender='agent')，按客户历史聚合**；**前端滚动分页**（Web+App），加载要快、能异步就异步。

后端（migration 1 新增 + store/service/http）：
- `migrations/009_agent_replied.sql`：新增 `agent_replied` 标记列(客服回复过的会话) + 回填存量 + 2 索引；幂等(information_schema 守护 DDL)。
- `store.go`：① `createConversation` 加 agentID 参数，超时重建**继承旧客服**(期望①)；② `ListOpenConversations` 加 offset 分页 + `contacted` 字段(按访客聚合 agent_replied)；③ 新增 `ListContactedConversations`(窗口函数 ROW_NUMBER 按访客去重、跨 open/closed、分页)；④ `MarkAgentReplied`；⑤ `CountContactedVisitors`/`CountOpenConversations`。
- `service.go`：客服消息持久化时 `MarkAgentReplied`；新增 `SettingInt`。
- `http.go`：`ListConversations` 支持 `mode`/`offset`/`limit`，返回 `total`；阈值 `session_fresh_minutes` 走 settings(期望③，clamp 1-1440)；settings key 白名单加该项。

Web（admin）：
- `Console.vue`：两 tab 滚动分页(`loadConvs` reset/append、`onConvScroll` 触底、`watch(filterMode)` 重载)；「已联系」改用后端 `contacted` 口径；tab 数字用后端 `total`(异步预取已联系总数不阻塞主列表)。
- `Settings.vue`：加「会话保持时长」(`session_fresh_minutes`，el-input-number 1-1440)。

App（Mac custom_service_swift）：
- `Models.swift`：Conversation 加 `contacted`/`status`，`isContacted` 改 `contacted==true`。
- `Store.swift`：拆 `convs`(全部)/`contactedConvs`(已联系)两份独立分页 + `loadConvs`/`loadMoreConvs`/`reloadContacted`/`refreshContactedTotal`。
- `Views.swift`：TabView 两页各自数组 + List 触底 `onAppear` 加载下一页 + tab 数字用 total。
- `SettingsView.swift`：加「会话保持」Stepper(1-1440)。

**业务流程对比**

- 改动前：访客 30 分钟后回来 → 会话重建丢 agent_id/消息 → 「已联系」判定失败 + 200 窗口截断 → 450 客户只显 4，客服找不到接待过的人。
- 改动后：「已联系」= 客服回复过的客户，按客户历史聚合(重建不丢)、跨 open/closed、滚动分页加载全部；重建会话继承原客服；30 分钟阈值后台可配。

**触发场景与边界 + 验证**

- 触发：客服回复过的客户进「已联系」(去重一人一条)，滚动到底自动加载下一页；阈值改 settings 实时生效。
- 边界：窗口函数仅作用于「被回复过」的访客子集(idx_agent_replied + idx_visitor_updated)；分页去重防 WSS 上浮重复；阈值 clamp 1-1440 防误配；migration 幂等(全新库 0 行、可重跑)。
- 验证：后端 `go build` 通过；Web `vite build` 通过；App `xcodebuild BUILD SUCCEEDED`；部署后 docker 自动跑 009，「已联系」数 = COUNT(DISTINCT visitor WHERE agent_replied=1)。

---

## [084] 2026-06-06 16:07 — 修复 Bug②「App 发出去的消息消失」：WS 连接就绪时机错误致断网必丢（App）· v0.7.0

**起因 / 需求**

爷爷反馈 Bug②：用手机 App 发消息，网络一断就丢，今天**断 20 次、成功 0 条**。之前 [079] 的"防丢失（ACK 确认 + 重连重发）"只在电脑网页版彻底生效，App 这版没修干净。

**根因（大白话）**

App 的 WS 连接在 `task.resume()`（只是发起握手，TCP/TLS/WS upgrade 都没完成、还没真连上）后就**立即** `onAlive(true)` → 触发 `resendPending` 重发，把消息发进一个"假装连上了"的连接里，全丢；而真正连上（收到服务器 `hello`）时反而不再触发重发。再加上 `sendDict` 只看 `task != nil` 就返回 `true`（误判已发），`send` 失败的回调还被 `{ _ in }` 吞掉。浏览器版用 `onopen`（真连上才触发）所以没这问题——这正是"网页版好了、App 没好"的根。

**改了什么（App `Sources/WSManager.swift` 重写，5 处）**

- 新增 `ready` 标志：只有**收到服务器 hello** 才算"连接真正就绪"
- `connect()`：去掉 `resume` 后立即 `onAlive(true)`（假就绪根因）
- `receive()` 收到 hello：`ready=true` + `retry=0` + `onAlive(true)`（此刻才触发 resendPending，时机正确）
- `sendDict`：`guard ready`（未就绪返回 `false` → 上层标红「未送达」→ 重连后自动重发）；`send` 失败回调触发 `handleDisconnect` 重连（带 `task === self.task` 比对，避免误杀已重连的新连接）
- `handleDisconnect`/`stop`：`ready=false` + 防重入

**业务流程对比**

- 改动前：断网发消息 → App 假装"已发" → 实际进黑洞 → 断 20 次成功 0 条
- 改动后：断网发消息 → 立即标红「未送达」→ 网络恢复（收到 hello）→ 自动重发 → 收到 ACK 变「已发」。彻底对齐网页版可靠性

**触发场景与边界 + 验证**

- 触发：手机网络抖动 / WS 断开期间发消息 → 标红「未送达」，连上自动重发
- 边界：`send` 回调延迟到达时用 `task===self.task` 比对，只有还是同一连接才判坏，不误杀已重连的新连接；`handleDisconnect` 防重入避免 send 失败与 receive 失败重复触发
- 说明：Store 层 `sendText` 的 `ok ? .sending : .failed`（断开就标红）逻辑本就正确，关键是 `sendDict` 返回值要准——本次正是把它修准
- 验证：`xcodebuild ** BUILD SUCCEEDED **` + 装机成功（seq 3132）

---

## [083] 2026-06-06 15:59 — 修复 Bug①「收到通知看不到人」：会话超时自动关闭误埋未读（后端 + 数据修复 + 重新 up 测试服）· v0.7.0

**起因 / 需求**

爷爷反馈 Bug①：客服收到「访客进入 / 新消息」通知，点开会话列表却**找不到人**，未读像凭空消失。同时爷爷把测试服挪用跑了别的项目，要求把别的项目停掉、客服系统重新 up 起来。

**根因（大白话）**

[store.go](backend/internal/store/store.go) 的 `EnsureFreshConversation(freshMinutes=30)`：访客隔 30 分钟再进来，系统会把上一段会话直接 `status=closed` 再开一段新的。但它**没检查旧会话里是否还有客服没看过的消息**(`unread_agent>0`)。有未读的会话被关 → 工作台列表只查 `status='open'` → 这条不显示 → 客服收到过通知却找不到人，未读被埋（消息没丢，一直在 messages 表，只是承载它的会话被关）。

**改了什么（修改 1 处 + 新增 1 个 migration）**

- `backend/internal/store/store.go` `EnsureFreshConversation`：超时重开前先判断 `existing.UnreadA>0`，**有未读就直接复用旧会话**(`return existing,false`，不关不另起)，让客服先处理未读；无未读才走原来的关旧开新。从源头杜绝再产生。
- `backend/migrations/008_reopen_unread_closed.sql`（新增）：数据修复，把历史上已被误埋的会话(`status='closed' AND unread_agent>0`)恢复为 `open` + 清 `closed_at`，让客服看到并处理。docker 启动自动迁移、幂等（全新库命中 0 行）。

**业务流程对比**

- 改动前：访客 30 分钟后回来 → 旧会话(含未读)被关 → 工作台列表看不到 → 客服找不到人，客户被晾着
- 改动后：旧会话有未读 → 复用它 → 列表正常显示未读 → 客服无缝接上；历史被埋的也一次性恢复

**触发场景与边界 + 验证**

- 触发：访客距上次活动 > 30 分钟再次进入，且旧会话有客服未读 → 复用旧会话
- 不触发：旧会话无未读 → 仍按原逻辑关旧开新（隔很久回来算新一轮咨询，重新问候）
- 边界：migration 只动「已关闭且有未读」，正常关闭(无未读)一律不碰；幂等可重复执行
- 验证：部署后 docker 自动跑 008，查 `status='closed' AND unread_agent>0` 应为 0；代码层新访客 30 分钟回访带未读时列表可见

**运维**

测试服(38.76.193.68)别的项目（宝塔 nginx `/www/server/nginx` + `doqaus-php` 容器）停掉，释放 80/443；客服系统 `docker compose up -d --build`（含本次 Bug① 修复 + 008 迁移）。数据卷 `cs_mysql_data`/`cs_redis_data`/`cs_ssl_data`/`cs_acme_data` 及别的项目数据卷**全程未动**。

---

## [082] 2026-06-04 16:14 — 「新访客进入提醒」开关补齐前端二道保险 + 命名直白（Web + App）· v0.7.0

**起因 / 需求**

爷爷要求：系统设置加一个开关，"新访客来了之后是否通知，不通知就不响那一声"，Web 客服工作台和 App 都要。爷爷暂不部署那台测试服（已挪作他用），只改代码本地验，App 照常装机。

**现状核查（先汇报，不凭印象）**

这个开关其实**早已存在**，键名 `notify_visitor_enter`：
- 后端 `service.go:590` 已 `if s.SettingBool(ctx,"notify_visitor_enter",true)` gate 了 `visitor_enter` sys 事件下发——关掉后端就不广播，两端自然不弹不响。**它没有形同虚设**。
- 但有两个问题：① 设置页 label 叫"通知客服"，藏在「访客进入网站」分区，爷爷没认出来就是它；② Web/App **本地没读这个值做二道保险**，万一后端版本不一致（旧版/配置漂移）就兜不住。

**改了什么（复用现有 `notify_visitor_enter` 键，不新造；修改 0 新增 0 删除 0 个功能键，纯增强）**

Web 工作台（admin，2 文件）：
- `admin/src/views/Settings.vue`：label "通知客服" → "**新访客进入提醒**"，提示语改直白（开/关分别什么效果，并注明"访客真正发来消息仍照常提醒，不受影响"）
- `admin/src/views/Console.vue`：① 新增 ref `notifyVisitorEnter`(默认 true)；② `loadSoundPref` 拉 `/admin/settings` 时一并读 `notify_visitor_enter`；③ 收 `visitor_enter` 事件播声/弹窗前加 `if (notifyVisitorEnter.value)` 本地 gate（二道保险）

App（Mac `~/code/custom_service_swift`，3 文件）：
- `Sources/Store.swift`：① 新增属性 `var notifyVisitorEnter = true`；② `onEnvelope` 的 `visitor_enter` 分支加 `, notifyVisitorEnter` 条件，关了不 `playNotify`
- `Sources/SoundPreview.swift`：`loadAgentSound()` 拉设置时一并读 `notify_visitor_enter`
- `Sources/SettingsView.swift`：Toggle "通知客服" → "新访客进入提醒"，Section 加 footer 说明

后端：**不改**（已 gate，且不碰那台服务器）。

**业务流程对比**

- 改动前：客服在设置里看到"通知客服"开关不知是干啥；关掉只靠后端不发事件生效，前端无兜底
- 改动后：开关叫"新访客进入提醒"一目了然；关掉后——后端不发(第一道) + 前端即便收到也不弹不响(第二道)。访客真正发消息的提示音不受此开关影响，避免漏接真实消息

**触发场景与边界 + 验证**

- 触发：仅"新访客打开带 widget 的网页"(visitor_enter)。关掉=不弹通知不响声
- 不触发：老访客/访客发真实消息的提示音**永远不受影响**（防漏接）
- 边界：非 admin 客服拉不到设置 → 本地默认 true，但后端 gate 同样不发，结果一致；设置值显式 'false' 才算关，其余(含缺失)默认开
- 验证：Web 改动 4 处已落地；App build `** BUILD SUCCEEDED **`（5 处 Swift 改动编译+codesign 通过），`.app` 已签好待装机（手机当前 unavailable，上线即装）

---

## [081] 2026-06-04 12:10 — 消息时间跨年带年份完善（三端）· v0.7.0

**起因 / 需求**

爷爷强调"不是今天的要带日期"。[080] 已做今天 HH:mm / 非今天 MM-dd HH:mm，本条完善：**跨年的带上年份**，避免跨年混淆。

**改了什么（三端 fmtMsgTime 加跨年判断）**

统一格式：**今天 HH:mm / 今年非今天 MM-dd HH:mm / 往年 yyyy-MM-dd HH:mm**
- App（Mac 789e7c0）Models.swift fmtMsgTime：加 `cal.isDate(d, equalTo:, toGranularity:.year)` 判断
- Web Console.vue：`dayjs isSame(now,'year')` 判断
- 访客端 chat.html：`getFullYear()` 比较

**验证**：App 装机 INSTALL_EXIT=0(seq 3004)；Web/widget rebuild（vite/Dockerfile）。需 deploy(10)+rebuild admin+widget 生效。

---

## [080] 2026-06-04 12:01 — 三端聊天每条消息显示具体时间（App + Web + 访客端）· v0.7.0

**起因 / 需求**

爷爷要 App / Web 客服工作台 / 访客端聊天界面**每条消息都显示具体时间**（之前只有 hover title 绝对时间 / 组间 time-divider，每条消息没有可见时间）。

**改了什么（三端）**

- **App**（Mac 58d66b2）：Models 加 `fmtMsgTime`（今天 HH:mm / 更早 MM-dd HH:mm，Asia/Shanghai）；MessageRow 气泡下加时间（所有消息）与发送状态同一行
- **Web** admin Console.vue：加 `fmtMsgTime`（dayjs，今天 HH:mm / 更早 MM-DD HH:mm）；每条 bubble 内底部加可见时间（右对齐小灰字）
- **访客端** widget/public/chat.html：加 `fmtMsgTime`；buildBubble 每条消息 bubble 末尾加可见时间

**业务流程对比**

- 改前：消息时间只在 hover bubble title（绝对时间）/ 组间 time-divider 显示
- 改后：每条消息气泡下/内都有可见的具体时间（今天 HH:mm，跨天带 MM-dd 日期）

**验证**：App 装机 INSTALL_EXIT=0(seq 2996)；Web/widget rebuild（vite / Dockerfile COPY）。需 deploy(10)+rebuild admin+widget 生效。

---

## [079] 2026-06-04 03:41 — 消息可靠发送：ACK 确认 + 未送达标红重发 + 重连自动重发（后端+App+Web）· v0.7.0

**起因 / 需求**

那边反馈 bug：客服端 WS 断开/不稳定时发的消息静默丢失（raw_ws 实测故障时段客服 chat rx=0、DB 无 agent 消息），但界面假显示"已发送"。客服误以为发成功、重复发，影响服务。

**根因（四路 agent 调研 + 实读代码）**

客户端发送缺可靠性：WSManager.sendDict(App) / ws.send(Web) 断连/失败时丢弃消息但 UI 乐观显示成功；无 ACK 确认、无失败重发、无离线队列。与 [068] token/WS 不稳定叠加。

**改了什么（后端 1 + App 4 + Web 1）**

- **后端** ws/hub.go：agent chat PreprocessAgentMessage 通过后回 `{type:ack, id, conv}`（客户端发送带本地 id，据此确认送达）
- **App**（Mac 仓库 756d1fa）：MsgStatus(sending/sent/failed)；WSManager.sendDict 返回 Bool；Store 状态机（带 id 发送+乐观 sending+12s ACK 超时+收 ack 转 sent+点击重发 resend+重连 onAlive 自动重发 resendPending）；MessageRow 发送中转圈/已送达/未送达红❗可点重发
- **Web** admin Console.vue：消息加 status；sendText/uploadAndSendFile 带 id + ws.send 返回值标 sending/failed + 12s 超时；onMessage type=ack→sent；onOpen 重连自动重发；角标显示 发送中/未送达(红可点重发)/已读

**业务流程对比**

- 改前：断连/丢包发消息 → 静默丢弃但显示"已发送"，客服以为成功（实际访客和 DB 都没有）
- 改后：服务器收到才回 ACK → 标"已送达"；12s 没收到 ACK → 标红"未送达·点击重发"；WS 重连自动重发未确认；**绝不假"已发送"**

**触发场景与边界 + 验证方式**

触发：WS 断/网络抖动发消息 → 12s 内没 ACK → 红色未送达。边界：Web 原本断开时 sendText 已 `if(!ws.alive) return` 提示不发，本次补"发出后没收到 ACK"的兜底。
验证：后端 go build PASS、App 装机 INSTALL_EXIT=0(seq 2980)、Web rebuild admin（vite build）。需 deploy(10)+rebuild backend(已部署)+admin 生效。

---

## [078] 2026-06-03 19:41 — 客户端堵空 conv（App+Web 第二道防线）+ 客服发消息发送音 · v0.7.0

**起因 / 需求**

[077] 后端已从源头杜绝 conv_id 孤儿（强制校验，任何端发空 conv 都被拒）。本条做客户端配套：① 第二道防线——App/Web 发送前也拦空 conv、明确提示不静默；② 爷爷要求客服发消息有声音反馈。

**改了什么（App Swift + Web admin）**

- **Swift App**（Mac 独立仓库 commit 9a73af6）：import AudioToolbox；sendText/uploadAndSend 发送后 `AudioServicesPlaySystemSound(1004)` 发送音；sendText/uploadAndSend guard 加 `!cid.isEmpty`、sendRead guard `!convId.isEmpty`、openConv guard `!id.isEmpty` —— 拦空串 conv
- **Web admin** Console.vue：① sendText 空 conv 由静默 return 改 `ElMessage.warning("请先选择一个会话再发送")`；② uploadAndSendFile 空 conv 改 `ElMessage.warning("会话已失效，请重新选择会话")`

**业务流程对比**

- 改前：conv 空时静默不发（App）/静默 return（Web），客服以为发出去了；发消息无声音
- 改后：conv 空明确提示"请先选会话"；客服发消息有发送音反馈

**触发场景与边界 + 验证方式**

触发：会话未选中/已失效时发送 → 明确提示。后端 [077] 已强制拦截（核心从源头杜绝），客户端为第二道防线 + 体验优化。
验证：Swift ARCHIVE/EXPORT SUCCEEDED 装机 INSTALL_EXIT=0；Web rebuild admin（vite build）。需 deploy + rebuild admin 生效。

---

## [077] 2026-06-03 19:27 — 修复客服消息 conv_id 偶发为空（孤儿消息）：后端强制校验 + 历史数据修复 · v0.7.0

**起因 / 需求**

爷爷生产库实测：客服 App/Web 发的部分消息按会话查不到（"消失"，但访客已收到）。5120 条消息中 26 条 conv_id 为空，全部 sender=agent，跨版本长期间歇（~0.5%），涉及多个客服（aid=1/4）。客服误以为发送失败、重复发送，影响服务。

**根因（四路 agent 调研 + 实读代码，并纠正一处误判）**

agent 消息入库的 conv_id 用的是**后端连接快照 c.ConvID**（非前端传的 e.ConvID）。客服 WSS 建连时 c.ConvID 为空，必须点开会话（AssignSelf→AttachAgentToConv）才绑定。在"建连→接管"窗口内发消息，service.go PersistMessageAsync（:205）快照到空 c.ConvID 直接入库 → 孤儿。visitor 路径有兜底（:217 OpenOrGetConversation 补建），agent 路径完全没兜底。InsertMessage 也无 conv_id 非空校验。[069] 遗留的 agentInConv 校验 TODO 至今未实现。次因：WSS 重连未重新 AssignSelf、并发竞态、空串绕过 NOT NULL。

**改了什么（后端，修改 3 + 新增 2）**

> 修改文件 3（service.go/hub.go/store.go）+ 新增迁移 1，升版本 0.6.9 → 0.7.0

- `service.go` PreprocessAgentMessage：强制校验 —— c.ConvID 空回 error("请先点开会话")+记 agent_msg_no_conv 日志+拦截（不广播不入库）；调 AgentOwnsConversation 校验会话存在 + 该客服已接管（防越权），不过回 error
- `ws/hub.go` agent 分支：补 `e.ConvID = c.ConvID`（对齐 visitor，服务器权威不信前端）
- `store.go`：新增 AgentOwnsConversation（idx_agent 索引 + 参数化，agent_id 匹配或未分配）；InsertMessage 入口加 conv_id 非空兜底
- `migrations/007_fix_orphan_messages.sql`：Docker 自动迁移，26 条孤儿按"客服/访客 + created_at 时间最接近活跃会话"关联回 conv_id；REGEXP 防 CAST 非数字、关联不到的保留不动；单事务失败回滚

**业务流程对比**

| 场景 | 改动前 | 改动后 |
| --- | --- | --- |
| 客服接管会话前发消息 | 空 conv_id 入库 → 孤儿（界面消失） | 被拒 + 提示"请先点开会话"，不产生孤儿 |
| 历史 26 条孤儿 | 按会话查不到 | Docker 启动自动迁移找回归属、界面可见 |
| 客服发给他人已接管会话 | 可越权写入 | 被拒（AgentOwnsConversation 校验） |

**触发场景与边界 + 验证方式**

触发：客服建连后、点开会话前发消息 → 拦截。边界：会话未分配（agent_id NULL）允许发（接管中）；他人接管 → 拒；visitor 兜底不变。
验证：`go build ./...` PASS（BUILD_OK）。部署后 007 自动跑，孤儿数 26→0（或仅关联不到的残留）。需服务器 deploy + rebuild backend 生效。客户端堵漏 + 发送音见 [078]。

---

## [075] 2026-06-02 23:42 — 阿里云国内镜像源实际打通（张家口个人版，7 镜像已推 + 公开）· v0.6.9

**起因 / 需求**

[074] 配好 CI 双推框架后，爷爷提供 AccessKey 让直接打通国内源（GHCR 在国内拉不下来）。

**做了什么（运维操作，用 AK 调阿里云 OpenAPI + 服务器 docker 完成）**

- 验证 AK（主账号 root），激活个人版 ACR，定位实例地域 = **cn-zhangjiakou（张家口）**
- 创建**公开**命名空间 `baofusir`
- 把测试服已 build 的 7 个镜像 tag + push 到个人版 registry，并把 7 仓库设为**公开**
- 登出后匿名 `docker pull` 验证通过（digest 一致）

**真实坐标（关键，以后查 —— 用户名/密码见 ACR「访问凭证」页，不写入仓库）**

- registry：`crpi-saarj7fitzff243d.cn-zhangjiakou.personal.cr.aliyuncs.com`
- namespace：`baofusir`
- 镜像：`<registry>/baofusir/cs-{backend,admin,widget,nginx,coturn,redis,mysql}:latest`
- 生产 `REGISTRY_BASE=crpi-saarj7fitzff243d.cn-zhangjiakou.personal.cr.aliyuncs.com/baofusir`

**坑（血泪教训）**

个人版 ACR 的 registry 域名是**专属的** `crpi-xxx.cn-zhangjiakou.personal.cr.aliyuncs.com`，
不是通用的 `registry.cn-zhangjiakou.aliyuncs.com`——用通用域名一直 403/401。正确域名在控制台「访问凭证」页查。

**改了什么（文件 3 个，仅注释/示例域名）**

- `.env.example` / `docker-compose.production.yml` / `.github/workflows/build-images.yml`：示例域名由占位 `registry.cn-hangzhou` 改为真实 `crpi-...` 个人版域名。

**业务流程对比**

- 改前：服务器从 GHCR（美国）拉镜像慢/拉不下来。
- 改后：服务器 `.env` 设 `REGISTRY_BASE=crpi-.../baofusir`，`docker compose pull` 从张家口**秒拉（公开免登录）**。

**待办（CI 自动双推，可选）**

配 4 个 GitHub Secret（`ALIYUN_REGISTRY`/`ALIYUN_NAMESPACE`/`ALIYUN_USERNAME`/`ALIYUN_PASSWORD`）后，每次 push 自动双推 GHCR + 阿里云。PASSWORD 需在 ACR「访问凭证」页设固定密码。

**验证方式**

`docker logout` 后匿名 `docker pull .../baofusir/cs-backend:latest` → `Status: Downloaded`，digest `a342ad...` 一致。

---

## [074] 2026-06-02 22:50 — CI 双推国内源（阿里云 ACR）+ 生产 compose 镜像源可一键切换 · v0.6.9

**起因 / 需求**

爷爷反馈：服务器从 GHCR（GitHub 美国）拉镜像拉不下来（国内网络慢/超时）。希望镜像能推一份到国内源，服务器从国内秒拉。

**决策（爷爷拍板）**

用 AskUserQuestion 给了 4 个方案，爷爷选 **A. 阿里云 ACR**：
- A 阿里云 ACR（选中）：CI build 后双推 GHCR + 阿里云，国内稳、免费个人版
- B 配 GHCR 国内代理：省账号但代理稳定性差 —— 否
- C 继续纯 build：现有服务器够用但开新服务器慢 —— 否

**改了什么（修改文件 3 个，新增 0 删除 0）**

- `.github/workflows/build-images.yml`：build 后**双推**——① 新增 `Login to Aliyun ACR` step（`if: env.ALIYUN_REGISTRY != ''`，配了 Secret 才执行）；② env 加 `ALIYUN_REGISTRY`/`ALIYUN_NAMESPACE`（引用 Secret）；③ Compute tags 追加阿里云同名 tag（latest/sha/版本号）。**没配 Secret 时只推 GHCR，完全不破坏现状**。
- `docker-compose.production.yml`：7 个 `image:` 由 `ghcr.io/baofusirys/cs-*` 改为 `${REGISTRY_BASE:-ghcr.io/baofusirys}/cs-*`，服务器 `.env` 设 `REGISTRY_BASE` 即可一键切国内/国外源 + 顶部注释加用法。
- `.env.example`：新增 `REGISTRY_BASE` 说明段（仅 production 拉镜像部署相关）。

**业务流程对比**

- 改前：CI 只推 GHCR，国内服务器 `docker compose pull` 慢/拉不下来。
- 改后：配齐阿里云 4 个 Secret 后，CI 同时推 GHCR + 阿里云；生产服务器 `.env` 设 `REGISTRY_BASE=registry.cn-hangzhou.aliyuncs.com/<命名空间>` 即可秒拉。

**待爷爷提供（才能真正启用国内源）**

阿里云容器镜像服务（ACR）的 registry 地址 / 命名空间 / 用户名 / 密码 → 存到 GitHub Repo Secret：`ALIYUN_REGISTRY`、`ALIYUN_NAMESPACE`、`ALIYUN_USERNAME`、`ALIYUN_PASSWORD`。

**触发场景与边界 + 验证方式**

边界：未配 Secret 时 `Login to Aliyun` step 因 `if env != ''` 跳过、tags 不追加阿里云 → CI 行为同现状（只推 GHCR），不会 break；本台测试服走源码 build（docker-compose.yml），本改动不影响它。
验证：YAML 结构合法；push 后观察 Actions——未配 Secret 应只见 GHCR 推送、阿里云 step skipped。**本改动不影响现有测试服运行（它走 build，不拉镜像）**。

---

## [073] 2026-06-02 19:52 — 客服点开会话不再把会话时间顶成「点击时间」 · v0.6.9

**起因 / 需求**

爷爷反馈 App（web 客服工作台也一样）的 bug：会话列表那个时间应该是「消息来的时间」，不是「客服点开这个会话的时间」。现状：访客 9:00 来消息，客服点开看了一眼，10 分钟后再点开，返回列表时该会话时间变成了 9:10（点击时刻），排序也跟着乱。

**根因（后端，三端共性 bug）**

会话列表 `ORDER BY updated_at DESC` + 侧边显示 updated_at（web / Swift / Flutter 三端都取后端 updated_at）。而「客服点开会话」这条路径上有三个写操作都顺手刷了 `updated_at=now()`：① `AssignAgent` 接管会话 ② `MarkRead` 标记已读 ③ `UpdateLastRead` 已读落地。[072] 当时只拦了 `InsertMessage` 里的 page_navigation，**漏了这三条不经过 InsertMessage 的直接 UPDATE 路径**。

**改了什么**

> 修改功能 3 个（修复），删除功能 0 个，新增功能 0 个。修改文件 1 个（backend/internal/store/store.go），改 3 个函数，升版本 0.6.8 → 0.6.9

- `AssignAgent`（store.go:315）：去掉 SQL 里的 `updated_at=?`，只更新 agent_id（接管不是新消息，不该上浮）
- `MarkRead`（store.go:504）：去掉 `updated_at=?`，只清未读计数
- `UpdateLastRead`（store.go:518）：去掉 `updated_at=?`，只推 last_read_*_at + 清对应未读
- 改后 `updated_at` 仅由 `InsertMessage`（真实 visitor/agent 消息、非 page 的 sys）维护 = 纯粹「最后一条消息时间」。**无需改任何前端——web / Swift / Flutter 三端自动好**。

**业务流程对比**

| 场景 | 改动前 | 改动后 |
| --- | --- | --- |
| 访客 9:00 来消息、客服 9:10 点开看 | 列表时间变 9:10、会话上浮 | 列表时间保持 9:00、不上浮 |
| 客服接管会话 | 会话上浮到顶 | 位置不变（无新消息）|
| 访客发新消息 | 上浮 + 更新时间 | 仍上浮 + 更新时间（正确，真实活动）|
| 访客来电(voice) | 上浮 | 仍上浮（[071] 来电是真实活动）|

**触发场景与边界 + 验证方式**

触发：客服点开任意会话（触发 assign + 已读）→ 该会话列表时间/排序不变。
边界：只去掉「接管/已读」的 updated_at 刷新；真实消息（InsertMessage visitor/agent/非 page 的 sys）照旧刷 updated_at 上浮；`CloseConversation` 仍刷 updated_at（已关闭会话不在 open 列表，无影响）。
验证：后端 `go build ./...` PASS（BUILD_OK）。手测：客服点开旧会话 → 返回列表，时间停在最后一条消息时刻不动；访客发新消息 → 正常上浮。**需服务器 `docker pull` 新镜像后生效，三端 App/web 不用更新**。

---

## [072] 2026-06-01 19:52 — 页面访问不再顶起会话列表的时间和排序 · v0.6.8

**起因 / 需求**

爷爷截图反馈：访客的「访客访问了 XX 页面」这种浏览动作（page_navigation，橙色横幅）该显示在聊天记录里没问题，但**不该影响**会话列表侧边栏的「最新消息时间」和「排序」。现在访客每访问一个 URL，对应会话就被顶到列表最上、时间也改成访问时刻，挤掉了真正的最后一句对话。

**根因**

`store.go` InsertMessage 对**所有 sys 消息**（含 page_navigation）都执行 `UPDATE conversations SET updated_at=?`，而会话列表 `ORDER BY c.updated_at DESC` + 侧边显示 updated_at + getLastMessagePreview 取最后一条消息（含 page_nav）。三处叠加 → 页面访问顶起时间和排序、还可能占据预览。

**改了什么（三端齐改，精确只动 page_navigation，不碰 voice 来电等其他 sys）**

> 修改文件 3 个（backend 1 + admin 1 + mobile_app 1），升版本号 0.6.7 → 0.6.8

- **后端** `store.go`：① InsertMessage case sys —— sender_ref 以 "page:" 开头（页面访问）时**只落库、不刷 updated_at**，voice 来电(voice:*)/问候等其他 sys 照旧上浮（加 import strings）；② getLastMessagePreview SQL 加 `AND NOT (sender='sys' AND sender_ref LIKE 'page:%')`，侧边预览跳过页面访问、显示真正最后一句对话。
- **admin** `Console.vue` WSS onMessage 非当前会话分支：加 `isPageNav` 判断，page_navigation 不更新 updated_at/last_message/不上浮；新会话的纯页面访问也不触发 scheduleConvsRefresh。
- **App** `app_state.dart` _onEnvelope 当前+非当前两分支：page_navigation 仍 messages.add 显示在聊天记录，但不更新 lastMessage*/updatedAt/不上浮；新会话纯页面访问不 refreshConvs。

**业务流程对比**

| 场景 | 改动前 | 改动后 |
| --- | --- | --- |
| 访客访问一个页面 | 会话被顶到列表最上、时间改成访问时刻 | 会话**纹丝不动**，时间/排序保持最后一句对话 |
| 页面访问横幅 | 显示在聊天记录 | 仍显示（橙色横幅，不变） |
| 侧边最新消息预览 | 可能显示「访客访问了 X」 | 显示真正最后一句对话 |
| 访客来电(voice) | 上浮 | 仍上浮（来电是真实活动，不受本次影响） |

**触发场景与边界 + 验证方式**

触发：访客在网站翻页（page_navigation）→ 会话列表的该会话时间/排序不变。
边界：只认 sender_ref 前缀 "page:"（页面访问专属），voice:*/问候等其他 sys 不受影响仍正常上浮；当前会话打开时页面访问横幅照常显示在聊天记录。
验证：后端 `go build/vet` PASS；App `flutter analyze` 零新增 error/warning（仅既有 info）；Web `vite build` PASS（✓ 7.99s）。手测：访客连续翻几页 → 客服端该会话在列表的位置和时间都不动；访客发文字/来电 → 正常上浮。

---

## [071] 2026-06-01 18:32 — 「已联系」口径放宽：访客来电也算（含秒挂）· v0.6.7

**起因 / 需求**

爷爷反馈：访客「上来直接打电话」（一进来就拨客服、没发文字）也是主动联系，应该出现在「已联系」列表，别让客服漏掉来电访客。这与 [067] 相反——[067] 当时把 voice 通话事件从「已联系」去掉（怕「访客只点来电秒挂」误判）。

**决策（爷爷拍板）**

用 AskUserQuestion 跟爷爷确认边界（来电 cancel 秒挂怎么算）：① 排除秒挂其余都算 / ② 有来电就算（含秒挂）/ ③ 只有接通才算。**爷爷选 ②「有来电就算」**——最不漏人，访客只要拨打过客服电话（含秒挂取消）都算已联系，不区分结果。宁可多显示也别漏来电访客。

**根因 / 数据事实**

voice 通话事件以 sys 消息落库（`sender='sys'`, `sender_ref='voice:'+reason/code`，service.go OnVoiceCallFinished），voice 全是访客拨客服、无客服外呼，故「有 voice 消息」即「访客打过电话」。[067] 把 EXISTS 收紧到只认 `sender='visitor'`，导致来电访客不算已联系。

**改了什么（三端齐改）**

> 修改文件 3 个（backend 1 + admin 1 + mobile_app 1），升版本号 0.6.6 → 0.6.7

- **后端**（核心，决定列表）：`store.go` ListOpenConversations 的 has_visitor_msg EXISTS 加回 voice —— `m.sender='visitor' OR (m.sender='sys' AND m.sender_ref LIKE 'voice%')`。LIKE 'voice%' 兼容 [069] 的 "voice:reason" 格式，走 idx_conv_time 索引。
- **admin**（实时翻牌）：`Console.vue` WSS onMessage 当前/非当前两分支，收到 `extra.kind==='voice_finished'` 时把 has_visitor_msg 实时翻 true（不计未读、不外加提示音）。
- **App**（实时翻牌）：`app_state.dart` _onEnvelope 同样两分支，`kind=='voice_finished'` 翻 hasVisitorMsg=true。

isContacted（admin/mobile 都信后端 has_visitor_msg 字段）不用改——后端字段已含 voice，自动生效。

**业务流程对比**

| 场景 | 改动前（[067]） | 改动后（[071]） |
| --- | --- | --- |
| 访客上来直接打电话（任何结果） | 不算已联系、不进列表 | 算已联系、进列表 |
| 访客来电秒挂取消(cancel) | 不算 | 也算（爷爷：有来电就算） |
| 访客发文字 | 算（不变） | 算（不变） |
| 客服端实时性 | 来电不翻牌 | 来电瞬间翻「已联系」、不用刷新 |

**触发场景与边界 + 验证方式**

触发：访客拨打客服电话（接通/未接/拒接/忙线/秒挂任一结果）→ 该会话即算已联系。
边界：voice 消息 sender_ref 形如 "voice:hangup/no_answer/rejected/busy/cancel/..."，LIKE 'voice%' 全覆盖；后端 SQL 决定刷新/拉取口径、前端 WSS 翻牌决定实时性，两者一致。
验证：后端 `go build/vet` PASS；App `flutter analyze` 零新增 error/warning（仅既有 info）；Web `vite build` PASS（✓ 8.15s）。手测：访客拨打后秒挂 → 客服端该会话立即进「已联系」筛选。

---

## [070] 2026-06-01 17:59 — 进会话不再转圈：三端消息本地缓存 + 增量同步（微信级秒显）· v0.6.6

**起因 / 需求**

爷爷反馈：iOS 客服 App 和 web 客服工作台，每次点进某个会话，都要先转几秒「加载消息中…」才显示聊天记录，跟微信那种「点进去立刻看到历史、再悄悄同步新消息」完全不一样，要求改成秒显。

**根因分析（三端代码 + 索引坐实，非凭印象）**

1. 后端不慢：messages 表有 `idx_conv_time(conv_id, created_at)` 索引（001_init.sql），单会话查最近 50 条毫秒级。
2. 慢在前端架构 + 跨境网络：服务器在东京/美国，App/web 每次进会话都同步发一次 HTTP 到海外（RTT 几百 ms ~ 1-2s），且前端**无本地缓存**——App `openConv` 一进来 `messages.clear()` 清空再拉（app_state.dart），web `loadMessages` 每次 `http.get` 整个替换（Console.vue），网络稍慢必然白屏转圈。

爷爷拍板「B 档：微信级，冷启动也秒显」。持久化选型复用 App 已有 shared_preferences、Web 的 localStorage，**不引入 Hive/IndexedDB native 依赖**（避免给 iOS 签名/Pods build 添雷）。

**改了什么 / 加了什么 / 删了什么**

> 修改文件 8 个（backend 2 + mobile_app 4 + admin 2），升版本号 0.6.5 → 0.6.6
> 新增：消息接口 after 增量参数 / App 消息按会话缓存+持久化 / Web 消息缓存+持久化 / 三端进会话秒显。按「最近 60 会话 × 每会话 200 条」+ LRU 淘汰防膨胀。

- **后端**：`store.go` ListMessages 签名加 afterID + after 分支 SQL（`created_at >= (子查询 afterID 的 created_at) ORDER BY ASC`，秒级精度用 >= 防漏同秒、重复交前端 id 去重，走 idx_conv_time）；`http.go` handler 读 after query。向后兼容。
- **App**：`models.dart` Message 加 toCacheJson；`http_client.dart` listMessages 加 after；`settings.dart` 加 getCachedMessages/setCachedMessages(LRU)/clearMessageCache（shared_preferences，key 前缀 msgs:）；`app_state.dart` messages 单例→指向 _msgCache[convId] 字典，openConv 三段式（内存秒显→持久化垫底→后台增量/全量 merge+落盘）、_mergeMessages（id 去重 + 乐观 local- 按 agent+content+media+时间≈120s 确认清理）、_persist（只落真实消息）、closeActive 保留缓存+落盘、logout/setBackend 清缓存。
- **Web**：`Console.vue` 加缓存层（msgCache + localStorage cs_msgs: + LRU + mergeMessages + lastRealId）、loadMessages 三段式、WSS onMessage 当前会话 push 后 saveCachedMsgs 实时落盘；`session.js` clear() 登出清 cs_msgs:。

**业务流程对比**

| 场景 | 改动前 | 改动后 |
| --- | --- | --- |
| 进进过的会话 | 清空→转几秒「加载消息中」→才显示 | 立刻显示缓存历史（0 转圈），后台只拉增量 |
| 杀进程/刷新页面后再进 | 同样从零拉、白屏转圈 | 读持久化缓存立刻显示，再增量同步 |
| 网络慢/海外 RTT 高 | 转圈时间随网络拉长 | 首屏不受网络影响，同步在后台 |
| 自己刚发的消息 | 仅本地乐观显示 | 乐观显示不变；增量拉到真实消息后按内容去重，不重复 |

**触发场景与边界 + 验证方式**

触发：App/web 点进任一会话；切走再切回；杀进程/刷新后重进。
边界：乐观 local- 持久化不存、merge 时已确认的丢弃未确认的保留（防重复）；after 用 >= 多带回同秒消息由 id 去重；LRU 60 会话×200 条防膨胀；换账号/换服务器清内存+持久化缓存（延续 [068] 防串台铁律）；App openConv 用 reqConvId 快照守卫、merge 只动目标 convId 缓存。
验证：后端 `go build ./... && go vet ./...` PASS；App `flutter analyze`（4 文件）零新增 error/warning（仅既有 info）；Web 本地 `npm install && vite build` PASS（vite ✓ built 9.97s）；串台「绕过」自测：A 会话发消息途中切 B → 消息落 A 缓存、B 视图不污染、切回 A 秒显不重复。

---

## [069] 2026-06-01 12:40 — iOS 客服 App 接听后 17s 无声→修复 race / 异常静默 + backend 5s 看门狗 + voice_finished reason · v0.6.5

**起因 / 需求**

集成方 [073] 工单反馈：iOS 客服 App 收到来电浮窗 → 客服点「接听」→ 浮窗显示「通话中」但访客端**完全听不到客服声音**，访客端 17 秒后被 ICE 心跳超时强制断线，客服 App 端始终停留在「通话中」状态不返回任何错误提示。爷爷原话「这种沉默挂死最恶心，必须修，而且必须给前端落实文案让客服知道是哪一步炸了」。

**根因分析（三层叠加）**

1. **mobile `_onOffer` 异常静默吞**：`voice_controller.dart` 的 `setRemoteDescription / createAnswer / setLocalDescription` 三步合在一个 try/catch 里，任意一步抛错都被外层 catch 静默吞掉，既不上报后端、也不弹 UI、PC 状态机半死不活 → 访客 17s 后 ICE 超时才间接发现
2. **APNs 冷启 race（关键修复）**：iOS 推送唤醒 → flutter engine 重启 → `voice_offer` 信令先到 → 此时 `_pc==null`，`_onIce` 收到对端 ICE candidate 直接 drop（旧代码 `if(_pc==null) return`）→ 等 accept() 真正 createPeerConnection 时早期 ICE 已永久丢失 → DTLS 永远握不上 → 单向静音
3. **mic preflight 缺失**：accept() 直接 `getUserMedia` 失败（权限拒/被占用/硬件故障）只在 `try{...}catch` 里 debugPrint 一行就 return，后端从未感知，看门狗也没启动 → 服务端以为「客服已 accept 进入通话」、访客端 UI 显示「通话中」、实际啥都没发生

**改了什么 / 加了什么 / 删了什么**

> 修改文件 3 个（mobile_app 1 + backend 2），同步升版本号 0.6.4 → 0.6.5
> 新增功能 4 个（5s 看门狗 / voice_signal_error 上报 / voice_accept_failed 上报 / voice_finished reason 中文文案）/ 删除功能 0 个 / 修改功能 6 个（_onOffer 三阶段独立 try/catch、accept() mic preflight、_prepareForIncomingCall + _earlyIceQueue、hub.go voice_accept/answer/end/reject、service.codeToText 签名、OnVoiceCallFinished 签名）

### Patch 1 — `mobile_app/lib/state/voice_controller.dart` `_onOffer` 三阶段独立 try/catch

- 入口先做 sdp 空值校验，缺失立即 `sendEnvelope('voice_signal_error', {phase: 'parse_offer', reason: 'empty_sdp', call_id, agent_id})` + 终止
- `setRemoteDescription` / `createAnswer` / `setLocalDescription` 各自独立 try/catch，每个 catch 块都 `debugPrint('[voice] phase=X err=$e\n$st')` + `sendEnvelope('voice_signal_error', {phase, reason, call_id, agent_id})`
- 新增 `create_pc` 兜底 try/catch（_prepareForIncomingCall 复用同一路径）

### Patch 2 — `mobile_app/lib/state/voice_controller.dart` `accept()` mic preflight

- accept() 顶部立即 `getUserMedia({audio: true})` preflight；失败立刻 `sendEnvelope('voice_accept_failed', {reason, detail, agent_id})` 然后 `_end()` + return
- 新增 `_classifyMicError(e)` 把多平台异常归一到 5 种 reason：`mic_permission_denied / mic_busy / mic_hardware_error / no_audio_tracks / mic_unknown`（兼容 iOS PlatformException / Android SecurityException / Web NotAllowedError 字符串差异）

### Patch 4 — `mobile_app/lib/state/voice_controller.dart` APNs 冷启 race 修复

- 新增 `_prepareForIncomingCall()`：来电信令一到立即 `createPeerConnection` + 注册 `onIceCandidate / onTrack / onConnectionState / onIceConnectionState` 回调 + 置 `_pcReady = true`
- 新增 `_earlyIceQueue` List 缓存早到 candidate；`_onIce` 在 `_pc==null` 时改为入队不丢
- `setRemoteDescription` 成功后立即 flush 队列：`for c in _earlyIceQueue: await _pc!.addCandidate(c)`
- `_onIncoming` 末尾自动 `catchError` 调 `_prepareForIncomingCall`；`accept()` 与 `_onOffer` 入口幂等再创（_pcReady 守卫）
- 防御性硬化：所有 `await _pc!.xxx`（addTrack / addCandidate / setRemote / createAnswer / setLocalDescription）全部包独立 try/catch + debugPrint；`_cleanup` 内 `pc.close` 与 `track.stop` 也包 try/catch；非致命错误降级为日志不击穿状态机
- 新增 `_sendSignalError / _onRemoteFinished / _reasonToText` 辅助：`voice_finished` 远端下发立刻关浮窗 + 中文文案（9 种 reason enum 跟 backend `codeToText` 对齐）；`handleSignal` 新增 `case 'voice_finished'`

### Patch 3 — `backend/internal/ws/hub.go` 5s 看门狗 + voice_signal_error/voice_accept_failed 处理

- Hub struct 新增 `pendingAccepts sync.Map` + `acceptTimers sync.Map`（复用现有 `finishedCalls` 模式）
- 新增 `pendingAccept` struct + `acceptAnswerTimeout = 5 * time.Second` 常量
- `case voice_accept` 末尾：`pendingAccepts.Store(callID, …)` + `time.AfterFunc(5s, fireAcceptWatchdog)`；重复 accept 先 `Stop` 旧 timer
- `case voice_answer`：立刻 `acceptTimers.LoadAndDelete` + `Stop` + `pendingAccepts.Delete`，正常握手分支
- `case voice_end / voice_reject`：同样取消看门狗；并把 `envelope.Extra.reason` 抽出（缺失走 `normal_hangup`）传给 sink
- 新增 `fireAcceptWatchdog`：`LoadAndDelete` dedup + `finishedCalls` 二次 dedup + 同时 fanout `voice_finished` 给 visitor 和 agent + 调 `sink.OnVoiceCallFinished(visitorID, callID, code, reason, durSec)`
- 新增 `extractReason` 辅助函数

### Patch 5 — `backend/internal/service/service.go` `codeToText` / `OnVoiceCallFinished` 加 reason

- `codeToText` 签名扩展为 `(code, reason, durSec)`：优先按 reason 渲染 9 种中文（`agent_no_answer_5s / mic_permission_denied / mic_busy / mic_hardware_error / no_audio_tracks / signal_exception / no_answer_sdp / no_ice_candidate / ice_disconnected`）；`reason=normal_hangup` 或空 → 走 code 旧文案（no_answer / rejected / busy / cancel / failed / hangup）；未知 reason 兜底带括号显示原 enum
- `OnVoiceCallFinished` 签名加 `reason string` 参数；`SenderRef` 从固定 `voice` 升级到 `voice:reason`（normal_hangup 走 code），admin REST 历史回放可以基于 `SenderRef startsWith voice:` 做正则识别
- `Envelope.Extra` 新增 `reason` 字段透传给前端，前端可直接读 `env.extra.reason`
- bizLog 增加 `reason` 字段

**业务流程对比**

| 场景 | 改动前 | 改动后 |
| --- | --- | --- |
| 客服点接听但 mic 权限拒 | UI 显示「通话中」，访客 17s 后 ICE 超时强断，客服端永不弹错 | accept() 入口立即弹「未授予麦克风权限」+ 后端 `voice_accept_failed` 落库 + 立即关浮窗 |
| iOS APNs 冷启接到来电 | offer 早到 + PC 未 ready + ICE candidate 静默 drop → DTLS 永远握不上 → 17s 单向静音 | `_prepareForIncomingCall` 立即建 PC + `_earlyIceQueue` 缓存 → setRemote 后 flush → 正常握手 |
| `createAnswer` 抛错 | 外层 catch 吞掉，无任何提示 | 三阶段独立 catch，每步上报 `voice_signal_error{phase, reason}` + 后端 5s 看门狗到点 fanout `voice_finished` |
| 客服按接听 5s 内没回 answer | 访客端等到 17s ICE 超时才断 | 后端 5s 看门狗到点 fanout `voice_finished{reason: agent_no_answer_5s}` + 双端中文文案「客服 5 秒未应答」+ 落库 |

**触发场景与边界 + 验证方式**

触发场景：
- 真机复现 iOS 客服 App 关后台 → 访客发起 voice 来电 → APNs 唤醒 → 客服点接听 → 期望 5s 内必有结果（成功通话 / 明确文案错误）
- 真机 mic 权限关闭 → 客服点接听 → 立刻弹「未授予麦克风权限」
- 真机 mic 被其他 app 占用 → 立刻弹「麦克风被占用」
- 真机正常通话中互相挂断 → reason=normal_hangup → 走旧文案不带括号 enum

边界：
- 客服重复点接听：`pendingAccepts.Store` 覆盖 + `Stop` 旧 timer，5s 重新计时（幂等）
- voice_answer / voice_end / 5s timer 三方竞争同一 callID：`LoadAndDelete` 原子保证只有一方走 fanout，`finishedCalls` 5min dedup 二次保险
- voice_finished envelope.Extra 缺失 reason：走 normal_hangup 旧文案兼容旧前端
- _earlyIceQueue 在 _cleanup 时 `.clear()` + `_pcReady=false`，避免下次来电脏数据

验证方式：
- `go build ./... && go vet ./...` PASS（exit 0，无 warning）
- mobile 端 685 行手工 grep 验证：5 处 `await _pc!.xxx` 全部包 try/catch 保护
- 真机脚本：① 关 mic 权限点接听 → 看 `/srv/cs-data/logs/biz/*.jsonl` 应有 `voice_accept_failed reason=mic_permission_denied` 一行；② kill iOS App 后访客拨 → 接听 → 后台日志应看到 `voice_signal_error phase=…` 或正常握手；③ accept 后 backend 故意丢 answer → 5s 看门狗触发 → 双端弹「客服 5 秒未应答」
- admin REST 历史回放：会话消息 `sender_ref` 应为 `voice:agent_no_answer_5s` 形式可被前端识别

---
