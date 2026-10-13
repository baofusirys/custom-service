# CHANGELOG Archive [068]

> 从主 CHANGELOG 按编号机械归档；内容保持原始、只追加不改写。

---

## [068] 2026-06-01 05:10 — 客服发消息串台 bug：敏感账密泄露给陌生访客（严重）· v0.6.4

**起因 / 需求**

爷爷反馈截图：客服 web 后台同时挂着 N 个会话，在「访客 A」会话里输了一半字（含账号密码这种敏感内容草稿），尚未发送；接着切到「访客 B」会话准备处理 B 的问题；切过去后 textarea 框里居然还**保留着写给 A 的草稿原文**，且客服没注意到，下意识按了回车 —— 草稿**带着 A 的账号密码被发给了陌生访客 B**，造成 critical 数据泄露事故。

爷爷原话：「这是严重 bug，必须立刻修，绝不能让客服打的字串到别人会话里」。

**根因分析**

`admin/src/views/Console.vue` 两处独立 bug 叠加：

1. **draft 是全局单例**：旧代码 `const draft = ref('')` 是整个 Console 组件级共享变量；`pickConv(c)` 切会话时只重置 messages 列表却**不清 draft**，导致 textarea 双向绑定的内容一直挂在那里 → 切到 B 会话时 A 的草稿原文还在框里
2. **sendText 用 `activeConv.value.id` 实时读**：即使切会话清了 draft，发送瞬间如果 race（onClick 触发 + Vue 微任务 + WSS 收到新会话推送切换 activeConv），sendText 中拼 ws.send 的 `conv` 字段会读到**新的** activeConv.id，把消息发到错误会话；同时 `pendingFiles`（待发附件列表）也是全局单例，会出现「附件挂在 B 但发给 A」或反过来的串台

mobile_app 端虽然 ChatPage 是 push 新页面方式打开（每个 conv 独立 TextEditingController，天然无 draft 串台），但 `app_state.dart sendChat / uploadAndSendFile` 函数体内若未来重构成 `activeConv!.id` 实时读，也会踩同样的 race 坑。本次连带做防御性硬化与 admin 范式对齐。

backend `hub.go` case "chat" 分支当前**完全信任前端传的 conv_id 字段**，前端只要伪造请求就能给任意会话写消息 → 即使前端修好，恶意客户端仍可绕过。该问题留 [069] 单独做（加 `agentInConv(c.ID, e.ConvID)` 校验），本次记 TODO。

**改了什么 / 加了什么 / 删了什么**

> 修改文件 2 个（admin 1 + mobile_app 1），同步升版本号 0.6.3 → 0.6.4
> 新增功能 0 个 / 删除功能 0 个 / 修改功能 6 个（admin: draft → drafts/pendingFiles → pendingFilesMap/sendText snapshot/uploadAndSendFile 加 convId 参数/pickFile/onPasteDraft snapshot；mobile: sendChat snapshot/uploadAndSendFile snapshot）

### ① `admin/src/views/Console.vue` —— per-conv 草稿隔离 + sendText snapshot

- **顶部加 [068] 多行注释**：解释串台 bug 起因 + 防御方案，作为长期 ban 改文档
- **第 N 行 draft 改字典**：`const draft = ref('')` → `const drafts = ref({})`；`const pendingFiles = ref([])` → `const pendingFilesMap = ref({})`
- **新增 computed `currentDraft`**：`get()` 返 `drafts.value[activeConv.value?.id] || ''`，`set(v)` 写 `drafts.value[activeConv.value?.id] = v`；切会话自动取出该 conv 的草稿（无则空），新会话默认空字符串 —— 从根上消除全局单例污染
- **新增 computed `currentPendingFiles`**：返 `pendingFilesMap.value[activeConv.value?.id] || []`
- **sendText 入口 snapshot**：`const sendingConvId = activeConv.value?.id` 一次性锁定；textSnap 从 `drafts[sendingConvId]` 读；ws.send 的 `conv` 字段、本地 messages.push 的守卫 `if (activeConv.value?.id === sendingConvId)`、conv.last_message/updated_at 全部走 sendingConvId；发送成功后清 `drafts.value[sendingConvId] = ''` —— 发送途中切走也不污染新会话视图、不串台
- **uploadAndSendFile 改签名 `(file, convId)`**：所有 `activeConv.value.id` 引用全部替换为 convId 参数，messages.push 同样加 `if (activeConv.value?.id === convId)` 守卫；未传 convId 时 fallback 读 activeConv.id 兼容旧调用
- **addPendingFile/removePendingFile/clearAllPending 改为 per-conv 操作**：`addPendingFile(file, convId)`、`removePendingFile(item, convId)`、新增 `clearPendingFor(convId)` 在内部 `URL.revokeObjectURL(item.preview)` 防 blob 内存泄漏
- **pickFile/onPasteDraft 入口 snapshot convId** 再传给 addPendingFile，确保 file picker dialog 期间切会话不会让文件挂错 conv
- **模板**：el-input v-model 改 `currentDraft`，v-for 改 `currentPendingFiles`，移除按钮传 `activeConv?.id`
- **保留 2 处合法 `activeConv.value.id`**：WSS onMessage 收 read/chat 推送时判断「当前是否在这个 conv」语义，属状态查询非 race 路径

