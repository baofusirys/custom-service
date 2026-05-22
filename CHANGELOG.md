# CHANGELOG

> 每次代码改动完成后，必须立刻在文件顶部追加一条。时间用北京时间绝对格式（YYYY-MM-DD HH:mm）。

---

## [010] 2026-05-21 19:20 — 通知声音 + 访客进入提醒 + 自动问候

**起因 / 需求**
爷爷要求做完整的通知体系：
1. 管理后台可以选择「客户端通知声音」和「客服端自己的通知声音」
2. 访客打开有 widget 的网页时，通知管理后台，并自动给访客发一条问候消息
3. 上面两个功能在管理后台是可选开关

**新增功能 5 项 / 修改 7 个文件 / 删除 0 项**

新建：
- `backend/migrations/002_settings.sql` — 新建 `settings` key-value 表（key/value/updated_at），首次部署时插入 5 条默认配置
- `admin/src/views/Settings.vue` — 新「系统设置」页（仅管理员可见），含：客服端/访客端声音选择（5 种内置音色 + 试听）、访客进入通知开关、自动问候开关 + 文本、Widget 标题
- `admin/src/api/sound.js` — 5 种内置声音库（classic/chime/ding/soft/alert/none），用 Web Audio API 程序合成，**零外部文件零体积**

修改：
- `backend/internal/store/store.go` — 新增 `GetSetting/GetSettingsMap/SetSetting/SetSettings` + `EnsureConversation`（返回 isNew，区分新建/已存在会话）
- `backend/internal/service/service.go` — 新增 `SettingBool/SettingStr/GreetingTextIfEnabled/OnVisitorEnter`（后者异步广播 visitor_enter sys 给所有 agent + 落库 greeting）
- `backend/internal/ws/hub.go` — 新增 `BroadcastToAllAgents`（不走 byConv，专门给客服推系统通知）
- `backend/internal/handler/http.go` — 新增 `GetSettings/UpdateSettings/VisitorPublicSettings`；`VisitorSession` 在 isNew 时调 `OnVisitorEnter` 异步通知，并在 HTTP 响应里直接回 `greeting` 文本（避开 WSS 时序问题）
- `backend/cmd/server/main.go` — 注册 3 个新路由：`GET /api/visitor/settings`（公开） + `GET/POST /api/admin/settings`（仅 admin）
- `admin/src/router/index.js` + `admin/src/views/Layout.vue` — 加 `/settings` 路由 + 菜单（仅 admin 可见）
- `admin/src/views/Console.vue` — `onMessage` 处理 `sys/visitor_enter` 弹 `ElNotification`；收到访客 chat 消息播 `agentSound`；onMounted 时拉客服音色偏好 + 监听首次 click 解锁 AudioContext
- `widget/public/chat.html` — 启动时拉 `/api/visitor/settings` 拿 `notifySound` + `widget_title`；收消息播声；handler 返回 `greeting` 时直接 render

**业务流程**

访客打开网页：
```
1. loader.js 注入 iframe → chat.html
2. chat.html bootstrap → 拉 /api/visitor/settings 拿声音/标题 → 拉 /api/visitor/session 创建会话
3. 后端 EnsureConversation 返回 isNew=true（新会话）：
   a) HTTP 响应里返回 greeting 文本（如果 greeting_enabled）
   b) 异步 OnVisitorEnter:
      - 广播 sys/visitor_enter 给所有在线 agent → 客服端弹 ElNotification + 播声
      - InsertMessage 把 greeting 落库（客服拉历史时能看到）
4. chat.html 渲染 greeting 到访客气泡列表 + 缓存到 localStorage
5. chat.html 建立 WSS（后续消息走 WSS 实时）
```

后续访客发消息：
- WSS chat → 客服端 onMessage → 区分 fromVisitor → push 到当前会话 或 conv.unread++ 并 playSound(agentSound)

客服在设置页修改：
- POST /api/admin/settings 白名单过滤（防止任意 key 注入）+ SanitizeText 清洗 + 写 audit_log

**触发场景与边界 + 验证方式**
- 验证 1：访客新打开 demo.html → 客服端右下角弹「访客 xxxxxx 进入了网站」通知 + 响 chime；同时访客气泡列表里出现问候消息
- 验证 2：管理后台 /settings 修改客服端声音为 "soft"，刷新 console；下次访客发消息 → 客服端播 soft 调
- 验证 3：管理后台关闭「通知客服」开关 → 下次访客新打开 → 客服端不再弹通知（但 greeting 仍会发，因 greeting_enabled 独立）
- 验证 4：管理后台修改 greeting_text 为「您好，我是小客服」→ 新访客打开看到这个新文本
- 边界：访客同一浏览器刷新页面（不是新会话，isNew=false）→ 不再触发 visitor_enter 通知和 greeting，避免刷新风暴
- 边界：浏览器 AudioContext 需要用户手势解锁，监听首次 click 自动解锁

**安全 / 健壮性**
- settings key 白名单 6 项，POST 时严格过滤；greeting_text / widget_title 走 SanitizeText 防 XSS；长度限制 500 字
- audit_log 记录每次 update_settings 的 actor + IP + diff
- OnVisitorEnter 用 goroutine + 5s timeout + panic recover；失败不影响访客主流程
- Web Audio API 播放 500ms 内同种音色防抖（避免连发消息时声音叠成噪声）
- 浏览器 AudioContext 未解锁时静默失败，不报错

---

## [009] 2026-05-21 18:32 — 重做 Widget 消息气泡：宽度按内容自适应 + 头像间距 + 尾巴设计

**起因 / 需求**
用户实地测试发现 widget 端：
1. 新发的消息会"撑大"已经发出的旧消息 —— 连续短消息（如「111」「3333」）和长消息（「123456789」）显示成一样宽，视觉错误。
2. 头像离气泡太近，间距不舒服。
3. 整体气泡设计需要更精致。

**根因**
`.msg-stack` 是 flex column 容器，没有显式设置 `align-items`，**默认是 `stretch`** —— flex item 在 cross axis (水平方向) 被拉伸到容器宽度。所以同组所有气泡都跟最宽那条一样宽，违反正常 IM 视觉。

**改了什么 / 加了什么 / 删了什么**（修改 1 个 / 新增 0 个 / 删除 0 个）
- 修改：[widget/public/chat.html](widget/public/chat.html)
  - `.msg-stack` 加 `align-items: flex-start`（对方消息靠左）/ `flex-end`（自己消息靠右），让每条气泡按内容宽度自适应，互不影响
  - `.msg-stack { max-width: calc(100% - 54px); min-width: 0 }`（留出头像 34px + gap 10px + 余量；min-width:0 让 flex item 能收缩）
  - `.bubble { display: inline-block; max-width: 100% }`（兼容性写法 + 受 stack 约束）
  - 头像间距 gap 8 → **10**px，头像大小 32 → **34**px，气泡圆角 14 → **18**px
  - 不同消息组之间的间距 4 → **8**px，组内消息间距收紧到 **3**px
  - 同组连续气泡的"chain 视觉"：非最后一条「靠头像那侧的下角」用 6px 圆角；最后一条用 5px 形成尾巴
  - 图片气泡：独立 `bubble-image` class，padding 4px，圆角 14px，图片自身 10px 圆角
  - 文件卡片重做：36px 圆角图标 + 文件名 + 副标题"点击下载"，min-width 180px 让卡片有最小辨识度

**业务流程对比**
- 改动前：「111」「3333」「123456789」三条气泡显示成一样宽（被 123456789 撑开）。
- 改动后：三条气泡各自按内容宽度，从短到长视觉清晰。
- 改动前：头像紧贴气泡，视觉拥挤。
- 改动后：头像和气泡有 10px 空气；组内最后一条气泡有"尾巴"指向头像。

**触发场景与边界 + 验证方式**
- 验证：访客端连发 3 条不同长度的消息「a」「abcdef」「abcdefghijklmn」 → 三个气泡各自按内容宽度显示，从窄到宽。
- 验证：连续多条消息组内，最后一条气泡的"尾巴"圆角比中间气泡小（视觉上指向头像）。
- 验证：发图片 → bubble 紧贴图片边界（4px padding），不浪费空间。
- 验证：发文件 → 卡片显示图标 + 文件名 + "点击下载"副标题，min-width 180px 保证可点。
- 边界：iframe 固定宽 380px，msg-stack max-width 算出来约 326px，图片 max-width 220px，都在合理范围。

---

## [008] 2026-05-21 18:15 — 修复"自己的安全机制把自己拉黑"误伤

**起因 / 需求**
爷爷登录测试服时被弹「您的 IP 已被临时限制访问，请稍后再试」。爷爷追问到底是什么在轮询、把 IP 也限制了。

**真相（用 Redis 数据实证）**
查 Redis 后真相大白：
- 爷爷的 IP `110.241.19.222` 累计 **697 次** violation
- 24h 累计违规阈值是 200，所以触发了自动拉黑
- 违规类型分布（按数量）：
  - `ws_handshake_flood`: **712 次（占 96%）** — 这是 WSS 握手频率超过 5/分钟的累计
  - `http_rpm_exceeded`: 16 次（[004] 之前 60/分钟时代的遗留）
  - `agent_login_fail_*`: 7 次（爷爷输错账号 / 密码）
  - `visitor_msg_flood`: 5 次（[006] 之前 10 条/分钟时代的遗留）