### ② `mobile_app/lib/state/app_state.dart` —— sendChat / uploadAndSendFile snapshot 防御性硬化

- **函数顶上加 [068] 大段中文注释**：说明 mobile 因 ChatPage push 隔离 + 本地 `TextEditingController` + dispose 天然无 draft 串台（与 admin 不同），但仍按 admin 范式加 snapshot 防御范式对齐 + 防未来重构踩坑
- **sendChat 入口**：`final convIdSnap = conv.id; final textSnap = text.trim();` 一次性锁定；ws.send 的 conv/content 字段、Message.convId/content 全部走 snapshot；本地乐观渲染 `if (activeConv?.id == convIdSnap)` 守卫 —— 切走会话时消息仍发到原 conv（已 ws.send），但不污染新会话 messages UI
- **uploadAndSendFile 入口**：`final convIdSnap = conv.id;`；`Api.uploadFile(file, convIdSnap)`（关键，防上传 await 期间用户切走导致 conv_id 参数被错读）；ws.send / Message.convId 全部用 snapshot；本地乐观渲染同样加守卫

### ③ 版本号 / 元数据

- `VERSION` 0.6.3 → 0.6.4
- `backend/internal/config/version.go` Version 常量 0.6.3 → 0.6.4
- `LATEST.md` 顶部「当前版本」标签 + 最近 3 次改动摘要刷新

### ④ TODO [069]（已挂牌，本次未做）

backend/internal/ws/hub.go `case "chat"` agent 路径需加 `agentInConv(c.ID, e.ConvID)` 校验防恶意客户端伪造 conv_id 越权写入。前端 snapshot 修好后，**前端层** race 已堵死，但**协议层**仍信任客户端字段。

**业务流程对比**

- **改动前（严重 bug）**：客服在 A 会话 textarea 输入「账号:xxx 密码:yyy」草稿未发送 → 切到 B 会话 → textarea 框里**仍显示给 A 写的账密原文** → 客服没注意按回车 → 账密被发给陌生访客 B → 数据泄露
- **改动后**：客服在 A 会话 textarea 输入草稿 → 切到 B 会话 → textarea 自动变成 B 之前留的草稿（无则空白）→ 切回 A 会话 → A 的草稿原样还在；即便 A 草稿不慎按回车前一瞬切到了 B，sendText 已 snapshot 了 A 的 convId，消息也发到 A 而非 B
- **附件场景**：客服在 A 会话挂了 3 个待发文件 → 切到 B 会话 → 文件预览区为空（B 没挂文件）→ 切回 A 还在；上传 await 期间切走也不会让文件挂到错误会话

**触发场景与边界 + 验证方式**

触发：admin web 端任何同时挂多会话的客服，切会话场景。

边界：
- 切到完全新会话（drafts[convId] === undefined）：textarea 显示空 ✅
- 切到曾输过草稿的旧会话：textarea 恢复原草稿 ✅
- 发送瞬间被推送切走 activeConv：消息仍发到 sendingConvId 对应的原 conv（正确），本地 messages 不污染新会话 UI ✅
- 上传文件 await 期间切走：文件发给上传开始时的 convId（正确）✅
- removePendingFile 切回原会话：刚移除的文件预览 blob URL 已 revoke，不内存泄漏 ✅
- mobile_app 端：因 ChatPage push 隔离，原本就无串台；本次 snapshot 加固后即便未来重构也安全 ✅
- 恶意客户端绕过（前端 sendingConvId 改为其他 conv 强发）：当前后端**不防**，[069] 处理 ⚠️

验证方式：
1. **手动场景**：登录 admin → 同时打开两个未读会话 A、B → 在 A 输入「TEST_LEAK_账密」不发 → 切到 B → 确认 B textarea 为空（或显示 B 之前草稿）→ 切回 A → 确认「TEST_LEAK_账密」还在 → 发送 → 确认消息进 A 而非 B
2. **race 模拟**：在 A textarea 输入 → 浏览器 devtools Performance 录制 → 按 Enter 同时点击 B 会话 → 检查数据库 messages 表确认 conv_id 仍是 A 的
3. **附件场景**：A 会话点 paperclip 选文件 → 文件 picker 弹出期间切到 B → 选完文件应进 A 而非 B 的待发区
4. **绕过测试** [069] 待做：用 wscat 伪造 `{type:"chat", conv:"别人的_conv_id", text:"x"}` 看后端是否拒绝
5. **回归**：[067] 「已联系」过滤、[065] unread 计数、[064] token refresh 应不受影响（本次只改 sendText / uploadAndSendFile / pickFile / onPasteDraft，与上述模块无耦合）

---