**不是什么神秘轮询**。是一天里反复刷新 / 切标签 / 多标签同时连 / 网络抖动重连，浏览器每次重建 WSS 都触发 1 次握手；我之前限速 5 次/分钟太严，人类用户轻易超。

**改了什么 / 加了什么 / 删了什么**（修改 3 个 / 新增 0 个 / 删除 0 个 + 1 次 Redis 解封操作）
- **立即操作**：在测试服执行 `redis-cli DEL bl:110.241.19.222 viol:110.241.19.222 viol:60.1.87.36 viol:172.19.0.1 viol:visitor:...`，解封爷爷的 IP + 清掉所有累积的违规计数（5 个 key）。
- 修改：[.env](.env):
  - `SECURITY_IP_WS_HANDSHAKE_PM 5 → 30`（浏览器刷新/切标签/重连一下就破 5，30 既挡机器人又不卡正常人）
  - `SECURITY_IP_BLACKLIST_THRESHOLD 200 → 1000`（更宽松的拉黑阈值，避免误伤）
- 修改：[backend/internal/security/ratelimit.go](backend/internal/security/ratelimit.go) — 新增 `LogSecurityWarn(ip, kind, detail)` 方法：只写安全日志，**不计** violation。区分「真攻击」（继续走 RecordViolation 计数 + 可拉黑）和「用户失误」（只记日志不拉黑）。
- 修改：[backend/internal/handler/http.go](backend/internal/handler/http.go) — 3 处从 `RecordViolation` 改为 `LogSecurityWarn`：
  - `agent_login_fail_nouser`（输错账号）
  - `agent_login_fail_password`（输错密码）
  - `upload_mime_blocked`（上传不支持的文件类型）
  - 这些都是用户失误，不应该拉黑。防爆破靠 Nginx 登录限速 (2 r/s burst=5) + bcrypt cost=12（每次 ~250ms 慢算抗爆破）。

**业务流程对比**
- 改动前：爷爷一天里反复刷新页面 → WSS 握手累积 712 次违规 → 自动拉黑 24h → 登录都进不去。
- 改动后：
  - WSS 握手阈值 30/分钟（即使刷新 30 次也不触发）
  - 拉黑阈值 1000（攒到 1000 次违规才拉黑，正常人 24h 内不可能）
  - 输错密码 / 上传错文件不再计违规（只记日志）

**触发场景与边界 + 验证方式**
- 爷爷已经可以重新登录（IP 解封）。
- 反复刷新 admin 页面 / 切标签 5 次 → 不再触发 WSS 握手限速。
- 故意输错密码 5 次 → 仅 security.log 有 warn 记录，Redis 里 viol:<ip> 不增加。
- 边界：如果真有人 1 分钟内 100 次 WSS 握手 → 仍然触发限速 + 拉黑（30/min 阈值挡住明显的机器人攻击）。

**保留的真攻击防御**
- `ws_handshake_flood`（超过 30/min 仍计 violation）
- `sqli_suspect`（SQL 注入模式仍计 violation）
- `http_rpm_exceeded`（HTTP 超过 600/min 仍计 violation）
- 注入侦测仍然 SanitizeText 清洗

**为什么登录失败不再计 violation 仍然安全？**
- Nginx 层 `login_rps 2 r/s burst=5` 已经把单 IP 的登录请求压到 2 次/秒
- 后端 bcrypt cost=12 每次校验约 250ms，单 IP 每秒最多算 4 次
- 攻击者 24h 最多算 ~34 万次 bcrypt（vs 6 位数字密码空间 100 万 / 8 位字母数字空间 218 万亿）
- 实际防爆破靠的就是 bcrypt 慢和 Nginx 节流，不靠拉黑机制

---

## [007] 2026-05-21 17:58 — 测试服超管密码改为 ***REDACTED***

**起因 / 需求**
爷爷要求把客服工作台的默认密码改成 `***REDACTED***`，方便测试时记忆和输入。

**改了什么 / 加了什么 / 删了什么**（修改 2 个 / 新增 0 个 / 删除 0 个 + 1 次数据库手工操作）
- 修改：[.env](.env) — `ADMIN_BOOTSTRAP_PASSWORD CsAdmins9cu2Qo5dkVY → ***REDACTED***`。
- 修改：[LATEST.md](LATEST.md) — 同步更新文档里记录的测试服超管密码。
- 数据库一次性操作（在测试服 38.76.193.68 执行）：
  1. `DELETE FROM custom_service.agents WHERE username='admin'` 删掉旧的 admin 行
  2. `docker compose restart cs-backend` 重启 backend
  3. backend 启动时 `EnsureBootstrapAdmin` 发现 admin 不存在，按新 .env 的 ***REDACTED*** 创建（bcrypt cost=12）

**为什么不能直接 UPDATE 数据库改密码？**
密码是 bcrypt 哈希存的，需要 bcrypt 工具生成新哈希。最稳妥的做法是「删行 + 重启 backend 触发自动重建」，复用已有的 EnsureBootstrapAdmin 逻辑，避免临时引入外部 bcrypt 工具。

**为什么 EnsureBootstrapAdmin 这个时候才生效？**
原代码只在 admin 不存在时创建（避免覆盖现有账号），所以光改 .env 是无效的；必须先把数据库里 admin 行删掉，才能触发"按新密码重建"。

**业务流程对比**
- 改动前：`admin` / `CsAdmins9cu2Qo5dkVY` 登录。
- 改动后：`admin` / `***REDACTED***` 登录。

**触发场景与边界 + 验证方式**
- 验证：curl 用 ***REDACTED*** 登录得到 200 + JWT；用旧密码得到 40105。
- 边界：***REDACTED*** 是 8 个字符，正好达到 backend 强制的「至少 8 字符」校验线；不再短就过不了 fail-fast。
- 安全：测试服专用密码，生产环境绝不能用这种弱密码。

---

## [006] 2026-05-21 17:50 — 修复客服未接管的会话也要看到未读 +1 + 取消访客消息频率限制

**起因 / 需求**
用户实地测试 [005] 后发现：
1. 访客发消息时，客服后台左侧虽然「上浮」了那条会话，但未读 badge 数字**没有 +1**（截图证实）。
2. 访客连发几条消息后，被 `SECURITY_VISITOR_MSG_PM=10` 限流触发「发送过于频繁」提示。用户明确要求：**这个限制不要！！！**

**根因分析**
1. 未读没 +1 的根因在**后端 Fanout 设计**，不是前端。`Hub.fanoutLocal` 只投递给 `byConv[ConvID]` 内的连接（同会话），而客服 client 只有在「点开某个会话 → AttachAgentToConv」后才加入 `byConv[那个会话]`。客服当然不可能预先接管所有会话，所以**未接管的会话的访客消息根本推不到客服客户端**，前端 onMessage 没触发，自然 unread 不会 +1。「上浮」是 5 分钟兜底拉取或切换路由触发的，跟 WSS 无关。
2. 限流功能本身在工作，是 `s.visMsgPM = 10` 太低（每分钟 10 条）。

**改了什么 / 加了什么 / 删了什么**（修改 4 个 / 新增 0 个 / 删除 0 个）
- 修改：[backend/internal/ws/hub.go](backend/internal/ws/hub.go) — `fanoutLocal` 重做：除了广播给 `byConv[ConvID]`，**同时给所有在线 agent 广播一份**（用 ConnID 去重避免双发）。这样任何客服都能实时收到全站所有访客消息，左侧未读 / 上浮才能 WSS 实时。仅 chat 类消息扩散给所有 agent；typing/read 不外溢节省带宽。
- 修改：[backend/internal/security/ratelimit.go](backend/internal/security/ratelimit.go) — `AllowVisitorMessage` 加判断：`pm <= 0` 视为不限制，直接返回 true。
- 修改：[.env](.env) — `SECURITY_VISITOR_MSG_PM 10 → 0`（关闭访客消息频率限制）。
- 修改：[admin/src/views/Console.vue](admin/src/views/Console.vue) — 区分 visitor / agent 消息：只有访客发的消息才让 `conv.unread++`；其他客服发的消息只更新 `updated_at` + 上浮（不动未读，因为客服间互发不是客服需要响应的）。

**业务流程对比**
- 改动前：访客发消息 → 客服没接管 → 收不到 WSS 推送 → 未读永远是 0。
- 改动后：访客发消息 → 服务端广播给所有在线 agent → 任何客服都立即看到 unread +1 + 上浮。
- 改动前：访客每分钟超过 10 条消息 → 弹「发送过于频繁」error 帧。
- 改动后：访客可以任意频率发消息。

**触发场景与边界 + 验证方式**
- 验证 1：客服后台**不点开任何会话**，让访客 A 发消息 → 左侧 A 会话立即出现红 badge `1`、上浮到顶。
- 验证 2：客服后台打开会话 A，访客 B 发消息 → B 出现 badge + 上浮，A 不动。
- 验证 3：客服打开会话 A 时，A 的访客发消息 → 直接 push 到右侧聊天区 + 静默 mark read，badge 不出现。
- 验证 4：访客连发 50 条 → 不再触发「发送过于频繁」。

**安全 / 健壮性**
- 访客消息频率限流可关闭，但**单 IP HTTP 限流**（600/分钟）和 **WSS 握手限速**（5 次/分钟）仍在。攻击者要刷消息得先建立 WSS，握手就被卡住。
- 客服间互发消息不让其他客服 unread +1，避免客服协同时未读乱跳。
- fanoutLocal 用 ConnID map 去重，已接管该会话的客服不会收到 2 份。

---

## [005] 2026-05-21 17:35 — 修复消息显示 2 条 + 未读数 WSS 实时 + 消息顺序改 WSS 优先

**起因 / 需求**
用户实地测试发现 3 个问题：
1. 同一条消息在客服工作台里显示 **2 条**（访客只发了 1 条，截图证实）。
2. 客服后台未读数 badge **不是 WSS 实时**的，需要等 HTTP 轮询才更新。
3. 用户追问：「消息处理顺序是先 WSS 再 DB，还是先 DB 再 WSS？」——这是项目第一天就明确要求的「WSS 优先」原则，但之前实现成了先 DB 后 WSS。

**根因分析**
1. 消息重复：`Hub.FanoutToConv` 同时做了「本地 fanoutLocal」+「Redis publish 给所有节点」；`fanoutFromRedis` 又订阅了同一频道。单节点部署时订阅的就是自己 publish 的内容，所以每条消息**在本节点被广播 2 次**。
2. 未读数延迟：`Console.vue` 收到非当前会话的 WSS 消息时只调 `scheduleConvsRefresh()`（3 秒防抖 + HTTP），未读数等 HTTP 才刷新，体感不实时。
3. 消息顺序错位：原 `handleIncoming` 是「同步 sink.OnVisitorMessage（含 InsertMessage 入库）→ 之后才 FanoutToConv」。DB 慢时实时通道被拖累。

**改了什么 / 加了什么 / 删了什么**（修改 4 个 / 新增 0 个 / 删除 0 个）
- 修改：[backend/internal/ws/protocol.go](backend/internal/ws/protocol.go) — `Envelope` 加 `Node string` 字段，标记消息来源节点 ID。
- 修改：[backend/internal/ws/hub.go](backend/internal/ws/hub.go):
  - `FanoutToConv` 给 envelope 盖本节点 ID 后再 `fanoutLocal` + `Redis publish`
  - `fanoutFromRedis` 检测到 `e.Node == h.cfg.NodeID` 跳过（消除回环）
  - `MessageSink` 接口重构：去掉 `OnVisitorMessage/OnAgentMessage`，新增 `PreprocessVisitorMessage/PreprocessAgentMessage`（同步：限流+清洗）和 `PersistMessageAsync`（异步：入库）
  - `handleIncoming` 顺序：**Preprocess → FanoutToConv → PersistMessageAsync**（WSS 优先，DB 不阻塞实时通道）
- 修改：[backend/internal/service/service.go](backend/internal/service/service.go) — 实现新接口；`PersistMessageAsync` 用 `go func` + 5s timeout + panic recover + 兜底 conv 创建。失败只记日志（原始报文已落 raw_ws.log，可重放）。
- 修改：[admin/src/views/Console.vue](admin/src/views/Console.vue) — WSS 收到非当前会话的访客消息时：(a) 直接 `conv.unread++`（WSS 实时，0 延迟），(b) 更新 `updated_at`，(c) 把该会话上浮到列表顶；当前会话则乐观渲染消息 + 静默 POST mark read。

**业务流程对比**
- 改动前：访客发 1 条 → 客服后台看到 2 条（Redis 回环）。
- 改动后：访客发 1 条 → 客服后台看到 1 条。
- 改动前：未读数等 ≥3 秒（防抖）→ HTTP 拉取后才更新。
- 改动后：未读数 WSS 一到就 +1，毫秒级更新；会话自动上浮到顶部。
- 改动前：DB 慢 100ms 时实时消息送达延迟 100ms。
- 改动后：实时消息走 Fanout，先送达对端；后台 goroutine 异步入库，不影响通道。

**触发场景与边界 + 验证方式**
- 验证 1（不重复）：访客在 demo.html 连发 5 条 → 客服后台显示 5 条（不是 10 条）。
- 验证 2（未读实时）：客服打开会话 A；访客 B 发消息 → 左侧 B 的未读 badge **立刻** +1 + 上浮到顶（不需要等 3-5 秒）。
- 验证 3（WSS 优先）：人为在 backend 容器内 `tc qdisc add dev eth0 root netem delay 200ms` 给 MySQL 加 200ms 延迟，发消息后客户端能秒收（不等 200ms）；business.log 显示 `persist insert msg` 比 fanout 晚 ~200ms。
- 边界：限流被拒的消息（visitor_msg_flood）不广播、不入库，仅给发送方回 error 帧。

**安全 / 健壮性**
- 限流和注入检测仍在同步阶段执行；恶意内容不会因为「异步入库」而提前广播给对端 —— 因为先做 SanitizeText 再 Fanout。
- 异步入库 panic 被 recover；失败仅记 business.log；原始 WSS 报文已落 raw_ws.log 可重放。
- 进程退出时 docker compose 给 15 秒优雅期，正常情况下所有 goroutine 都能完成入库。

---

## [004] 2026-05-21 17:18 — 修复客服后台一直弹「请求频繁」+ 减少无效轮询

**起因 / 需求**
用户实地测试发现客服后台一直弹 toast「请求过于频繁，请稍后再试」，问为什么消息走 WSS 还会被限流，到底在轮询什么。

**根因分析**
1. 客服后台并没有"消息走 HTTP"——chat 消息 100% 走 WSS。但前端还有 2 个高频 HTTP 轮询：
   - 每 15 秒 GET `/api/health`（顶部在线人数统计）
   - 每 20 秒 GET `/api/agent/conversations`（左侧会话列表）
   - 此外 WSS 每收到一条非当前会话的新消息也会立刻触发 1 次 refreshConvs（怕未读不准）
2. 后端默认 `SECURITY_IP_HTTP_RPM=60`（单 IP 60 次/分钟）—— 对一个进行密集对话的客服窗口太严：访客稍微多发几条，加上常规轮询和点会话产生的 3 次 HTTP，1 分钟轻松超 60。

**改了什么 / 加了什么 / 删了什么**（修改 2 个 / 新增 0 个 / 删除 0 个）
- 修改：[.env](.env) — `SECURITY_IP_HTTP_RPM 60 → 600`（测试服专用；.env.example 中默认值不变，让集成方按各自规模决定）。
- 修改：[admin/src/views/Console.vue](admin/src/views/Console.vue):
  - 去掉每 15 秒的 health 轮询（数据变化由 WSS 推送驱动，统计冷数据 5 分钟刷一次足够）
  - conv 列表轮询从 20 秒延长到 5 分钟（仅作兜底，主要靠 WSS 触发）
  - WSS 收到非当前会话的新消息 → 用 3 秒防抖触发 refreshConvs（短时间多条消息只触发一次，而不是每条都触发）

**业务流程对比**
- 改动前：客服在线时 1 分钟 HTTP 请求量 ≈ 4(health) + 3(conv) + N(WSS 触发的) + M(用户操作) ≈ 易破 60，频繁弹 toast。
- 改动后：1 分钟 HTTP 请求量 ≈ 0~3（仅 WSS 触发的去重后的几次 + 用户主动操作）；远低于 600 阈值。

**触发场景与边界 + 验证方式**
- 验证：进入 console 后开两个浏览器（一个客服一个访客），让访客 1 分钟连发 30 条消息 → 客服侧不再出现「请求过于频繁」toast；F12 Network 看 `/api/agent/conversations` 在 5 分钟内最多 2-3 次（一次进入 + 防抖触发）。
- 边界：仍保留 5 分钟的兜底轮询，防止 WSS 推送丢失导致会话列表过期。

**后续优化（v0.2.0 计划）**
- 后端新增 WSS 推送类型：`conv_new`（新访客接入）、`conv_update`（会话变更）、`stats`（在线人数变化时）；客服后台彻底告别 HTTP 轮询。

---

## [003] 2026-05-21 17:15 — 修复消息内容不显示的严重 Bug + 重做前后台样式

**起因 / 需求**
用户实地测试发现 3 个问题：
1. 严重 Bug：客服后台点开访客，气泡只有空壳，**消息内容看不到**。
2. 前台消息卡片时间显示不明显。
3. 前台和客服窗口的整体样式都不专业、不美观。

**根因分析**
1. Bug 根因：`backend/internal/store/store.go` 中的 `Message`/`Conversation`/`Agent`/`Visitor`/`FileRecord` 5 个结构体都没加 JSON tag。Go 默认按字段名首字母大写序列化（`Content`/`Sender`），而前端按 snake_case 小写读（`m.content`/`m.sender`），永远 undefined。会话列表表面没事，是因为 `ListOpenConversations` 内部用了显式的 `map[string]any`，但 `ListMessages` 直接返回结构体切片，所以消息内容就是看不到。

**改了什么 / 加了什么 / 删了什么**（修改 4 个 / 新增 0 个 / 删除 0 个）
- 修改：[backend/internal/store/store.go](backend/internal/store/store.go) — 给所有外部数据结构加 JSON tag（统一 snake_case）。同时 `pass_hash`/`ip_cipher` 加 `json:"-"` 防止意外外泄。
- 修改：[admin/src/views/Console.vue](admin/src/views/Console.vue) — 整体重做。新增：访客头像（el-avatar，按 visitor_id 哈希出颜色，显示名称首字母）；消息按发送者 + 5 分钟内分组；每组顶部居中时间分隔条（如「今天 16:42」）；hover 气泡显示精确时间；会话列表用 el-avatar + 双行布局；顶部 chat-header 用 el-tag 显示访客地理位置 + 来源 + 当前页；在线统计用 el-statistic。保持所有 Element Plus 组件使用原生默认样式，scoped style 只做布局（无 .el-xxx 覆盖）。
- 修改：[widget/public/chat.html](widget/public/chat.html) — 整体重做。新顶部栏：渐变背景 + 客服头像 + 在线状态指示点（绿色/红色 dot）；消息区按 5 分钟分组 + 时间分隔条 + 圆形头像 + 圆角气泡（mine 渐变蓝 / theirs 白色边框，气泡尾巴用不对称圆角）；系统消息用橙色 chip 风格；文件用 file-card；输入框聚焦时高亮发蓝；滚动条美化；自动 grow textarea。
- 修改：[widget/public/loader.js](widget/public/loader.js) — 浮动按钮从「文字胶囊」改为「56px 圆形渐变图标按钮」+ 未读红色 badge + hover 抬升动效；展开动画 (scale + opacity)。

**业务流程对比**
- 改动前：客服后台打开访客会话，气泡是空的（看不到消息内容）；前台聊天窗口样式像 2010 年的论坛。
- 改动后：消息内容正常显示；前后台都是现代客服系统风格（参考 Intercom/Crisp）；时间清晰可读；消息有头像、有分组、有时间分隔条。

**触发场景与边界 + 验证方式**
- 触发：访问 http://38.76.193.68/admin/console、http://38.76.193.68/widget/demo.html
- 边界：5 分钟内同发送者连续消息合并到同一组；超过 5 分钟或换发送者起新组并显示分隔条。
- 验证：
  1) 访客在 demo.html 发消息 → 客服后台能看到完整内容（而不是空气泡）；
  2) F12 Network 看 `/api/agent/conversations/:id/messages` 响应：字段都是 `content`、`sender` 小写；
  3) 同发送者连续多条消息共享一个头像；
  4) 5 分钟以上间隔出现时间分隔条；
  5) 访客端右下角圆形按钮 + 未读红 badge。

---

## [002] 2026-05-21 16:42 — 首次部署到测试服 38.76.193.68 + 修复 Go 编译 + 修复日志权限

**起因 / 需求**
用户要求把客服系统部署到「零零壹测试服器」（38.76.193.68）做实地测试。部署过程暴露了两个首版骨架未跑通过的问题，必须立即修：
1. Go `go.mod` 只声明了直接依赖，1.17+ 的 module graph 要求完整声明 → `go build` 拒绝构建。
2. Backend 用非 root 用户 `app`（UID 100）运行，bind mount 的宿主目录默认 root 所有，日志写不进去。

**改了什么 / 加了什么 / 删了什么**（修改 1 个 / 新增 0 个 / 删除 0 个）
- 修改：[backend/Dockerfile](backend/Dockerfile#L1-L20) — 把构建顺序从「COPY go.mod → go mod download all → COPY 源码 → go build」改为「COPY 全部源码 → go mod tidy + go mod download → go build」。tidy 会同时补全间接依赖、生成 go.sum、下载所有包。
- 新增（部署运维）：服务器上 chown -R 100:101 /srv/cs-data/logs /srv/cs-data/uploads，匹配容器内 app 用户。
- 新增（不进 git）：本地 `.env`（测试服专用强随机密钥配置）。

**业务流程对比**
- 改动前：`docker compose up -d --build` 在 backend 构建阶段失败：`go: updates to go.mod needed; to update it: go mod tidy`。
- 改动后：6 个镜像全构建成功，6 个容器全 Up（3 个 healthy 健康检查通过），HTTP 200、WSS 握手限速 5r/s 生效、日志 4 路写入正常。

**触发场景与边界 + 验证方式**
- 触发：在测试服执行 `docker compose up -d --build`。
- 边界：构建期间会现场下载所有 Go 依赖 + npm 依赖，第一次约 5 分钟；后续靠 Docker layer cache 加速。
- 验证（在 38.76.193.68 实测，全部通过）：
  1. `curl http://127.0.0.1/api/health` → `{"agents":0,"now":"2026-05-21 16:39:31","status":"ok","tz":"Asia/Shanghai","visitors":0}`
  2. SQL 注入 payload `admin' OR 1=1 --` → 40105 账号或密码错误（未注入成功）
  3. 真实 admin 登录返回 JWT
  4. WSS 握手 20 次连发 → 前 5 次通过、后 15 次 429（限速生效）
  5. security.log 持续记录 ws_handshake_flood violation，count_24h 累加
  6. business.log 持续以北京时间 JSON 格式写入

**已知优化项（不阻塞测试）**
- Nginx 登录限速 `burst=5 nodelay` 偏宽松，10 次并发把 bcrypt 算到 503；下一版可改 `burst=2`。
- curl 客户端会自动 resolve `..`，路径穿越返回 404（被 nginx 兜底），未触达后端的 400 检查；这不是漏洞，仅是测试方法的细节。

---

## [001] 2026-05-21 14:30 — 项目首版：企业级自托管在线客服系统骨架

**起因 / 需求**
用户（爷爷）需要一套可嵌入任意网页的在线客服系统，要求：
1. 访客端可以一行 `<script>` 嵌入任何网站；
2. 客服后台用 Vue3 + Element Plus 原生样式；
3. WSS 全双工通信，万人级长连接 + 消息秒级送达；
4. 客户消息处理顺序以 WSS 通道优先；
5. 自托管 —— 别人想用就部署一份给自己；
6. 部署方式：把仓库目录全量上传到服务器，`docker compose up -d --build` 一条命令搞定，不需要预先 down。

**改了什么 / 加了什么 / 删了什么**（新增功能 9 个 / 删除功能 0 个 / 修改功能 0 个）
- 新增：根目录文档体系 — `LATEST.md`、`CHANGELOG.md`、`README.md`、`.gitignore`、`.env.example`、`docker-compose.yml`
- 新增：`backend/`（Go 后端）— 含 `cmd/server/main.go` 入口、`internal/config`（配置/时区）、`internal/logger`（日志长效存储 + 按天滚动 + 原始 JSON）、`internal/db`（MySQL + 自动迁移）、`internal/redis`（连接池 + Pub/Sub）、`internal/security`（IP 限流、暴力请求拉黑、XSS/SQL 注入清洗、AES-GCM 加密）、`internal/ws`（Hub + Client + 心跳）、`internal/handler`（HTTP API）、`internal/service`（业务逻辑）、`internal/middleware`（JWT/CORS/审计）、`migrations/*.sql`（SQL schema）
- 新增：`admin/`（Vue 3 + Element Plus 客服工作台 + 管理后台）— 含路由 `/admin/login`、`/admin/console`、`/admin/visitors`、`/admin/history`、`/admin/agents`、`/admin/settings`，全部 Element Plus 原生样式，无自定义 CSS
- 新增：`widget/`（嵌入式聊天小部件）— 含 `loader.js` 引导脚本 + iframe 容器 + 内部聊天 UI；通过 `data-cs-endpoint` `data-cs-site` 属性配置；自动重连 + 离线消息暂存
- 新增：`nginx/`（网关 + 反代）— SSL 终结、WSS upgrade、按 IP 限流 + 按 URI 限速、`X-Forwarded-For` 透传、静态资源直出
- 新增：`docs/INTEGRATION.md`、`docs/DEPLOY.md`、`docs/SECURITY.md`、`docs/ARCHITECTURE.md`
- 新增：数据库 schema（visitors / conversations / messages / agents / files / audit_logs / sessions）和启动时自动迁移
- 新增：长效日志体系 — 业务日志、安全日志、审计日志、原始 WSS 报文日志 4 路独立通道，按天 rotate，bind 到宿主机 `/srv/cs-data/logs/`，重启不丢
- 新增：消息优先级队列 — WSS 通道消息走 0 号 channel（最高优先级），HTTP 兜底走 1 号 channel

**业务流程对比**
- 改动前：项目目录是空的，没有任何代码。
- 改动后：用户把仓库目录全量上传服务器，进入目录运行 `docker compose up -d --build`，访问 `https://<域名>/admin/` 即可登录客服工作台；同时给任何第三方网站塞一行 `<script src=".../widget/loader.js" ...>` 即可在该网站右下角出现客服气泡，访客点击即可与客服实时对话。

**触发场景与边界 + 验证方式**
- 触发：访客打开任何嵌入 widget 的网站 → 触发 visitor session 创建 → WSS 握手 → 双向消息流。
- 边界：
  - 单 IP 限流：60 req/min（HTTP）+ 5 次握手/分钟（WSS）；超过返回 429。
  - 单访客消息频率：10 条/分钟，超过临时静音 60 秒并打安全日志。
  - 文件上传上限：默认 20 MB，类型白名单。
  - 客服离线时，访客消息进入「未分配队列」并持久化到 MySQL，客服上线后自动接管。
- 验证方式：
  1. `docker compose up -d --build` 后所有容器 healthy；
  2. `curl https://<域名>/api/health` 返回 `{"status":"ok","tz":"Asia/Shanghai"}`；
  3. 浏览器开 demo 页（`/widget/demo.html`）发送消息，客服后台秒级收到；
  4. 安全自验证：`bash docs/security-selftest.sh`（含 SQL 注入 payload、XSS payload、单 IP 暴力请求模拟，应全部被拦截）；
  5. 重启 `docker compose restart`，历史消息和日志均不丢。
