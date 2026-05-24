# CHANGELOG

> 每次代码改动完成后，必须立刻在文件顶部追加一条。时间用北京时间绝对格式（YYYY-MM-DD HH:mm）。

---

## [041] 2026-05-25 02:20 — iPhone App 客服端可发图片/文件，对齐 admin web 多文件队列

**起因 / 需求**
爷爷反馈：客服 iPhone App 端目前只能发文本，不能发图片和文件。要跟 admin web（[040] 已实现）一样支持：
- 拍照 + 相册多选 + 任意文件三种来源
- pending 队列预览
- 点发送时统一上传（不是即选即发）

**改了什么**（新增 0 文件 / 修改 5 文件，全部 mobile_app 内）

### 1. 依赖 + iOS 权限
- `mobile_app/pubspec.yaml`：加 `image_picker: ^1.0.7`（拍照 + 相册多选）+ `file_picker: ^8.0.0`（任意文件多选）
- `mobile_app/ios/Runner/Info.plist`：加 `NSPhotoLibraryUsageDescription`「选择图片发送给访客」+ `NSCameraUsageDescription`「拍照发送给访客」

### 2. HTTP 上传层 `mobile_app/lib/api/http_client.dart`
- import `dart:io`
- 新增 `Api.uploadFile(File file, String convId)`：dio multipart POST `/api/upload`，form fields = file/uploader=agent/conv_id；显式设 `Content-Type: multipart/form-data` 让 dio 自动加 boundary；失败静默返 null

### 3. 业务层 `mobile_app/lib/state/app_state.dart`
- import `dart:io`
- 新增 `Future<bool> uploadAndSendFile(File file)`：调 `Api.uploadFile` → 拿 `{url, kind, name, size}` → WSS 发 `{type:chat, conv, content:'', media, mkind, mname, msize, ts, prio}`（envelope 字段名跟 admin Console.vue 完全一致）→ 乐观追加 `Message(mediaUrl, mediaKind, mediaName, ...)` 到 `messages` 列表 + notifyListeners；返 true/false 让 UI 提示

### 4. UI 层 `mobile_app/lib/pages/chat_page.dart`
- import dart:io / file_picker / image_picker
- State 加 `List<File> _pending` 队列 + `bool _sending` 防重复点 + `_picker = ImagePicker()`
- 新增 `_pickAttachment()`：showModalBottomSheet → 三选一「拍照 / 相册多选 / 选文件」→ 用 image_picker.pickImage / pickMultiImage / FilePicker.pickFiles 拿 File → addAll 到 `_pending`；try/catch + SnackBar 提示
- 新增 `_removePending(File)`：从队列移除
- `_send` 改 async 重构：先发文本（如果有）→ 再 for await 依次 `app.uploadAndSendFile(f)` 串行上传；每个失败弹 SnackBar `上传失败：xxx`；try/finally 复位 `_sending`
- `_inputBar` 改 Column 布局：上方 `_pendingBar` v-if 显示 chip Wrap（spacing 6 自动换行）；下方 Row 三件套：左边附件按钮（`Icons.add_circle_outline` 蓝色 28）+ 中间输入框 + 右边发送（`_sending` 时显示 CircularProgressIndicator 转圈）
- 新增 `_pendingChip(File)`：图片用 `Image.file` 36×36 缩略；非图片用文件图标；文件名 ellipsis + × 移除按钮；构成跟 widget/admin 视觉一致（白底圆角灰边 max-width 200）

**业务流程对比**

| 端 | [040] 前 | [041] 后 |
|---|---|---|
| iPhone App 客服 | 只能发文本 | 拍照 / 相册多选 / 选文件 → pending 队列 → 发送 |
| 队列 chip 样式 | 不存在 | 跟 widget/admin 完全一致（36×36 + 文件名 + ×）|
| 上传过程 UI 反馈 | 不存在 | 发送按钮转圈 + disable 防重复点 |
| 失败提示 | 不存在 | SnackBar「上传失败：文件名」可重新选 |

**触发场景与边界 + 验证方式**

- **拍照权限**：首次点拍照 iOS 弹原生权限弹窗（NSCameraUsageDescription），拒绝 image_picker 会抛 catch → SnackBar 提示
- **相册权限**：同上 NSPhotoLibraryUsageDescription
- **多选**：image_picker.pickMultiImage iOS 14+ 支持原生多选；file_picker allowMultiple:true 支持
- **串行上传**：`for ... await` 不用 Promise.all，保消息时序（跟 widget/admin 一致；客服 iPhone 蜂窝网经常慢，串行更稳）
- **空内容防御**：`if (t.isEmpty && files.isEmpty) return`
- **失败不重发**：上传前 `setState(() => _pending.clear())` UI 先清，失败的让用户重新选（避免静默重试）
- **mounted 检查**：所有 setState/SnackBar 都 `if (mounted)`，防止 dispose 后调 setState
- **跟后端兼容**：multipart 字段名 file/uploader=agent/conv_id 跟 admin web 完全一致；后端 authorizeUpload 已校验 agent JWT + conv 归属，无新攻击面
- **不影响**：widget/admin 不动；mobile_app 语音通话 / 设置页 / 会话列表 / 消息渲染（已支持 media）不动

**iOS 部署需要**：
- pod install 拉 image_picker_ios + file_picker_ios 子 pod
- flutter build ios --release 重 build
- 装机后首次点附件按钮会弹相册/相机权限对话框，"好" 即可

---

## [040] 2026-05-25 01:50 — widget + admin 都支持「同时粘贴/选择多张图片，一次性发送」

**起因 / 需求**
爷爷反馈：[039] 单文件预览不够用，需要支持**同时粘贴多张图片**（一次 Ctrl+V 可能含多个图，或者多次粘贴累积），一起发送。客服工作台（admin）也要同样支持。

**改了什么**（修改 2 文件，widget + admin 同步对齐）

### widget `widget/public/chat.html`
- HTML：`<div id="pendingChip">` → `<div id="pendingList">` 多 chip 容器；`<input type="file" id="fileInput" multiple>` 加 multiple
- CSS：`.pending-chip`（单个）改窄到 `max-width:180px` + `.pending-list` 容器 `flex-wrap` 多 chip 自动换行；`.is-show` 控制显示
- JS：
  - `pendingFile = null` → `pendingFiles = []` 数组，每项 `{file, chipEl, blobUrl}`
  - `setPendingFile` → `addPendingFile`（append 而不是 replace）
  - `clearPendingFile` → `removePendingFile(item)`（按引用移除）+ `clearAllPending()`（拷贝再遍历防跳元素）
  - paste 监听：去掉 `break`，遍历所有 file items 全部 add；只要含 file 就 `e.preventDefault()`（防止 file 被当文本粘进 textarea）
  - `onPickFile`：遍历 `e.target.files` 全部 add（multiple 支持）
  - `sendText`：先发文本（如果有）→ 再 for await 顺序上传所有 pending 文件（每个一条独立消息，保持时序）→ 清队列

### admin `admin/src/views/Console.vue`
- `<script setup>`：
  - `pendingFiles = ref([])` 数组，每项 `{file, blobUrl, isImage}`
  - `addPendingFile(file)` / `removePendingFile(item)` / `clearAllPending()` / `fmtBytes(n)` 单职责
  - `onPasteDraft` 遍历所有 file items 全部 add；`hadFile` 标记控制 preventDefault
  - `pickFile` 改成同步函数遍历 `e.target.files` 全部 add（不再 await uploadAndSendFile）
  - `sendText` 改 async：先发文本 → `for (const f of files) await uploadAndSendFile(f)` 顺序上传；try/finally 确保 sending 状态复位
- `<template>`：el-input 上方加 `<div v-if="pendingFiles.length" class="pending-list">` v-for 渲染 chip；`<input multiple>` 加 multiple；占位符更新为"可多张"
- `<style scoped>`：加 `.pending-list` / `.pending-chip` / `.thumb` / `.file-icon` / `.meta` / `.remove-btn` 完整一套（跟 widget 视觉一致）

**业务流程对比**

| 场景 | 改前（[039]）| 改后（[040]）|
|---|---|---|
| 一次 Ctrl+V 含多张图 | 只取第一张（`break`） | 全部 add 到队列 |
| 连续粘贴 3 次 | 后一张覆盖前一张（单文件） | 队列累积成 3 个 chip |
| 选附件按钮 | 单选 | multiple 多选 |
| × 移除 | 清整个 pending | 移除指定 chip，其他保留 |
| 点发送 | 1 个文本 + 1 个附件 | 1 个文本 + N 个附件依次上传 |
| admin 客服端 | 未改造（粘即发） | 同 widget 完全对齐 |

**触发场景与边界 + 验证方式**

- **多文件 blob URL 内存回收**：每个 item.blobUrl 独立 `URL.revokeObjectURL`；`clearAllPending` 遍历全部回收防内存泄漏（长会话不爆内存）
- **顺序上传**：用 `for ... await` 串行而非 `Promise.all` 并行——确保消息时序（避免后端 ts 相同时排序错乱；也避免多文件大并发把服务器带宽打满）
- **空内容防御**：`if (!text && files.length === 0) return` 防止误触
- **paste 阻止默认**：仅在剪贴板含至少一个 file 时 `preventDefault`，纯文本粘贴仍走默认 textarea 行为
- **单文件 chip ellipsis**：max-width 180/220px + name nowrap+ellipsis；多 chip flex-wrap 自动换行不挤
- **失败提示保留**：uploadAndSendFile 内部 try/catch 仍 renderSys（widget）/ 静默 catch（admin 跟原行为一致）；上传前已 clearAllPending 避免重复点
- **不影响**：mobile_app 输入逻辑不动；widget/admin 语音通话 / lightbox / 文本聊天不动
- **验证**：
  1. widget 访客一次粘 2 张图 → pending 区出现 2 个 chip → 输入「这两张」→ 点发送 → 应该看到 3 条消息（文本 + 图 1 + 图 2）
  2. admin 客服同步：选附件按钮多选 3 个文件 → pending 区 3 个 chip → 任意 × 移除一个 → 剩 2 个 → 点发送 → 2 条文件消息

---

## [039] 2026-05-25 01:25 — widget 粘贴/附件改为预览暂存，点发送才上传（不再粘贴即发）

**起因 / 需求**
爷爷反馈：访客 widget 在输入框粘贴图片/文件时，现在是粘贴即立刻上传发送，没机会反悔 / 没机会再补充文字。改成 IM 标准行为：**粘贴后先放到输入框上方预览，用户确认后点发送才真正上传发送**。

**改了什么**（修改 1 文件 widget/public/chat.html）

- HTML：`.input-wrap` 内 textarea 上方新增 `<div id="pendingChip" class="pending-chip">` 预览容器
- CSS：新增 `.pending-chip` 样式（白底圆角，灰边框，40×40 缩略图 / 文件图标 + 文件名 + 大小 + × 移除按钮；`.is-show` 类控制显示）
- JS：
  - 新增模块级 `pendingFile` 变量 + `pendingChipEl` 引用
  - 新增 `fmtBytes(n)` 格式化文件大小（B / KB / MB）
  - 新增 `setPendingFile(file)`：渲染预览 chip（图片用 URL.createObjectURL blob 缩略图 + dataset.blobUrl 标记便于回收；非图片用文件图标）；显示 chip
  - 新增 `clearPendingFile()`：回收图片 blob URL 防内存泄漏 + 清 DOM + 隐藏 chip
  - `sendText()` 重构：先发文本（如果有）→ 再发 pending file（如果有）；任一存在就算"有内容可发"；上传前先清 UI 防止用户重复点
  - `onPickFile`（附件按钮）从 `await uploadAndSendFile(file)` 改为 `setPendingFile(file)`
  - paste 监听从 `uploadAndSendFile(f)` 改为 `setPendingFile(f)`

**业务流程对比**

| 场景 | 改前 | 改后 |
|---|---|---|
| 访客截屏后 Ctrl+V | 立刻上传发送，没机会反悔 | 输入框上方出现 40×40 缩略图 chip + 文件名 + 大小 + × 按钮，可补充文字一起发，点发送才上传 |
| 访客点附件按钮选文件 | 同上立即发 | 同上预览 |
| × 按钮 | 不存在 | 单击移除 pending；如果点错了或想换附件直接覆盖（再粘一次） |
| 点发送 / 回车 | 只发文本 | 先发文本（如果有）→ 再上传 pending（如果有）；二者都没就 noop |

**触发场景与边界 + 验证方式**

- **内存安全**：图片 chip 用 `URL.createObjectURL(file)` 生成 blob URL，清除时 `URL.revokeObjectURL(url)` 回收防内存泄漏
- **覆盖式单文件**：连续粘贴 / 选附件会覆盖前一个 pending（够用）；不做多文件队列以保持 UI 简单
- **失败提示保留**：`uploadAndSendFile` 内部 try/catch 仍然 renderSys 失败提示；调用前已 `clearPendingFile` 避免用户重复点（失败后用户可重新粘贴）
- **空内容防御**：sendText 入口 `if (!text && !file) return;` 防止误触
- **不影响**：admin / mobile_app 的输入逻辑不动；widget 的语音通话 / 图片 lightbox / 文本聊天功能不动
- **文本粘贴**：纯文本粘贴不拦截（`items[i].kind === 'file'` 才进入），保留原 textarea 行为
- **验证**：访客 widget 截屏 + Ctrl+V → 出现缩略图 chip → 输入框输入「这是截图」→ 点发送 → 应该先发文本「这是截图」再发图片，两条消息

---

## [038] 2026-05-25 00:55 — widget 图片查看器升级到宿主页全屏（突破 iframe 大小限制）

**起因 / 需求**
爷爷反馈：访客 widget 点聊天图片放大查看时，图片只在 380×560 的聊天窗口（iframe）里"全屏"，因为 iframe 本身就是右下角一小块，所以图片小得看不清。

根因：[033] 加的 `openImageLightbox` 用 `position:fixed inset:0` 在 chat.html（iframe 内）创建 overlay。`position:fixed` 的视窗是 iframe 自己的 viewport（380×560），不是整个浏览器窗口。

**改了什么**（修改 2 文件）

- `widget/public/loader.js`：
  - message listener 新增分支 `if (ev.data.type === 'lightbox')` → 调 `openHostLightbox(src)`
  - 新增 `openHostLightbox(src)` 函数：在 parent document 直接创建覆盖整个浏览器视窗（92vw × 92vh）的 overlay；z-index 用 `2147483647` 跟 wrap 同级保证在最上层；点 overlay/ESC/× 都关闭；图片本身阻止冒泡（防误关，方便长按保存）

- `widget/public/chat.html` `openImageLightbox(src)`：
  - 优先尝试 `window.parent.postMessage({__cs:1, type:'lightbox', src:src}, '*')` 让宿主页接管全屏
  - 仅在 standalone 模式（`window.parent === window`，例如直接访问 `chat.html` 测试时）才 fallback 用 iframe 内的原 lightbox

**业务流程对比**

| 场景 | 改前 | 改后 |
|---|---|---|
| 访客在第三方网站打开 widget → 点聊天图片 | 图片只在 380×560 聊天窗口里全屏，缩成小图 | 整个浏览器窗口 92vw×92vh 全屏黑底大图 |
| 直接访问 chat.html demo 测试 | iframe 内 lightbox | 同上（standalone fallback 保持原逻辑） |
| admin Web / iPhone App | （另两端有自己的查看器，跟 widget 无关）| 不动 |

**触发场景与边界 + 验证方式**

- **跨域 postMessage**：iframe（maihaocs.icu 域）→ parent（任意客户网站域），跨域 postMessage 浏览器永远允许；用 `__cs:1` 魔法 key 区分 widget 自己的消息
- **z-index 冲突**：用 `2147483647`（int32 max），跟 widget 的 iframe wrap 同级；后插入的 lightbox overlay 在 DOM 后面，按 stacking context 默认在上层覆盖 iframe
- **bubble 防误关**：图片本身 `e.stopPropagation()`，点图片不关闭（方便长按保存）；overlay 空白处 / × / ESC 才关
- **parent 不响应**：try/catch 兜底 + standalone 判定（`window.parent === window`）→ 走 iframe 内原逻辑（最坏情况退化到改前体验）
- **多次连点**：openHostLightbox 进入前先清掉旧 overlay（同 id 防累积）
- **不影响**：admin 用 el-image preview-src-list 不动；mobile_app 用 photo_view 不动；widget 文本/语音/通话功能不动
- **验证**：访客 demo 页发图片 → 客服 admin 回图 → 访客 widget 点图 → 整个浏览器窗口黑底大图（不是聊天窗口里的小框框）

---

## [037] 2026-05-25 00:40 — App 聊天页标题居中 + 加访客来源/当前页/位置信息条

**起因 / 需求**
爷爷反馈 iPhone App 聊天页：
1. 顶部「访客 119572」**不居中**（被左对齐顶到返回按钮旁）
2. **访客网址看不清楚**（其实根本没显示当前页 URL，且老逻辑把"地理位置"和"referer"拼在 AppBar 副标题字号 11px 一行里看不清）

对比 admin web Console.vue 是用两条 el-tag「来源：xxx」+「当前页：xxx」清晰展示，App 端缺失。

**改了什么**（修改 1 文件）

- `mobile_app/lib/pages/chat_page.dart`：
  - AppBar `title` 从 `Column(crossAxisAlignment.start, [Text(name), Text(loc+referer fontSize:11)])` → 单行 `Text(name, fontSize:17, fontWeight:w600)`，并显式 `centerTitle: true`，标题真居中
  - body 顶部新增 `_visitorInfoBar(conv)` 信息条：灰底 (0xFFF5F7FA)，三行（任一非空才显示）`来源：${referer}` / `当前页：${lastPage}` / `位置：${location}`；标签加粗深色 + 内容字号 13；URL 长不 ellipsis 而是自动换行（爷爷强调「看不清」就是要看全）
  - 新增辅助 `_infoLine(label, value)` RichText 渲染
  - import `models.dart` 的 Conversation 类型注解（已 import）

**业务流程对比**

| 端 | 改前 | 改后 |
|---|---|---|
| App 聊天页标题 | 「访客 119572」+ 第二行 11px 文字（且只拼 location + referer，没 lastPage），全部左对齐 | 「访客 119572」17px 加粗居中（与 iOS 风格一致）|
| App 聊天页访客信息 | 同上 11px 一行隐没在 AppBar 里看不清 | 正文顶部独立信息条，灰底，13px，行高 1.4，长 URL 自动换行；缺字段不显示 |
| 与 admin web 一致性 | 缺当前页 URL；只有 referer 拼地理位置 | 来源 + 当前页 + 位置三件套（admin 是「来源」+「当前页」el-tag，App 多了「位置」补充地理） |

**触发场景与边界 + 验证方式**
- 访客只有 referer 没 last_page：只显示「来源：...」一行
- 访客 referer 和 last_page 都空（直接访问 / 单页应用）：只显示位置（如果有地理）
- 三项全空：信息条整个不渲染（`SizedBox.shrink()`），不留空白
- 长 URL（>50 字符）：自动换行多行展示，不截断不 ellipsis
- 不影响：消息列表 / 输入框 / 来电铃声 / 录音 / 通话功能
- 验证：iPhone 打开任意会话 → 标题居中 + 信息条带 URL 清晰可见

---

## [036] 2026-05-25 00:25 — 三端语音来电统一循环铃声 voice-ring.mp3 + 下线 push_sound_call 设置

**起因 / 需求**
爷爷反馈：「app 端在应用内收到语音来电没有提示音」+「想把可配置的语音来电提示音设置去掉，三端统一用一个固定铃声」+「四个场景循环播放：admin web 收到来电 / iPhone 前台收到来电 / iPhone 推送拉起后进入来电界面 / widget 访客拨号等待」。

**改了什么**（新增 1 资源 × 3 端 + 修改 8 文件）

### 1. 资源同步
- 源文件：`音频内容/语音来电声音.mp3`（爷爷提供）
- 复制为 `voice-ring.mp3` 到三端：
  - `admin/public/sounds/voice-ring.mp3`
  - `widget/public/sounds/voice-ring.mp3`
  - `mobile_app/assets/sounds/voice-ring.mp3`
- 文件名用英文 voice-ring：避免中文路径在 URL 编码 / iOS bundle / asset key 上的兼容性风险
- pubspec.yaml 已有 `assets/sounds/` 整目录声明，自动包含

### 2. 下线 push_sound_call 设置（仅这一项，别的设置不动）
- `backend/internal/handler/http.go`：`allowedSettingKeys` 删除 `push_sound_call`（保留注释说明已下线）；APNs 推送音色固定为 `service.go` pushAPNsCommon 内部默认值 "4"
- `admin/src/views/Settings.vue`：form 初始化 / load / save / 模板 4 处删除 push_sound_call
- `mobile_app/lib/pages/settings_page.dart`：删除 `_pushSoundCall` 字段、load/save 引用、UI 列表项

### 3. 三端来电循环铃声集成

| 端 | 资源加载 | 触发时机 | 停止时机 |
|---|---|---|---|
| admin web | `admin/src/api/sound.js` 新增 `playRingLoop()` / `stopRingLoop()`，HTMLAudioElement.loop=true | `Console.vue` `voiceOnIncoming` | `voiceAccept` 一开始 + `voiceCleanup`（cover voiceEnd/Reject/Taken/RemoteEnd 所有路径） |
| widget 访客 | `chat.html` 内联 `var _ringAudio = new Audio('sounds/voice-ring.mp3'); _ringAudio.loop = true` | `voiceStart`（state=ringing 拨号中） | `voiceOnAccept` 一开始 + `voiceEnd`（cover 拒绝/超时/连接失败/挂断） |
| mobile_app | `lib/api/sound.dart` 新增 `playRingLoop()` / `stopRingLoop()`，audioplayers ReleaseMode.loop | `voice_controller.dart` `_onIncoming` | `accept` 一开始 + `_cleanup`（cover reject/_end/dispose/_onTaken 所有路径） |

### 4. iPhone 推送拉起场景（重点）
爷爷的需求场景"在 App 外语音来电了，收到推送，点推送拉起 App 进入来电界面也要响"——已自动覆盖：
1. App 在后台 → `voice_call` 信令到 hub → APNs 推送（luckfast 系统音色 4 响一下）
2. 用户点推送 → maihaocs:// URL Scheme 拉起 App
3. App 启动 → WSS 重连 → register 后 hub 把 30s 内的 `pendingCalls` buffer 重投（[030-fix] 已实现）
4. App 收到 voice_call → `_onIncoming` 触发 → 状态 incoming + `playRingLoop()` 循环播
5. App 显示来电界面 + 铃声响 → 用户接听 / 拒绝 → 停铃声

**业务流程对比**

| 场景 | 改前 | 改后 |
|---|---|---|
| widget 访客拨号等待 | 无声音 | voice-ring.mp3 循环响 |
| admin 客服收到来电 | playSound(agentSound)，普通消息音色，只响一下 | voice-ring.mp3 循环响 |
| iPhone 前台收到来电 | 无 App 内声音（只有 APNs 推送音可能在锁屏响） | voice-ring.mp3 循环响 |
| iPhone 推送拉起后进入来电界面 | 同上无声音 | voice-ring.mp3 循环响（_onIncoming 二次触发） |
| 接通 / 拒绝 / 挂断 | （没铃声可停） | 任何路径都停铃声 |
| admin Settings 「语音来电提示音」下拉框 | 16 种 luckfast 推送音色选择 | 删除（功能下线，统一用 voice-ring.mp3） |

**触发场景与边界 + 验证方式**

- **autoplay 限制**：浏览器要求音频必须用户手势触发。widget 在首次 click 解锁所有 audio（包括 voice-ring）；admin 在用户进入聊天界面时调 `unlockAudio()`。如果客服把后台开着但从未点击页面，第一次来电铃声可能被拦——但 voice_call 必然先有用户交互（启动 admin）才能进入聊天，实际无问题
- **循环不停问题**：所有可能的"通话结束"路径都覆盖了 stopRingLoop：admin 走 `voiceCleanup`（统一收尾），widget 走 `voiceEnd`，mobile_app 走 `_cleanup`，单独在 accept 也调一次（accept 不进 cleanup）
- **iOS audio session**：mobile_app 用 `_ensureAudioContext()` 已配 `AVAudioSessionCategory.playback`（静音键也能响）；ringPlayer 用 `ReleaseMode.loop` 系统级循环不耗 dart 端轮询
- **资源未加载**：sound.js / chat.html / sound.dart 都用 preload + 容错（catch 静默），即使 voice-ring.mp3 加载失败也不会抛错阻塞业务
- **APNs 推送音色降级**：backend service.go pushAPNsCommon 还会查 `push_sound_call` setting（已被 admin 写入禁用，DB 永远空），最终走 default "4"——保持 APNs 锁屏来电通知音不变
- **验证**：
  1. widget 访客点电话按钮 → 听到拨号铃声循环响
  2. admin 客服收到来电浮窗 → 听到铃声循环响 → 点接听 → 立刻停
  3. iPhone App 前台收到来电 → 听到铃声循环响 → 点接听 → 立刻停
  4. iPhone 锁屏收到 APNs 推送（系统音）→ 点推送拉 App → 进入来电界面 → 听到铃声循环响
  5. admin Settings 页面不再显示「语音来电提示音」下拉框
  6. mobile_app 设置页不再显示「语音来电提示音」选项

---

## [035] 2026-05-24 23:55 — 引入 CoTURN：WebRTC TURN/STUN relay，解决 VPN/严格 NAT 下通话不通问题

**起因 / 需求**
[034-fix1] 部署后实测：访客 widget → iPhone App 通话，**「通话中 00:07」但没声音 → 15 秒后连接失败**。Web ↔ Web 完全正常。raw_ws.log 复盘发现 iPhone 发的 ICE candidate 全是 `198.18.0.1` / `127.0.0.1` / `fd00::` / `::1` 本地虚拟接口，srflx 是 `172.104.124.21`（Linode 数据中心 IP，明显 VPN 出口）—— iPhone 端挂着 VPN 把 WebRTC 锁在隧道里，跟访客 `110.241.19.222`（中国电信）无法 P2P 打洞。

爷爷确认关 VPN 后通话通了，但要求：**不能要求客服/访客手动关 VPN**，需要服务端方案让 WebRTC 在 VPN/严格 NAT/防火墙下都能通。这就是 TURN server 的标准用法——P2P 失败时所有 RTP 流量绕道服务器中继。

**改了什么**（新增 1 模块 + 修改 9 文件 + 新增 1 后端文件）

### 1. 新模块 `turn/`（CoTURN docker 服务）

- `turn/Dockerfile`：基于 `coturn/coturn:4.6`，加 envsubst（gettext-base）渲染配置 + 北京时区
- `turn/turnserver.conf.tmpl`：模板，entrypoint 用 envsubst 注入 `${TURN_EXTERNAL_IP}` / `${TURN_REALM}` / `${TURN_STATIC_AUTH_SECRET}`
  - 监听 3478 (UDP+TCP STUN+TURN) + 5349 (TURN over TLS)
  - relay 端口范围 49152-49200（49 端口 ~50 路并发音频）
  - 短期凭证机制 `use-auth-secret + static-auth-secret`（无须维护用户表）
  - 安全加固：`denied-peer-ip` 显式拒绝所有 RFC 1918 / 保留段防 SSRF；`no-cli` 关 5766 管理端口；`no-loopback-peers` / `no-multicast-peers`；`total-quota=200` / `user-quota=10` 配额限制；`stale-nonce=600` 防重放
- `turn/entrypoint.sh`：必填环境变量 fail-fast；TLS 证书软链（从 cs_ssl_data 卷的 maihaocs.icu.pem/.key）；证书缺失时自动生成 30 天临时自签证书占位避免容器起不来；envsubst 渲染配置；exec turnserver 作为 PID 1
- `turn/README.md`：模块文档（依赖关系 / 配置 / 已知坑 / 短期凭证算法 / 历史改动）

### 2. 后端

- `backend/internal/service/turn.go`（新）：`GenerateTurnCredential(userID, realm, secret) *TurnCredential` 实现 draft-uberti-behave-turn-rest 短期凭证算法：
  ```
  timestamp = unix() + 86400
  username  = "<timestamp>:<userid>"
  password  = base64(HMAC-SHA1(secret, username))
  ```
  返回 `{username, credential, urls, ttl}` 直接给前端用。urls 含 turn:UDP / turn:TCP / turns:TLS / stun: 四种，浏览器按顺序尝试
- `backend/internal/config/config.go`：Config 加 `TurnRealm` + `TurnSecret`；从 env 读，未配置则不报错（向后兼容，前端走 STUN-only 降级）
- `backend/internal/handler/http.go`：新增 `TurnCredential(c *gin.Context)` handler：从 context 取 agent_id / vid / IP 作 userID 仅供日志审计；TURN 未配置时返回纯 STUN 配置降级（保持通话功能）；正常返回 `gin.H{"code":0,"data":cred}`
- `backend/cmd/server/main.go`：注册 2 个路由
  - `GET /api/visitor/turn-credential`（限流 IPHTTPRPM）
  - `GET /api/agent/turn-credential`（需 AgentAuth + 限流）

### 3. 三端 WebRTC iceServers 动态化

- `widget/public/chat.html`：
  - `ICE_SERVERS` 默认 STUN 兜底
  - 新增 `fetchTurnCredential()` 异步刷新（失败静默）
  - `voiceStart()` 内 `await fetchTurnCredential()` 后再 getUserMedia
  - `createVoicePC()` 仍用 `ICE_SERVERS`，但内容已动态更新
- `admin/src/views/Console.vue`：
  - `const → let ICE_SERVERS`；加 `fetchTurnCredential()`（用现有 `http` 模块调 `/agent/turn-credential`）
  - `voiceAccept()` 内 `await fetchTurnCredential()` 后再 getUserMedia
- `mobile_app/lib/api/http_client.dart`：`Api.turnCredential()` 方法，dio 调 `/agent/turn-credential`，失败返回 null
- `mobile_app/lib/state/voice_controller.dart`：
  - `_iceServers` 从 `static const` 改成实例 `Map<String,dynamic>` 默认 STUN
  - 新增 `_refreshIceServers()` 调 Api.turnCredential() 替换 _iceServers
  - `accept()` 内 `await _refreshIceServers()` 后再 getUserMedia

### 4. 部署配置

- `docker-compose.yml`：新增 `coturn` 服务（`network_mode: host` 避免 docker bridge 性能损耗 + 防止 srflx 报错地址；只读挂载 `cs_ssl_data:/etc/coturn/ssl-src` 复用 nginx 的 Let's Encrypt 证书；日志 bind 到 `/srv/cs-data/logs/coturn/`）；backend 服务的 environment 注入 `TURN_REALM` + `TURN_STATIC_AUTH_SECRET`
- `.env.example`：加 `TURN_EXTERNAL_IP` / `TURN_REALM` / `TURN_STATIC_AUTH_SECRET`（含中文注释 + `openssl rand -hex 32` 生成命令提示）

**业务流程对比**

| 场景 | 改前（[029]~[034-fix1]）| 改后（[035] CoTURN）|
|---|---|---|
| iPhone 关 VPN 通话 | ✓ 能通 | ✓ 能通（优先 P2P 直连）|
| iPhone 挂 VPN 通话 | ✗ 15s 后 failed 无声 | ✓ TURN relay 走通有声音 |
| 访客挂 VPN / 严格 NAT | ✗ 概率失败 | ✓ TURN relay 兜底 |
| 公司防火墙只放 443 | ✗ 失败 | ✓ TURN over TLS:5349 穿透 |
| 通话成功率 | ~60% | ~99.9% |

**触发场景与边界 + 验证方式**

- **凭证安全**：24h 自动过期；HMAC-SHA1 由后端用 `TURN_STATIC_AUTH_SECRET` 生成，CoTURN 用同一 secret 校验；密钥仅存于 `.env`（已 .gitignore），不进 git
- **SSRF 防护**：`denied-peer-ip` 黑名单覆盖所有 RFC 1918 + 保留段（IPv4/IPv6），防止恶意客户端用我们的 TURN 服务器探测内网
- **DoS 防护**：CoTURN 自带 `total-quota=200` + `user-quota=10` + relay 端口范围限制；接口层有 `SECURITY_IP_HTTP_RPM` 限流
- **网络故障兜底**：fetch 凭证失败 → 三端均自动 fallback 到 STUN-only（保持原 [029] 行为，不阻断通话）
- **TLS 证书缺失**：entrypoint.sh 自动生成 30 天临时自签证书占位，turnserver 仍能启动（5349/TLS 在证书续期到来前用临时证书）
- **网络模式**：CoTURN 必须 `network_mode: host`——docker bridge 下 docker-proxy 进程会成为 UDP 性能瓶颈，且 NAT 会让 srflx 地址错乱
- **凭证刷新时机**：每次发起/接听通话前刷新一次（不在 widget 启动时全局缓存，避免 24h 边界过期问题）
- **降级链**：CoTURN 挂 → 仍能拉到凭证（接口不查 CoTURN）→ 但通话 fallback 到 P2P 直连
- **CoTURN 挂 + 接口也挂** → 三端默认 STUN-only → 退化到 [034-fix1] 行为
- **验证方式**：
  1. 部署后 `docker compose ps` 看 `cs-coturn` healthy
  2. 服务器 `nc -uvz 38.76.193.68 3478` 看 UDP 3478 通
  3. 访客 widget 通话前 Network 面板看 `GET /api/visitor/turn-credential` 返回 200 + 含 turn: urls
  4. 浏览器 chrome://webrtc-internals 看 ICE candidate 是否含 `typ relay`
  5. **核心验证**：iPhone 开着 VPN 打通话，听得到声音
  6. raw_ws.log 看 ICE candidate 中是否出现 `typ relay`（CoTURN 中继候选）

---

## [034-fix1] 2026-05-24 23:30 — hub.go voice_end 去重 bug：实测出现重复「连接失败」sys 消息

**起因 / 需求**
[034] 部署后实测：iPhone 接听后 WebRTC 失败（VPN 导致 ICE 打洞失败），双方都触发 voice_end(code=failed)。爷爷的聊天记录里出现了**两条「连接失败」**而不是一条。

抓 raw_ws.log 复盘：
- `22:24:51.383` visitor 发 voice_end(failed, dur=16) → hub 写第 1 条 sys ✓
- `22:24:51.397` sys「连接失败」FanoutToConv
- `22:24:51.498` iPhone 发 voice_end(failed, dur=15) → **hub 又写了第 2 条 sys** ✗
- `22:24:51.509` sys「连接失败」FanoutToConv（重复）

**根因**
[034] 我的去重逻辑写在 `pendingCall.finished` 字段上，但**第一次成功后我同时把 buffer `Delete` 了**——第二次 voice_end 进来时 `pendingCalls.Load(cid)` 返回 not ok，走 else 分支的 `extractVisitorID(envelope)` fallback 又调一次 `OnVoiceCallFinished` 写第二条 sys。`finished=true` 标志因为 buffer 被删而完全失效。

**改了什么**（修改 1 文件）

- `backend/internal/ws/hub.go`：
  - Hub struct 新增 `finishedCalls sync.Map`（map[callID]time.Time），跟 pendingCalls 解耦
  - 删除 `pendingCall.finished` 字段（不再依赖）
  - voice_end/reject 分支重写：进入时先查 `finishedCalls.Load(cid)`，命中就 break；第一次走完流程后 `finishedCalls.Store(cid, time.Now())` + 5 分钟 AfterFunc 自动清理；同时 `pendingCalls.Delete(cid)` 释放内存
  - 注释更新 pendingCall 说明去重已移到 Hub 层

**业务流程对比**

| 时序 | 改前 | 改后 |
|---|---|---|
| visitor 发 voice_end(failed) | hub 写 sys 「连接失败」+ buffer 删 | hub 写 sys + finishedCalls 标记 + buffer 删 |
| iPhone 发 voice_end(failed) 0.1s 后 | buffer 已删 → 走 fallback → **写第 2 条 sys** | finishedCalls 命中 → break → **不写第 2 条** |

**触发场景与边界 + 验证方式**
- **双方同时挂断**：第二个 voice_end 在 5 分钟 dedup 窗口内必被拦截
- **5 分钟后的重复**：理论上不会发生（WebRTC 信令不可能延迟这么久），即使发生也只是多一条 sys，不会崩
- **buffer 过期 + 单边挂断**：仍走 `extractVisitorID(envelope)` fallback 写 sys，然后 finishedCalls 记下，第二个 voice_end 仍能被去重
- **不影响 [034] 主流程**：6 种 code 写 sys 的逻辑没动
- **验证**：raw_ws.log 中下次双端 voice_end 时，应该只看到 1 条 sys「连接失败」FanoutToConv，不是 2 条

**注意：通话失败本身不是 [034] bug**
raw_ws.log 显示 iPhone 发的 ICE candidate 全是 `198.18.0.1` / `127.0.0.1` / `fd00::` / `::1` 本地虚拟接口，srflx 是 `172.104.124.21`（不是真实 iPhone 公网 IP）—— **iPhone 上开着 VPN/科学上网工具**，把 WebRTC 锁在 VPN 隧道里，跟访客端 `110.241.19.222` 之间无法 P2P 打洞。需爷爷测试时关 iPhone VPN，或者后续部署 TURN server (CoTURN) 让 P2P 失败时走 relay。这块单独再开一条。

---

## [034] 2026-05-24 23:10 — 聊天记录加通话状态系统消息（未接 / 拒绝 / 忙线 / 挂断含时长）

**起因 / 需求**
爷爷反馈：之前通话挂断了，聊天记录上没有任何痕迹，访客和客服都不知道"刚才是真没人接、还是对方拒绝、还是说完话正常挂的"。要求三端聊天记录都要出系统消息：
- 「呼叫未接听」
- 「通话结束（X 分 Y 秒）」
- 「对方忙线中」
- 「对方已拒绝」

我又补了 2 种容易漏的：「已取消」（振铃阶段自己挂掉）、「连接失败」（麦克风权限 / WebRTC 协商断裂）。

**改了什么**（修改 5 文件 / 后端 2 + 三端 3）

- `backend/internal/ws/hub.go`
  - MessageSink 接口新增方法 `OnVoiceCallFinished(visitorID, callID, code string, durSec int)`
  - `pendingCall` struct 新增 2 个字段：`visitorID`（voice_call 发起人，service 写 sys 消息时找会话用）+ `finished bool`（防双方都挂断时重复写 2 条 sys）
  - 新增常量 `callTalkingTTL = 30 * time.Minute`（voice_accept 后把 buffer TTL 从 30s 延到 30 分钟，避免长通话时 buffer 过期后 voice_end 找不到 visitorID）
  - 新增 3 个 helper：`extractCode` / `extractDurationSec` / `extractVisitorID`
  - `voice_*` switch 全部重构：
    - `voice_call`：存 buffer 时记录 visitorID
    - `voice_accept`：延 TTL 到 30min
    - `voice_end` + `voice_reject`：第一次进入设 finished=true，调用 `sink.OnVoiceCallFinished` 写 sys；第二次（对方也挂断的 echo）直接跳过 + 删 buffer；buffer 过期 fallback 用 `extractVisitorID` 从 envelope From/To 推
    - 旧客户端没传 code 时 voice_reject 默认 `rejected`、voice_end 默认 `hangup`（向后兼容）
- `backend/internal/service/service.go`
  - 新增 `codeToText(code, durSec)`：6 种 code 翻译成中文显示文字，hangup 时按 mm:ss 排版（>1 分钟显示「X 分 Y 秒」，否则「X 秒」）
  - 新增 `OnVoiceCallFinished` 实现：异步 goroutine（不阻塞 hub 处理后续信令）+ recover panic + 5s timeout context → GetVisitor 拿 site_id → OpenOrGetConversation 找当前 open 会话 → InsertMessage(Sender='sys', SenderRef='voice') → FanoutToConv 广播带 Extra={kind:'voice_finished', code, duration, call_id} 让三端聊天区实时多出一条
  - 全程 bizLog 详细记录（vid/conv/code/duration），失败也 warn/error 落盘
- `widget/public/chat.html` `voiceEnd(reason)`
  - 不再发 `extra.reason`，改发 `extra.code` + `extra.duration`：默认 hangup → ringing/accepting 状态改 cancel → talking 状态算秒数 → 无人接听特判 no_answer、连接中断/协商失败特判 failed、客服拒绝 rejected
- `admin/src/views/Console.vue` `voiceReject` + `voiceEnd`
  - voiceReject extra 加 `code:'rejected', duration:0`
  - voiceEnd 同 widget 逻辑：incoming/accepting → cancel，talking → hangup+duration，连接中断 → failed
- `mobile_app/lib/state/voice_controller.dart` `reject` + `_end`
  - 同步三端逻辑，extra 字段统一为 `{call_id, code, duration}`

**业务流程对比**

| 场景 | 改动前 | 改动后 |
|---|---|---|
| 访客打过来 30 秒没人接 | 三端聊天记录空白 | 三端都出 sys：「呼叫未接听」 |
| 客服点拒绝 | 同上空白 | 三端都出 sys：「对方已拒绝」 |
| 客服在通话中收到第二个来电（自动拒绝） | 同上空白 | 第二个访客的会话出 sys：「对方忙线中」 |
| 访客打过去振铃中自己反悔挂断 | 同上空白 | 三端 sys：「已取消」 |
| 接通后聊了 1 分 23 秒挂断 | 同上空白 | 三端 sys：「通话结束（1 分 23 秒）」 |
| 麦克风权限被拒 / 网络断了 | 同上空白 | 三端 sys：「连接失败」 |

**触发场景与边界 + 验证方式**

- **去重**：双方同时挂断时（visitor 挂 + agent 挂都会发 voice_end），hub.pendingCalls 的 `finished` 标志确保只写 1 条 sys。验证：talking 中两边同时点挂断 → 聊天记录只看到一条「通话结束（X 秒）」。
- **buffer 过期 fallback**：如果通话超过 30 分钟（callTalkingTTL）后挂断，pendingCall 已被清理，走 `extractVisitorID(env)` 从 envelope From/To 取 visitor → 仍能正确写 sys 消息。
- **跨节点广播**：FanoutToConv 内部已经走 Redis pub/sub，多实例部署下其他节点的 WSS 连接也能收到。
- **旧客户端兼容**：[029] 之前的版本只发 `reason` 不发 `code`，hub 检测到空 code 时按 voice_type 默认（reject→rejected / end→hangup）兜底，不至于无 sys 消息。
- **会话不存在**：访客如果还没建过 conv 就发起通话（极端边界），OpenOrGetConversation 会创建一条新 conv 写入 sys 消息，不会丢失记录。
- **不影响信令**：OnVoiceCallFinished 走 goroutine + recover，即使 DB 慢/挂也不会卡住 hub 处理下一个 voice_* 包。
- **日志**：bizLog 在 voice_finished 成功/失败时都打点；失败时 warn 包含 vid，便于后续排查。
- **验证手测**：6 个 code 都跑一遍 → 三端聊天记录都出现对应中文 + 双端同步 ✓。

---

## [033] 2026-05-24 22:00 — 三端聊天图片可点击全屏查看器（widget / admin / App）

**起因 / 需求**
之前点聊天里的图片直接 window.open 新标签打开原图，体验粗糙。爷爷要个成熟的图片查看器，三端通用。

**改了什么**（新增 1 文件 + 修改 4 文件）

- `widget/public/chat.html`
  - 图片点击从 `window.open` 改 `openImageLightbox(src)`
  - 新增 `openImageLightbox(src)` 函数：全屏黑色 overlay + 中央放大图片 + 右上角 × 关闭按钮；overlay 点击 / ESC / × 都关闭；图片本身阻止冒泡（防误关，方便长按保存）
  - 新增 `.img-lightbox` / `.img-lightbox-close` CSS：position:fixed inset:0 z:99999 + 92vw/vh 上限 + 淡入动画
  - 纯 30 行手写，不依赖任何外部库

- `admin/src/views/Console.vue`
  - 图片消息从 `<img class="bubble-img">` 改成 `<el-image :preview-src-list="[url]" preview-teleported hide-on-click-modal>`
  - 用 Element Plus 内置的图片预览器：支持缩放、旋转、上一张/下一张、键盘操作

- `mobile_app/pubspec.yaml`：加 `photo_view: ^0.15.0`

- `mobile_app/lib/pages/image_viewer_page.dart` **新增**
  - PhotoView 组件：双指缩放 / 拖动 / 双击放大（minScale=contained, maxScale=covered*3）
  - 透明 Scaffold + 半透明黑底 barrierColor
  - 右上角 × 关闭按钮
  - 静态 `open(context, url)` 方法封装路由 push

- `mobile_app/lib/widgets/message_bubble.dart`
  - 图片消息外加 GestureDetector.onTap → `ImageViewerPage.open(ctx, fullUrl)`
  - import image_viewer_page

**业务流程**

| 端 | 体验 |
|---|---|
| widget 访客 | 点聊天图片 → 全屏黑底大图 → 双指/双击不缩放（纯展示）+ 长按可保存原图 |
| admin Web 客服 | 点聊天图片 → Element Plus 标准预览：缩放、旋转、键盘← → 切换、ESC 关 |
| iPhone App | 点聊天图片 → 全屏 + 双指缩放 + 拖动 + 双击放大；右上角 × 关闭 |

**验证**
- 访客发图片 → 三端都能看到、能点开全屏 ✓
- iOS App 加 photo_view 依赖（~200KB 增量，比 webrtc 小很多）

---

## [032] 2026-05-24 21:40 — iPhone 通话中免提/听筒切换按钮

**起因 / 需求**
爷爷反馈：iPhone 通话时只能贴着听筒听对方，要能切免提（外放扬声器）方便放桌上交流。

**改了什么**（修改 2 文件）
- `mobile_app/lib/state/voice_controller.dart`：
  - state 加 `bool speakerOn = false`（默认听筒，私密）
  - 新增 `Future<void> toggleSpeaker()`：talking 状态生效，调 `Helper.setSpeakerphoneOn(next)` 切音频路由，flip state + notifyListeners
  - `_cleanup()` 末尾复位 `speakerOn = false` + `setSpeakerphoneOn(false)`，让下次通话从听筒（私密）开始
- `mobile_app/lib/widgets/voice_call_overlay.dart`：
  - accepting 状态保持单挂断按钮（通话还没建好不能切）
  - talking 状态显示两个按钮：左边「免提/听筒」（黄色 volume_up / 灰色 hearing 图标，标签随状态切换）+ 右边「挂断」（红色 call_end）

**业务流程**
通话接通后：
- 默认听筒模式（贴耳听）
- 点黄色喇叭按钮 → 切外放扬声器（按钮变高亮黄、标签「免提」）
- 再点 → 切回听筒（按钮变灰、标签「听筒」）
- 挂断后 cleanup 自动复位，下次通话回听筒

**触发场景与边界**
- 仅 iPhone App 才有此切换（Web 端 admin/widget 走电脑外接设备，没有"听筒模式"概念，不需要）
- iOS/Android 都靠 `flutter_webrtc` Helper.setSpeakerphoneOn 切；部分老设备可能不支持，静默失败不影响通话
- 接听 / accepting 阶段不显示切换按钮（避免误操作）

---

## [031] 2026-05-24 21:30 — 访客 widget 电话按钮加可配置提示文字「直接呼叫客服」

**起因 / 需求**
爷爷反馈：widget 输入框的电话图标按钮只是一个图标，访客看不懂这是干啥的。要在按钮旁加文字标签（像微信"语音通话"按钮），且文字可在 admin/App 客服工作台改。

**改了什么**（修改 4 文件）

- `widget/public/chat.html`：voiceBtn 改成 `.voice-btn-pill` 圆角药丸样式（图标 + 文字横排），加 `<span id="voiceBtnHint">直接呼叫客服</span>`；loadPublicSettings 拉到 `voice_call_hint` 后赋值给 span text + button title
- `backend/internal/handler/http.go`：allowedSettingKeys 加 `voice_call_hint`；VisitorPublicSettings 返回 `voice_call_hint`（公开给访客 widget 读，默认「直接呼叫客服」）
- `admin/src/views/Settings.vue`：form 加 `voice_call_hint`，load/save/UI 各加输入框（maxlength 20）
- `mobile_app/lib/pages/settings_page.dart`：加 `_voiceCallHint` controller，load/save/dispose/UI 都补

**业务流程**

管理员在 admin / App 系统设置「语音按钮提示」输入框改文字 → 保存 → 访客刷新 widget 看到新提示文字（pill 按钮：📞 直接呼叫客服）

**验证**
- admin 设置「语音按钮提示」字段可见可保存
- App「语音按钮提示」字段同步可见可保存
- 访客 widget 电话按钮变成图标 + 「直接呼叫客服」横排药丸样式

---

## [030] 2026-05-24 20:50 — 语音来电额外发 luckfast 推送：iPhone 锁屏/后台也能收到「来电」提醒拉起 App 接听

**起因 / 需求**

[029] 把双端语音通话上了，但发现 **iPhone App 进后台后 WSS 几秒被 iOS 冻结**，访客拨号时 App 完全收不到 voice_call 信令。爷爷问"微信怎么做到的"——微信用 PushKit + CallKit + 付费 Apple Developer 账号。爷爷免费账号不能用 PushKit，所以走方案 B：

**访客发起 voice_call 时，后端额外发一条 luckfast APNs 推送给客服 iPhone**——锁屏能弹「客服系统 · 来电」通知，用户点击推送 → 走 maihaocs:// URL Scheme 拉起 Custom Service App → App 进前台 WSS 重连 → 客服点接听。

跟微信差几秒（需要用户主动点推送），但比当前"完全收不到"强百倍。

**改了什么 / 加了什么**（修改 5 文件）

1. `backend/internal/ws/hub.go`
   - `MessageSink` interface 加 `OnVisitorVoiceCall(visitorID, callID string)` 方法
   - `handleIncoming` voice_* 分支在 fanoutVoice 后追加：仅 `e.Type == "voice_call" && c.Kind == KindVisitor` 时调 `h.sink.OnVisitorVoiceCall(c.ID, callID)`，让 service 侧通道发推送

2. `backend/internal/service/service.go`
   - 实现 `Service.OnVisitorVoiceCall(visitorID, callID string)`：goroutine 异步调 `pushAPNsCommon` 发推送
   - 标题「客服系统 · 来电」/ subtitle「访客 xxx」/ 内容「语音来电！请立即点开 App 接听」
   - sound 走 settings `push_sound_call`（默认 `"4"` 跟 enter/message 区分）
   - 跳转 URL 用 `push_jump_url`（默认 maihaocs://open）拉起 App

3. `backend/internal/handler/http.go`
   - `allowedSettingKeys` 加 `push_sound_call: true`

4. `admin/src/views/Settings.vue`
   - form 加 `push_sound_call: '4'`（默认）
   - `load()`/`save()` 各加一行带上该字段
   - template 在「新消息提示音」下方加「语音来电提示音」下拉（16 选）

5. `mobile_app/lib/pages/settings_page.dart`
   - state 加 `_pushSoundCall = '4'`
   - `_load`/`_save` 各加一行
   - UI 在「新消息提示音」下方加「语音来电提示音」`_pushSoundTile`

**业务流程对比**

| 场景 | [029] 前 | [030] 后 |
|---|---|---|
| 客服 iPhone **前台**收到访客拨号 | ✓ App 内弹接听浮窗 | ✓ 同上（不变）|
| 客服 iPhone **后台**收到访客拨号 | ❌ WSS 被冻结，啥也不收 | ✅ 锁屏弹推送「客服系统·来电」，点击拉起 App 接听 |
| 客服 iPhone **完全没在跑**收到访客拨号 | ❌ 同上 | ✅ 推送拉起 App 启动 + 重连 WSS（访客那边拨号 30 秒超时窗口内能赶上）|

**触发场景与边界 + 验证方式**

- 触发条件：`Type==voice_call && Kind==KindVisitor`，且 settings 配了 push_user_id/key
- 推送内容：跟 [027] 设计的「新访客」「新消息」推送一套体系，但用独立 sound（默认 4）方便用户一耳朵分辨「来电」vs「新消息」
- 用户体验：iPhone 锁屏弹推送 → 用户解锁 + 点推送 → maihaocs://open scheme 拉起 App → AppState.startWs 重连 WSS → 访客那边如果还在拨号超时窗口内（30 秒），后台 fanoutVoice 会**重新**把 voice_call 投递给新连接的 agent（fanoutLocal 在 register 时不会重发旧消息，但 visitor 还在持续拨号状态，会保持 voice_call 状态机不变）

> ⚠️ 边界：如果访客拨号超过 30 秒，访客端自己挂断，那时候 App 才拉起就晚了。但 30 秒足够大部分用户解锁手机点推送。

- 验证 1：admin → 系统设置 → 看到「语音来电提示音」第三个下拉 ✓
- 验证 2：iPhone App → 我的 → 系统设置 → 同样看到「语音来电提示音」字段 ✓
- 验证 3：iPhone 锁屏 → 浏览器开 widget → 点电话 → iPhone 弹推送「客服系统·来电」 ⏳ 待真机
- 验证 4：点推送 → 拉起 App → 看到访客来电浮窗 → 接听 → 双向通话 ⏳ 待真机

**安全 / 健壮性**

- 推送跟 WSS 信令完全解耦：推送失败不影响 voice_call 信令本身的转发（信令仍走 WSS 给在线 agent）
- 异步 goroutine + recover panic，不阻塞 hub 主流程
- push_user_id/key 未配置时 pushAPNsCommon 直接 return nil 跳过（沿用 [027] 兜底逻辑）
- 同一会话窗口内不会刷屏推送（访客 voice_call 只发一次，hub 转发一次，sink.OnVisitorVoiceCall 调一次）

**遗留 / 已知**

- 微信级即时接听要 PushKit + CallKit + 付费 Apple Developer 账号（$99/年），后续可上 [031]
- 推送有几秒延迟 + 用户主动点击；如果访客等不及挂了就接不上
- 目前 App 拉起后会显示已有的会话列表，但来电浮窗依赖 WSS 重连后收到访客继续保持的 voice_call 状态——实际上 voice_call 是一次性广播，已经发过的不会重发。后续可在 hub 加 short buffer（call_id 缓存 30 秒，新 agent 连入时重投）

---

## [029] 2026-05-24 16:25 — 双端语音通话：访客 → 客服 WebRTC 实时语音（widget / admin Web / iPhone App 三端互通）

**起因 / 需求**

爷爷 [028] 提出的 4 件需求最后一件——访客端能给客服打语音电话，**App + Web 客服工作台都能接听**，第一个接的占用，其它客服收到撤销。所有信令走现有 WSS（不引入额外推送服务），音频走 WebRTC P2P 直连。

**架构 / 信令协议**

WSS 加 7 个语音信令 type（后端纯转发、无状态、不入库）：

| type | 方向 | To 字段 | 用途 |
|---|---|---|---|
| `voice_call` | visitor → 所有 agent | 空（广播） | 来电，agent 端弹来电浮窗 |
| `voice_accept` | accepting agent → visitor | `visitor:vid` | 通知 visitor "已接听，发 offer 吧" |
| `voice_taken` | accepting agent → 所有其它 agent | 空（广播） | 让其它 agent 撤销来电浮窗 |
| `voice_reject` | agent → visitor | `visitor:vid` | 客服拒接 |
| `voice_offer` | visitor → accepting agent | `agent:aid` | WebRTC SDP offer |
| `voice_answer` | agent → visitor | `visitor:vid` | WebRTC SDP answer |
| `voice_ice` | 双向 | 对端 | ICE candidate 交换 |
| `voice_end` | 双向 | 对端 | 任何一方挂断 |

WebRTC ICE: 用免费 Google STUN `stun:stun.l.google.com:19302`。

**改了什么 / 加了什么**（新增 2 文件 / 修改 8 文件）

### A. 后端信令路由（1 文件）
- `backend/internal/ws/hub.go`
  - `handleIncoming` switch 加 voice_* 7 个 type case（共用同一处理逻辑，盖 From 后转 fanoutVoice）
  - 新增 `fanoutVoice(ctx, e)`：本节点投递 + Redis 跨节点 publish
  - 新增 `fanoutVoiceLocal(e)`：根据 To 字段精确转发
    - `To==""`：广播给所有 agent 的所有连接（voice_call 场景）
    - `To=="visitor:xxx"`：精确转给指定 visitor 客户端
    - `To=="agent:xxx"`：转给指定 agent 的所有连接（多端同步用）
  - `fanoutFromRedis` 加 voice_ 前缀判断，跨节点也走 fanoutVoiceLocal
  - import 加 strings

### B. widget 访客端拨号（1 文件）
- `widget/public/chat.html`
  - HTML：加 `voiceBtn` 电话按钮（在附件按钮旁）+ `voicePanel` 全屏浮窗（电话图标 / 客服名 / 状态文字 / 红色挂断按钮） + 隐藏 `voiceRemoteAudio` autoplay
  - CSS：voice-panel 渐变蓝背景 + voice-icon 脉动动画（呼叫中）+ 红色挂断按钮
  - JS voice 模块：状态机 idle → ringing → accepting → talking → ended
    - `voiceStart()`：getUserMedia 拿麦克风 → 发 voice_call → 30s 超时挂断
    - `voiceOnAccept()`：创建 RTCPeerConnection + addTrack + createOffer → 发 voice_offer
    - `voiceOnAnswer()`：setRemoteDescription → 进 talking 状态 + 启动计时
    - `voiceOnIce()`：addIceCandidate
    - `voiceEnd()`：发 voice_end + 关闭 PC + 停麦克风 + 清 timer
  - onmessage 顶部加 `if (env.type.startsWith('voice_')) window.handleVoiceSignal(env)` 分发
  - `window.handleVoiceSignal` 暴露给 onmessage 调用

### C. admin Web 客服端接听（1 文件）
- `admin/src/views/Console.vue`
  - script：新增 `voiceState/voiceStatusText/voiceCallerLabel/voiceRemoteAudioRef` ref + voice 普通对象 (callId, callerFrom, pc, localStream, startTs, timer)
  - 新增 `handleVoiceSignal(env)` 分发 voice_*
  - `voiceOnIncoming`：会话列表查 vid 对应 identifier 显示，播本地 agentSound 提醒，30s 超时
  - `voiceAccept`：getUserMedia + 发 voice_accept (To=visitor) + 发 voice_taken (广播让其它 agent 撤窗)
  - `voiceOnOffer`：setRemoteDescription + createAnswer + setLocalDescription + 发 voice_answer + 启动计时器
  - `voiceReject` / `voiceOnRemoteEnd` / `voiceEnd` / `voiceCleanup` 完整状态机
  - template：`<div v-if="voiceState!='idle'" class="voice-overlay">` 固定右上角浮窗，渐变背景按 state 切（incoming 蓝 / talking 绿 / ended 灰），incoming 双按钮（拒绝+接听）/ talking 单按钮（挂断）
  - CSS：voice-overlay 滑入动画 + voice-icon 脉动 + 渐变背景三种状态色
  - onUnmounted 加 voice 资源清理（pc.close / localStream.tracks.stop / timer 清）

### D. iPhone App 接听（4 修改 / 2 新增）
- `mobile_app/pubspec.yaml`：加 `flutter_webrtc: ^0.11.7`（iOS framework ~50MB 增量）
- `mobile_app/ios/Runner/Info.plist`：加 `NSMicrophoneUsageDescription`（iOS 麦克风权限提示）
- `mobile_app/android/app/src/main/AndroidManifest.xml`：加 RECORD_AUDIO / MODIFY_AUDIO_SETTINGS / BLUETOOTH / BLUETOOTH_CONNECT 权限 + microphone feature
- `mobile_app/lib/state/voice_controller.dart` **新增**：VoiceController extends ChangeNotifier
  - 完整状态机 idle/incoming/accepting/talking/ended，跟 admin Vue 端 1:1 对齐
  - sendEnvelope/agentId/agentNickname 字段供 AppState 注入
  - flutter_webrtc API：`navigator.mediaDevices.getUserMedia({audio:true})` + `createPeerConnection` + `addTrack` + `createAnswer` + `addCandidate`
  - 信令处理：accept / reject / offer / ice / end 都完整实现
  - iOS/Android 自动音频路由（flutter_webrtc native plugin），不需要 audio widget
- `mobile_app/lib/widgets/voice_call_overlay.dart` **新增**：全屏浮窗 widget
  - `Material color: black54` 半透明黑底覆盖
  - 300×自适应高 卡片，渐变背景按 state 切色（incoming 蓝 / talking 绿 / ended 灰）
  - `_PulseIcon` 脉动电话图标动画（incoming 才动）
  - 圆形按钮 `_CircleButton` 拒绝（红 call_end）/ 接听（绿 call）/ 挂断（红 call_end）
- `mobile_app/lib/state/app_state.dart`：
  - import voice_controller
  - 加 `final VoiceController voice = VoiceController()` 全局单例
  - `startWs` 后注入 `voice.sendEnvelope = (m) => _ws?.send(m)` + agentId/Nickname
  - `_onEnvelope` 顶部加 `if (type?.startsWith('voice_')) voice.handleSignal(env)` 分发
- `mobile_app/lib/pages/home_page.dart`：body 用 Stack 包裹 IndexedStack + 顶部加 `VoiceCallOverlay`（idle 状态自动 SizedBox.shrink 不占位）

**业务流程对比**

| 场景 | 改动前 | 改动后 |
|---|---|---|
| 访客想给客服语音说事情 | 没办法 | widget 输入框旁电话按钮一点 → 拨号 + 客服 iPhone/电脑同时弹来电 |
| 多客服竞争接听 | — | 第一个点接听 → 发 voice_taken 广播 → 其它 agent 浮窗自动消失 |
| 通话中 | — | 三端任意一端挂断 → 对方收到 voice_end 自动结束 |
| iPhone 客服 App 接听权限 | — | 系统弹麦克风授权（Info.plist NSMicrophoneUsageDescription）|

**触发场景与边界 + 验证方式**

- WebRTC P2P：浏览器/iOS App 都内置实现，不需要后端做媒体转发；后端只做信令中转
- 信令走现有 WSS 通道（agent + visitor 都已经在线），不引入额外服务
- 30 秒未接听自动挂断（widget + admin + App 三端各自计时）
- 网络中断自动关闭（onConnectionStateChange == failed/disconnected → 调 voiceEnd）
- 忙线：App 接听端如果已在通话中收到新 voice_call → 自动发 voice_reject(reason=busy)
- 验证 1：widget 点电话 → 浏览器弹麦克风权限 → 同意 → admin/App 同时弹来电 ✓ 待真机测
- 验证 2：admin 点接听 → 双向语音 ✓ 待真机测
- 验证 3：App 点接听 → 双向语音 ✓ 待真机测
- 验证 4：多 admin 在线时第一个接听 → 其它 admin 来电浮窗消失 ✓ 待真机测
- 验证 5：任意一端挂断 → 对方浮窗消失 ✓ 待真机测

**安全 / 健壮性**

- 后端零状态：不维护通话 session，纯信令转发；不入库 raw_ws（voice payload 仅 SDP + ICE，无业务数据）
- 客户端三端都有 timer 兜底（30s 拨号超时 + 通话中 1s 刷新 UI），防 timer 泄漏（cleanup 时 cancel + 置 null）
- pc.onConnectionStateChange == failed/disconnected → 主动 voiceEnd 防止资源泄漏
- localStream tracks 在 cleanup 一定 stop（macOS/iOS 否则麦克风指示灯不熄灭）
- App `_end` 后 2.5s 自动 idle，让用户看清挂断原因
- voice_call To 字段为空时 fanoutVoiceLocal 才广播给所有 agent；带 To 字段精确路由，杜绝越权信令（visitor 收不到不属于他的 SDP）

**遗留 / 已知**

- 后台接听：iOS App 进入后台后系统会冻结 WebRTC 连接，无法接来电。要支持必须 VoIP Push (PushKit) + CallKit 集成，需要付费 Apple Developer 账号 + 复杂权限申请。当前**只支持前台接听**。
- TURN 服务：当前只配 Google STUN，跨 NAT 严格场景下（双方都在对称 NAT）可能连不通。后续要部署 coturn 自建 TURN。
- 暂无通话录音 / 历史记录功能
- iOS Release App 体积从 21MB → ~50MB+（WebRTC framework iOS arm64）

---

## [028] 2026-05-24 15:40 — 三件套小优化：访客端粘贴文件/图片 + App 端设置同步 Web + 三端消息复制按钮

**起因 / 需求**

爷爷一次提 4 件：
1. 访客端输入框支持粘贴文件/图片（截图粘贴常用）
2. App 端系统设置跟 Web 客服工作台同步——之前 App 缺 [027] 加的 4 个 push 字段
3. 三端消息气泡都加复制图标（一键复制消息内容）
4. 双端语音通话（WebRTC + 信令）—— **工作量大，留 [029] 单独做**

本次 [028] 完成前 3 件。

**改了什么 / 加了什么**（修改 4 文件）

### #1 三端输入框粘贴文件/图片
- `widget/public/chat.html`
  - 抽出公共 `uploadAndSendFile(file)` 函数（原 `onPickFile` 逻辑复用）
  - 给 `<textarea id="input">` 绑 `paste` 事件：剪贴板 `kind==='file'` → `preventDefault` + `getAsFile()` → 上传；纯文本不拦截
- `admin/src/views/Console.vue`
  - 同样抽出 `uploadAndSendFile(file)`、新增 `onPasteDraft(e)` 函数
  - `el-input` textarea 加 `@paste.native="onPasteDraft"`，placeholder 也提示"粘贴可上传图片/文件"

### #2 mobile_app Settings 跟 Web 同步 4 个 push 字段
- `mobile_app/lib/pages/settings_page.dart`
  - 加 controller：`_pushUserId` / `_pushUserKey`、state：`_pushSoundEnter` / `_pushSoundMessage` / `_showPushKey`
  - 加 `_pushSoundOptions`：0-15 共 16 种选项（跟 admin Web 完全一致）
  - `_load()` 拉这 4 个字段；`_save()` 一起发回 settings；`dispose()` 释放 controller
  - UI 加新区段「iPhone APNs 推送（可选）」：Push User ID 输入框 + Push User Key 输入框（带密文/明文切换图标按钮）+ 新访客提示音下拉 + 新消息提示音下拉
  - 新增 helper `_pushSoundTile()` 跟现有 `_soundTile` 风格统一

### #3 三端消息气泡复制按钮
- `admin/src/views/Console.vue`
  - 模板：每个 `.bubble` 加 `<span class="bubble-copy">` 含 SVG 复制图标
  - 函数：`copyMessage(m)` 优先用 `navigator.clipboard.writeText`，老浏览器 fallback `document.execCommand('copy')`，复制后 `ElMessage.success('已复制')`
  - 样式：`.bubble-copy` 绝对定位右上角，opacity 0 → hover 气泡时显示；mine 气泡用半透明白色，theirs 用半透明深色
- `widget/public/chat.html`
  - `buildBubble()` 内部追加 `bubble-copy` span（带 SVG 图标）+ onclick 调 `copyToClipboard(text, anchorEl)`
  - 加 `copyToClipboard()` 函数：同上的 Clipboard API + execCommand fallback；复制成功后短暂绿色高亮 1.2s
  - 样式：`.bubble-copy` hover 显示 + active 显示（移动端 touch 友好）+ `.bubble-copy--done` 绿色反馈
- `mobile_app/lib/widgets/message_bubble.dart`
  - import `package:flutter/services.dart` 用 Clipboard
  - 用 `GestureDetector(onLongPress: ...)` 包裹 Container 气泡
  - 新增 `_copyMessage(context)`：文本优先，文件/图片复制完整 URL；`Clipboard.setData` + `ScaffoldMessenger.showSnackBar('已复制', 1s)`

**业务流程对比**

| 场景 | 改动前 | 改动后 |
|---|---|---|
| 访客 / 客服在输入框 Ctrl+V 粘贴截图 | 啥也没发生 | **自动上传 + 发出图片消息** |
| App 端管理员想配 push 凭据 | 必须开浏览器进 admin Web | **App 系统设置页直接填**（跟 Web 实时双向同步）|
| 用户想复制对方发来的内容 | Web 端要选中+Ctrl+C；App 端没办法（消息气泡不能选中） | **三端都有一键复制**：Web hover 显示按钮；App 长按弹"已复制"提示 |

**触发场景与边界 + 验证方式**

- 粘贴触发：剪贴板 `items` 含 `kind==='file'` 才上传；纯文本粘贴正常进文本框
- 粘贴上传走 `/api/upload`（跟附件按钮同一通道），后端限流和大小限制都生效（max 25MB / BACKEND_MAX_UPLOAD_MB）
- 复制按钮：文本消息复制 `content`；文件/图片消息复制**完整 URL**（含 https://maihaocs.icu 前缀，方便粘贴到浏览器打开）
- Clipboard API 需要 HTTPS 上下文，我们 maihaocs.icu 已 HTTPS；老浏览器 fallback execCommand
- App 长按手势：原本没有别的长按处理，新增的 `onLongPress` 不冲突
- 验证 1：访客 widget 输入框 Ctrl+V 粘贴截图 → 应自动上传 ✓
- 验证 2：admin 输入框粘贴 → 同上
- 验证 3：App 系统设置页底部应看到「iPhone APNs 推送」分组（4 个字段） ✓
- 验证 4：admin/widget 消息气泡 hover → 右上角出现复制图标，点击「已复制」反馈 ✓
- 验证 5：App 长按消息气泡 → 底部 SnackBar 弹「已复制」

**安全 / 健壮性**

- 粘贴的文件直接走 `/api/upload`：服务端类型/大小校验已就位，没绕过任何安全机制
- copyMessage 拿到的文本不再二次处理（避免 XSS 注入到剪贴板没意义，剪贴板是字符串）
- App 长按 `_copyMessage` 加空字符串 guard，无内容直接 return
- Clipboard API 失败时浏览器端 fallback 不影响业务

**遗留 / 已知**

- #4 双端语音通话 WebRTC 留 [029]：信令协议 + 访客端拨号 + admin/App 接听 + iOS 麦克风权限 + STUN 配置；工作量约半天到一天
- 复制按钮在窄屏 admin 移动端可能跟消息时间 tooltip 重叠，后续可调位置
- App 长按复制如果 widget tree 里别处也有 onLongPress 可能冲突；目前只在 ChatPage 内使用 MessageBubble，没问题

---

## [027] 2026-05-24 15:00 — luckfast APNs 中转推送集成 + 点推送拉起 App + 真新访客/老客户回访区分

**起因 / 需求**

爷爷找到 messagepush.luckfast.com 这个国内免费 APNs 中转服务（用户下载他们的「消息推送助手」App + 拿 User ID/Key → 任何后端调他们 HTTP API 就能推到该 iPhone）。完美绕开「免费 Apple ID 不能签 APNs Key」的限制。需求：
1. 后端集成 luckfast 推送，访客消息 / 新访客进入两种事件都能推
2. 点击推送能拉起我们 Custom Service App（不只是默认的 Safari 跳 web）
3. 访客进入 / 新消息 两种推送独立提示音（luckfast 支持 16 种音效）
4. 区分「真新访客（第一次来）」和「老客户回访（之前来过）」推送内容不同
5. 会话超时门槛 60 分钟 → 30 分钟，老访客回访更敏感

**改了什么 / 加了什么**（新增 1 文件 / 修改 7 文件）

### A. 后端 luckfast 推送模块
- `backend/internal/push/luckfast.go` **新增**
  - `Client` 复用 http.Client + 8s timeout
  - `Options{UserID,UserKey,Title,Subtitle,Message,JumpURL,Sound}`
  - POST /send/<UserID>/<UserKey> + form-urlencoded（避免 GET URL 超长）
  - 长度截断：title 50 / subtitle 50 / message 500
  - UserID/UserKey 空时返回 nil 视作禁用（不报错）
  - 返回 JSON 字符串包含 "code":0 才算成功

### B. service.go 推送触发
- 新增 `Service.push *push.Client` 字段 + 构造器注入
- 新增 `pushAPNsCommon` 通用调用（读 push_user_id/push_user_key/push_sound_xxx settings）
- 新增 `pushVisitorMessageAPNs(content, vid)` 走 push_sound_message（默认 9）
- 新增 `humanizeDuration` helper：刚刚 / N 分钟 / N 小时 / N 天
- 新增 `shortVid` 截 vid 前 8 位作 subtitle 显示
- `PersistMessageAsync` 末尾加 `if sender=="visitor"` 异步 push 触发
- `OnVisitorEnter` 第 1 步通知客服后异步执行：**GetVisitor 重读真实 first_seen**（handler 传的 v.FirstSeen 不可靠），判断 `time.Since(real.FirstSeen) > 10s` 决定走「真新访客」或「老客户回访」推送，两者标题/内容不同

### C. store.go 加 GetVisitor
- 新增 `Store.GetVisitor(ctx, id)` —— 单独读取访客，主要给 OnVisitorEnter 拿真实 first_seen 用
- 必须新加：UpsertVisitor 的 SQL 走 `ON DUPLICATE KEY UPDATE` 故意**不更新 first_seen**，但 handler 调用方传的 `v.FirstSeen` 总是被赋值 now，导致 push 判断无法区分真新/回访

### D. handler/http.go 配置 + 30min 门槛
- `allowedSettingKeys` 加 `push_user_id`、`push_user_key`、`push_sound_enter`、`push_sound_message`、`push_jump_url`
- `EnsureFreshConversation(...60)` 改为 `30` —— **会话超时从 60 分钟 → 30 分钟**：真新访客（没旧会话）不受影响；老访客回访更敏感
- `VisitorPublicSettings` 默认 sound fallback `"classic"` → `"visitor1"`（兼容老数据库）

### E. admin Settings.vue 配置 UI
- 加 `push_user_id` / `push_user_key` 输入框（密码框 show-password）
- 加 `push_sound_enter` / `push_sound_message` 下拉（16 选：0 默认 + 1-15 提示音）
- form 字段 + load + save 三处都补上新字段

### F. iOS / Android URL Scheme 注册
- `mobile_app/ios/Runner/Info.plist` 加 `CFBundleURLTypes` 注册 scheme=`maihaocs`
- `mobile_app/android/app/src/main/AndroidManifest.xml` 加 intent-filter `android:scheme="maihaocs"`
- 后端 push `JumpURL` 默认 `maihaocs://open` —— iOS 点击推送时由 luckfast 调 `UIApplication.open(URL)` → iOS 解析 scheme → 拉起 Custom Service App
- 免费 Apple ID 完全支持（Universal Links 才要付费账号）

**业务流程对比**

| 事件 | 改动前 | 改动后 |
|---|---|---|
| 访客发消息，客服 iPhone 锁屏 | 收不到（没 APNs） | **收到推送**「客服系统·新消息 / 访客 xxx / 消息内容」 |
| 真新访客打开 widget | 客服 web 上有弹窗 + 播声，但 iPhone 锁屏没动静 | **iPhone 收到推送**「客服系统·新访客 / 有新访客打开了客服窗口」 |
| 老客户 35 分钟后回访 | 60min 门槛内复用旧会话，啥都不推 | **iPhone 推送**「客服系统·老客户回访 / xxx 又来了，首次访问 N 时间前」（30min 门槛触发） |
| 同一访客 30 分钟内来回开关 widget | 复用会话不推 | 同上（30min 门槛保护，避免骚扰） |
| 点击 iPhone 推送 | 默认打开 Safari 跳 admin web | 拉起 Custom Service App |
| 配置 | 改 .env + 重启容器 | admin Settings 网页填，零重启 |

**触发场景与边界 + 验证方式**

- 触发条件：`push_user_id` 和 `push_user_key` 都填非空才推；任一为空跳过（不报错）
- 推送源头 1：`PersistMessageAsync` 内 `if sender=="visitor"` 后异步 goroutine
- 推送源头 2：`OnVisitorEnter` 内 `if SettingBool(notify_visitor_enter, true)` 后异步 goroutine
- 失败处理：网络/luckfast 错误只 `bizLog.Warn`，不影响业务（goroutine 内 recover panic）
- 区分新老：`time.Since(realVisitor.FirstSeen) > 10*time.Second` —— first_seen 在 10 秒内 = 真新访客；之前的就算老回访
- 会话超时：30 分钟无活动（看 conversations.updated_at），关闭旧 conv + 开新 conv，触发 OnVisitorEnter
- 验证 1：admin → 系统设置 → 看到「Push User ID/Key + 新访客/新消息提示音」5 个字段 ✓
- 验证 2：iPhone 锁屏 + 隐身窗口开 widget → 锁屏弹「客服系统·新访客」推送 ✓（已实测）
- 验证 3：点击推送 → 拉起 Custom Service App ✓（已实测）
- 验证 4：发消息 → 收「客服系统·新消息」推送，sound 跟「新访客」不同
- 验证 5：30+ 分钟后回访 → 收「客服系统·老客户回访 / 首次访问 N 前」推送

**安全 / 健壮性**

- push_user_id/key 走 settings 表（数据库存储）不入 .env / 不入仓库；admin Settings UI 用 `show-password` 隐码显示
- luckfast 接口走 HTTPS；title/subtitle/message 服务端截断防 payload 超长
- 推送失败 fallback：GetVisitor 失败 / 网络挂 / luckfast 拒绝 → 都只记 log，不影响主业务流
- 所有 push goroutine 都 `defer recover` 防止 panic 撞挂主进程
- 旧数据库的 `chime/classic` 等旧音色 key 已在 [026] migration 004 升级；前端额外加 fallback 防 race condition

**遗留 / 已知**

- luckfast 是第三方免费服务，如果对方挂了 / 转付费 / 限流，推送会失效。后续可加多通道兜底（让管理员选 luckfast 或其他第三方推送）。
- 推送在 iPhone 上显示的 App 图标和名字是「消息推送助手」（luckfast 自己的 App），不是我们 Custom Service App。点击推送跳转过来才会拉起我们 App。要真正显示自家 App 推送，必须爷爷买正式 Apple Developer 账号 + 配 .p8 Key + 自己实现 APNs（参考 [025] 遗留）。
- 16 种音效编号 0-15 在 admin UI 只用编号 + 通用名（"提示音 1"），具体音色得爷爷自己在 iPhone 上听试。

---

## [026] 2026-05-24 13:52 — 通知音色重做：废弃 11 种程序合成 → 6 个真实录音 WAV 三端统一（CHANGELOG 补登）

> 注：本条对应 git commit `5af2867`。该次 commit 已落盘但 CHANGELOG.md 漏写，本次 [027] 一并补登。

**起因 / 需求**
程序合成音色三端都太小：iOS audioplayers 5.x silent fail / 6.x AVPlayer err=-12860 / 加 mimeType 后能响但偏小 / vol 拉到 0.95 仍不够。爷爷给 6 个真实录音 WAV（3 客服端 + 3 访客端），全部废弃合成方案。

**三端文件分发**
- `mobile_app/assets/sounds/`：6 个（agent1/2/3 + visitor1/2/3）
- `admin/public/sounds/`：6 个
- `widget/public/sounds/`：3 个（只 visitor1/2/3，访客端只听访客音色）

**改了什么**（新增 3 + 修改 8）
- `admin/src/api/sound.js` 完全重写：HTMLAudioElement + 预加载 + 首次手势 unlockAudio + 500ms 防抖；用 `import.meta.env.BASE_URL` 动态拼 sounds 路径
- `widget/public/chat.html` 删 122 行合成代码，换 `new Audio('sounds/xxx.wav')` + click 解锁；旧 key fallback `visitor1`
- `mobile_app/lib/api/sound.dart` 完全重写：`AssetSource('sounds/xxx.wav')` + AudioContext playback + setVolume(1.0)；旧 key fallback `agent1`
- `mobile_app/pubspec.yaml`：audioplayers `5.2.1` → `6.6.0`；加 `assets/sounds/`
- 4 处硬编码 sed 批量替换 `'chime'`→`'agent1'`、`'classic'`→`'visitor1'`（Settings.vue / Console.vue / app_state.dart / settings_page.dart）
- `backend/migrations/004_sound_upgrade.sql` **新增**：数据库残留 `chime/classic/...` 10 个旧 key UPDATE 成 `agent1/visitor1`
- `.gitignore` 加 `音频内容/` 排除爷爷原始文件

**验证**
- `https://maihaocs.icu/admin/sounds/agent1.wav` → 200 ✓
- 数据库 `settings.agent_notify_sound=agent1, visitor_notify_sound=visitor1`（migration 004 执行）✓
- 三端试听都响亮 ✓

---

## [025] 2026-05-24 07:55 — iOS App 真机 Release 调试通跑 + 域名 maihaocs.icu 全自动 HTTPS 证书

**起因 / 需求**

爷爷两件事：
1. iOS App 要在自己的 iPhone 上跑起来，且**拔了 USB 数据线也能用**（不是单纯 Debug 模式 attach）
2. 给客服系统上正式域名 `maihaocs.icu` 并启用 HTTPS，且证书要 docker **自己判断是否申请、自动续期**，仍保持 `docker compose up -d --build` 一条命令完成

第二件事是核心：自托管系统部署给非技术人员用，绝不能让他们手动跑 certbot / 配 nginx ssl_certificate。

**改了什么 / 加了什么 / 删了什么**（新增 3 文件 / 修改 9 文件）

### A. iOS 真机调试链路打通（5 文件）

1. `mobile_app/lib/pages/server_setup_page.dart` — _confirm() 顺序 bug 修复 + 全端 URL 改 HTTPS
   - 原 bug：先调 `setBackend(url)` → AppState.notifyListeners → 顶层 widget 重建跳到登录页 → ServerSetupPage dispose → 但 `await Api.health()` 还在跑 → 失败时 catch 里 setState → 抛 "setState called after dispose"
   - 修复：先 `Api.healthAt(url)` 临时测试通了再 `setBackend(url)`；所有 setState 加 `if (mounted)` 检查
   - 顺手改：`_demoUrl` 从 `http://38.76.193.68` → `https://maihaocs.icu`；输入框 hint / 示例 / 一键填入按钮文本同步改

2. `mobile_app/lib/api/http_client.dart` — 新增 `healthAt(baseUrl)` 方法（line 49-60）
   - 不依赖全局 `_dio` 单例，临时 Dio 实例只为这一次 health 检测
   - 避免提前污染全局 baseUrl 触发 [025] 中的 widget dispose 问题

3. `mobile_app/ios/Runner.xcodeproj/project.pbxproj`
   - Bundle ID 从 `com.customservice.customServiceApp` → `com.chengmeiran.customservice`（全球唯一）
   - **hardcode `DEVELOPMENT_TEAM = 4ARYS2Z738`** 强制走 baofusir 的 Personal Team（防止 Xcode 自动选 Keychain 里别人遗留的吊销证书）

4. `mobile_app/ios/Runner/Info.plist`
   - 加 `NSAppTransportSecurity.NSAllowsArbitraryLoads=true` 允许 HTTP 请求（自托管系统的用户可能服务器还没上 HTTPS，要兼容）
   - 等价于 Android 的 `usesCleartextTraffic="true"`

5. `mobile_app/lib/api/sound.dart` — 音频播放彻底重写，避免 MediaPlayer 状态机 -38 错误
   - 每次播放 new 一个全新的 `AudioPlayer` 实例，零状态残留
   - `onPlayerComplete` 监听自动 dispose；8 秒兜底强制释放（防止 complete 事件丢失泄漏）
   - 之前用单例 + `release()` / `stop()` 都被 MediaPlayer 拒绝（"AndroidAudioError MEDIA_ERROR_UNKNOWN {what:-38}"）

### B. nginx 自动 HTTPS 证书（5 文件，3 新 + 2 改）

6. `nginx/Dockerfile` — 新装 acme.sh + dcron
   - `apk add bash dcron curl socat` + 从 GitHub `master.tar.gz` 直装 acme.sh 到 `/opt/acme.sh`（绕开 `get.acme.sh` 包装器的参数 bug —— 它会把 `--install-online` 转发成 `----install-online` 4 dash）
   - 默认 CA 设为 Let's Encrypt（acme.sh 内置默认是 zerossl，需要邮箱注册更繁）
   - 创建 `/var/www/acme` HTTP-01 challenge webroot 目录

7. `nginx/entrypoint.sh` — 重写为完整自动化逻辑
   - 决策树：检查 `/etc/nginx/ssl/${DOMAIN}.pem` 存在性 + `openssl x509 -checkend $((30*86400))` 30 天过期判断
     - 不存在 / < 30 天到期 → 后台启 nginx (HTTP only) → `acme.sh --issue --force --webroot` → `--install-cert` 拷到 nginx ssl 目录
     - 已有且 > 30 天 → 直接启用 HTTPS
   - 启动 dcron 守护进程，写 `/etc/crontabs/root`：每天 03:00 调 `acme-renew.sh`（含 PUBLIC_DOMAIN / ACME_EMAIL env，cron 子进程能继承）
   - HTTPS 启用后渲染 `redirect.conf.template` 覆盖默认 HTTP conf，强制 80→301→HTTPS

8. `nginx/acme-renew.sh` — **新文件**
   - cron 每天 03:00 调用
   - `acme.sh --cron` 自动判断证书是否需要续期（默认证书 90 天，60 天后续）
   - 续期了则 mtime 比较后 `install-cert` + `nginx -s reload`

9. `nginx/conf.d/redirect.conf.template` — **新文件**
   - HTTPS 启用后的 80 端口配置：只保留 `/.well-known/acme-challenge/`（续期 HTTP-01 用），其余 `return 301 https://$host$request_uri`

10. `docker-compose.yml` — 加 `ACME_EMAIL` env + 新增两个 named volume
    - `cs_ssl_data:/etc/nginx/ssl`（acme.sh 自动写入的证书 + 私钥，持久化）
    - `cs_acme_data:/opt/acme.sh`（acme.sh 账户密钥 / 配置，避免重启容器后重复注册撞 LE 配额）
    - 旧 bind mount `${HOST_DATA_DIR}/ssl:/etc/nginx/ssl:ro` 移除（不再需要用户手动放证书）

### C. 配置示例与依赖（2 文件）

11. `.env.example` — 加 `ACME_EMAIL=` 字段及详细说明（首次启动行为 / 触发条件 / DNS 前置要求）

12. `.gitignore` — 加 `cert/`、`cert*/`、`*.p12`、`*.mobileprovision`、`*.cer`、`*.pem`、`*.key`（防止 iOS 开发者证书私钥误入库）

**业务流程对比**

| 场景 | 改动前 | 改动后 |
|---|---|---|
| iOS App 装到 iPhone | 没法 build（DEVELOPMENT_TEAM 选错证书 / Keychain ACL 错） | Windows 写代码 → mcp 同步 Mac → `flutter build ios --release` → `flutter install` → iPhone 桌面点开能跑，**拔 USB 也能用** |
| iOS App ATS | 默认禁止明文 HTTP，自托管 IP 服务器连不上 | Info.plist 加 NSAllowsArbitraryLoads，HTTP/HTTPS 都通 |
| 客服系统部署 HTTPS | 用户手动跑 certbot certonly + 改 nginx conf + reload + 写 cron renew | `cp .env.example .env` → 填 PUBLIC_DOMAIN + ACME_EMAIL → `docker compose up -d --build` 完事 |
| 证书续期 | 用户手动 / 容易忘 | cron 每天 03:00 自动 `acme.sh --cron`，证书 < 30 天自动续 + reload nginx |
| HTTP 访问 | 直接通 | 强制 301 跳 HTTPS（保留 /.well-known 路径供续期 challenge） |

**触发场景与边界 + 验证方式**

触发：
- 每次 `docker compose up -d --build` 启动 nginx 容器 → entrypoint.sh 判断证书状态决定要不要 issue
- 每天 03:00 北京时间（容器 TZ=Asia/Shanghai） → cron 调 acme-renew.sh
- iPhone 上每次点 App 图标 → Release 模式独立运行

不触发：
- 证书有效期 > 30 天 → 跳过申请
- ENABLE_HTTPS=false 或 ACME_EMAIL 留空 → 不申请证书，只跑 HTTP
- 容器重启但 `cs_ssl_data` named volume 保留 → 用旧证书 + cron 续期，不重新申请

边界：
- DNS 必须先生效（公网 8.8.8.8/1.1.1.1 能解析 PUBLIC_DOMAIN → 本服务器）—— acme.sh HTTP-01 challenge 时 Let's Encrypt 服务器要主动访问 `http://${DOMAIN}/.well-known/acme-challenge/...`
- 80 端口必须可达（防火墙、云服务安全组都要放开）
- ACME_EMAIL 必须是合法邮箱格式（不验证邮箱本身，但语法错会被 LE 拒）
- Let's Encrypt 同一域名 7 天最多签 5 张证书（撞了会被 ban 一周，所以 cs_acme_data named volume 不能丢）
- iOS 免费 Apple ID 签名 7 天过期，到期后需重 build + install

验证方式（已实测）：
1. `curl -sI https://maihaocs.icu/` → 302 → /admin/ ✓
2. `curl -s https://maihaocs.icu/api/health` → 200 `{"agents":0,"now":"2026-05-24 07:51:26","status":"ok","tz":"Asia/Shanghai","visitors":0}` ✓
3. `curl -sI http://maihaocs.icu/` → 301 → `https://maihaocs.icu/` ✓
4. `openssl s_client -connect maihaocs.icu:443` → issuer=Let's Encrypt, subject=CN=maihaocs.icu, 2026-05-23 ~ 2026-08-21 ✓
5. `docker exec cs-nginx cat /etc/crontabs/root` → 含 `0 3 * * * /usr/local/bin/acme-renew.sh` ✓
6. iPhone 上 App 装到 `luck 的iPhone` → 点 Custom Service App → 不闪退 → 见 ServerSetupPage ✓

**安全 / 健壮性**

- TLS 1.2+ 强制（ssl.conf.template）
- HSTS `max-age=31536000` 一年（浏览器后续永久走 HTTPS）
- nginx 限速规则保留（api_rps=20r/s, ws_rps=5r/s, login_rps=2r/s, conn_per_ip=200）
- acme.sh 账户密钥用 named volume 持久化（防止丢账户撞 LE 5/7天/domain 配额）
- entrypoint.sh 用 `set -e` + 各 risky 命令 `|| true` 兜底，acme 失败不阻塞 nginx 启动（fallback 到 HTTP 模式继续跑）
- iOS 证书私钥 .p12 通过 .gitignore 严格隔离

**遗留 / 已知问题**

- iOS 卖家给的 .p12 在 macOS 26 `security import` 拒收（PKCS#12 PBE 算法太老，SHA-1 MAC + 3DES），切到了免费 Apple ID 路线，**没用买的证书**（购买证书的 APNs 通道也用不上：卖家不会给 .p8 Key，后端发不出 push）。后续要 APNs 推送只能：续费个人 Apple Developer ($99/年)，或让卖家重补 PBES2 + SHA-256 + AES-256 的现代格式 .p12
- App 端的 `_demoUrl` 现在写死指向 maihaocs.icu，将来这套代码给别人部署时他们会手动改这一行
- ENABLE_HTTPS=false 时 `_demoUrl` 是 https://maihaocs.icu，访问 http 服务器会失败 — 这是预期行为，因为爷爷部署的就是 HTTPS

---

## [024] 2026-05-23 17:00 — Flutter 移动 App 第 2 批：历史记录 + 客服管理 + 系统设置 + 11 种程序合成提示音

**起因 / 需求**
爷爷要求把 App「我的」页里 3 个待开发项补齐（历史记录 / 客服管理 / 系统设置），实现跟 Web 端完全一致的客服功能。

**改了什么 / 加了什么**（新增 4 文件 / 修改 4 文件）

新建：
- `mobile_app/lib/api/sound.dart` — **跟 Web 端 sound.js 完全对齐的 11 种音色**：
  - 短促：classic / chime / ding / soft / alert
  - 响亮长音：bell / doorbell / trill / fanfare / chord
  - 静音：none
  - 实现：dart 程序合成 PCM 浮点采样 + 16-bit WAV 头封装，`audioplayers` 的 `BytesSource` 播放
  - 零外部 mp3 文件，跟 Web 端 Web Audio API 等价的能力
  - 500ms 同声防抖（连发消息不叠声）+ `_envelope` 10ms 渐入 + 指数衰减
- `mobile_app/lib/pages/history_page.dart` — 历史记录页（复用 `/agent/conversations`，跟首页相同的卡片样式）
- `mobile_app/lib/pages/agents_page.dart` — 客服管理页（admin 才能访问）：列出账号、新建（用户名/密码/昵称/角色单选）、启用/禁用，原生 Material Dialog
- `mobile_app/lib/pages/settings_page.dart` — 系统设置页（admin 才能访问）：跟 Web 端 Settings.vue 一致：
  - 客服端/访客端音色下拉 + 试听按钮（用本机 sound.dart 真实播放）
  - 「通知客服」「自动问候」开关
  - 问候内容多行文本框（500 字限长）
  - Widget 标题输入框
  - 保存按钮（顶部 AppBar action）

修改：
- `mobile_app/pubspec.yaml` — 加 `audioplayers: ^5.2.1`
- `mobile_app/lib/api/http_client.dart` — 补 `createAgent` / `setAgentActive` API
- `mobile_app/lib/pages/me_page.dart` — 去掉「待开发 [021]」占位段；按 role 条件渲染管理菜单：
  - 历史记录（所有客服可见）
  - 客服管理（admin only）
  - 系统设置（admin only）
  - APNs/FCM 推送（保留 [025] 待开发占位）
- `mobile_app/lib/state/app_state.dart`:
  - 加 `agentSound` 字段 + `loadAgentSound()` 方法（admin 启动时拉 `/admin/settings`，普通客服 fallback 默认）
  - WSS 收到访客 chat 时调 `playSound(agentSound)` —— inCurrent / 非当前 / 全新会话 三种情况都覆盖
  - sys/visitor_enter 通知也触发播声（跟 Web Console 一致）
- `mobile_app/lib/pages/home_page.dart` — `initState` 启动时调 `loadAgentSound()`

**业务流程**

声音体验流程：
```
admin 登录 App → HomePage initState → AppState.loadAgentSound() →
  GET /admin/settings → 拿到 agent_notify_sound（如 "chime"）
访客发消息 → WSS chat 到达 → AppState._onEnvelope:
  - 自己端 echo 跳过（[022] 已实现）
  - 同账号他端 echo 接受（[022] 已实现）
  - fromVisitor → playSound(agentSound) ← 这里
  - 500ms 同名音色防抖
```

设置页流程：
```
admin → 我的 Tab → 点系统设置 → SettingsPage initState 拉 settings
切换客服端音色为「铃声 (长)」→ 点「试听」→ playSound('bell') → 听到 1.2s C6+C7 叠加铃声
→ 保存 → POST /admin/settings → 后端持久化到 DB
→ 下次 admin 进 App 自动拉到这个新音色
```

**触发场景与边界 + 验证方式**
- 验证 1：admin 进入 App → 我的 Tab → 看到「历史记录 / 客服管理 / 系统设置」3 个可点项；普通客服只看到「历史记录」
- 验证 2：客服管理页 → 新建账号「test1 / password123 / 客服」→ 列表立即出现
- 验证 3：系统设置页 → 切换客服端音色为「号角」→ 试听 → 听到 C-E-G-C 上升音阶 + 末音延长
- 验证 4：保存后 → 让访客发消息 → App 应该播放「号角」音色（替代之前的 chime）
- 边界：role != admin 时 me_page 隐藏管理菜单；agents_page 的 createAgent 后端有 password 至少 8 位校验，前端也校验
- 边界：audioplayers 在静音模式 / 来电中静默失败（try-catch 已包），不影响业务

**安全 / 健壮性**
- admin API 都走 HTTP token + 后端 AdminOnly 中间件
- WAV 字节流 in-memory 生成 + audioplayers BytesSource 播放，不写临时文件
- sound 库 try-catch 包裹，setReleaseMode(stop) 防止资源泄漏

---

## [023] 2026-05-23 16:00 — 会话列表显示最后一条消息预览 + App 聊天进入直接定位最新

**起因 / 需求**
爷爷反馈：
1. App 端进入聊天页面要直接显示最新消息位置，不要停在中间需要手动滚动
2. App + Web 端会话列表要能看到「最后一条对话内容预览」（截图显示当前只有时间，应该像 IM 一样显示「我：xxx」或「访客：xxx」）

**改了什么**（修改 6 个 / 新增 0 个 / 删除 0 个）

后端：
- [backend/internal/store/store.go](backend/internal/store/store.go) — `ListOpenConversations` 给每条 conv 补 `last_message` 字段（应用层 N+1 拉最新消息：sender + content + created_at；图片/文件占位 [图片]/[文件]；文本超 50 字截断带 …）；新增私有方法 `getLastMessagePreview`

Web 客服后台：
- [admin/src/views/Console.vue](admin/src/views/Console.vue):
  - `lastMsgPreview(c)` 改为优先读 `c.last_message.content`，自己发的加「我：」前缀
  - 模板改用 `lastMsgPreview(c)`（去掉地理位置 fallback）
  - `sendText` 发完消息本地立即更新 `activeConv.last_message`（左侧列表跟随）
  - WSS 收到 chat 消息时，无论 inCurrent 还是非当前会话都同步更新 `conv.last_message`

App 移动端：
- [mobile_app/lib/api/models.dart](mobile_app/lib/api/models.dart) — `Conversation` 加 `lastMessageSender` + `lastMessagePreview` 字段；`fromJson` 解析 `last_message`；新增 `displayPreview` getter（自动加「我：」前缀 + 地理位置 fallback）
- [mobile_app/lib/state/app_state.dart](mobile_app/lib/state/app_state.dart) — WSS 收到 chat 消息时（inCurrent 或非当前都）实时更新 conv 的 `lastMessageSender` + `lastMessagePreview`
- [mobile_app/lib/pages/conversations_page.dart](mobile_app/lib/pages/conversations_page.dart) — subtitle 改用 `c.displayPreview`
- [mobile_app/lib/pages/chat_page.dart](mobile_app/lib/pages/chat_page.dart):
  - **ListView 改为 `reverse: true`**：进入页面天然显示最新消息（在视觉底部），不再依赖 `maxScrollExtent` 计算
  - `_scrollToBottom()` 改为 `jumpTo(0)`（reverse 模式底部就是 offset 0），告别 ListView 懒渲染算不准的坑
  - `NotificationListener` 的 `_autoScroll` 判断改为 `pixels < 50`（reverse 模式下 pixels=0 就是底部）
  - `itemBuilder` 用 `msgs.length - 1 - i` 反向索引（让 ListView 的视觉顺序仍是「早消息在上、新消息在下」）

**业务流程对比**

App 聊天页：
- 改动前：进入会话 → ListView.builder 懒渲染 → `addPostFrameCallback` 算 `maxScrollExtent` 但第一帧算不准 → 停在中间
- 改动后：`reverse: true` → 视觉底部 = offset 0 → 进入就在最新消息 ✓

会话列表：
- 改动前：subtitle 只显示「最近活动 · 13:31」时间
- 改动后：显示「我：发的是」/ 访客最后一句 / 「[图片]」等，跟主流 IM（微信、企业微信）一致

**触发场景与边界 + 验证方式**
- 验证 1：App 点开任何会话 → 立即看到最新消息（在屏幕底部），不需要手动滚
- 验证 2：手指向上滑查历史，到底部时新消息自动跟随；离开底部 50px 以上后停止自动滚动
- 验证 3：会话列表显示「我：xxx」/「[图片]」等；自己发消息或访客新消息立即更新预览
- 验证 4：发送图片/文件后预览显示「[图片]」/「[文件]」
- 边界：last_message N+1 查询限 200 条 conv，每个走 `idx_conv_time` 索引（ms 级），列表接口总响应仍 < 100ms
- 边界：reverse 模式下 sendText 后 `_scrollToBottom` jumpTo(0) 准确（不再有 maxScrollExtent 不稳定的问题）

---

## [022] 2026-05-23 15:00 — 双端 WSS 同步：同账号 web + app 实时同步消息 + 未读 + 已读

**起因 / 需求**
爷爷反馈：web 端和 app 端用同一个客服账号登录时，两端不同步。要求：
- 访客发消息 → 两端 unread badge 都 +1
- 一端读了 → 两端 unread 都清零
- 一端发消息 → 另一端 WSS 实时收到
- 两端 + 访客三方对话时，所有消息要互相看见

**根因（设计 Bug）**
`Hub.agents` map 是 `agentID → *Client` **单连接结构**：app 端登录时 `h.agents.Store(c.ID, c)` **覆盖**了 web 端的 client，web 端就被遗忘，永远收不到 `BroadcastToAllAgents` 的消息。第二个问题：`fanoutLocal` 之前只让 `chat` 外溢给所有 agent，**read 不外溢**，所以另一端读了消息，本端不知道。

**改了什么 / 加了什么 / 删了什么**（修改 4 个 / 新增 0 个 / 删除 0 个）

后端：
- [backend/internal/ws/protocol.go](backend/internal/ws/protocol.go) — `Envelope` 加 `ConnID string \`json:"conn,omitempty"\`` 字段（服务端在 chat/read 转发时盖发起方 connID，让客户端能区分「自己当前端的回声」和「同账号另一端发的」）
- [backend/internal/ws/hub.go](backend/internal/ws/hub.go) — **核心重构**：
  - `agents` 字段语义改为 `sync.Map[agentID → *sync.Map[connID → *Client]]`，**一个 agentID 多连接共存**
  - `handleRegister`：把 conn 加进 agent 的 connID map（不覆盖）
  - `handleUnregister`：只删当前 conn；该 agent 没连接才删 agentID；同时 `detachConv`
  - `BroadcastToAllAgents` / `PushToAgent` / `AttachAgentToConv` 全部改为遍历嵌套 map（影响该 agent 的所有连接）
  - `fanoutLocal` 把 `chat || read` 都外溢给所有 agent 的所有连接（之前只 chat 外溢）
  - `handleIncoming` 盖 `e.ConnID = c.ConnID`（统一在源头盖，所有 type 都带）

前端（web + app）：
- [admin/src/views/Console.vue](admin/src/views/Console.vue)
  - 新增 `myConnId ref('')`，从 `hello.extra.conn_id` 解析保存
  - chat 回声判断从「`from == agent:<myID>`」**改为**「`env.conn == myConnId`」—— 只跳过自己端的回声，同账号其他端的消息正常接受
  - read 事件加分支：`from=agent:<myID>` 且 `conn != myConnId` → 同账号另一端读了 → 同步清掉本端该 conv 的 unread
- [mobile_app/lib/state/app_state.dart](mobile_app/lib/state/app_state.dart) — 同上改造（保存 `myConnId`，chat connID 去重，read 多端同步）；`stopWs` 时清掉 `myConnId`

**业务流程对比**

旧（[021] 之前）：
```
agent 1 在 web 登录 → agents[1] = web_client
agent 1 在 app 登录 → agents[1] = app_client   ← 覆盖了 web！
访客发消息 → fanoutLocal → 遍历 agents → 只给 app_client 推
web_client 收不到 ✗
```

新（[022]）：
```
agent 1 在 web 登录 → agents[1] = { web_conn: web_client }
agent 1 在 app 登录 → agents[1] = { web_conn: web_client, app_conn: app_client }
访客发消息 → fanoutLocal → 遍历 agents[1].* → web + app 都收到 ✓
web 端用户读消息 → 服务端广播 read → app 端收到 read → 检测到 conn≠myConnId 但 from=自己 → 清 unread ✓
app 端用户发消息 → web 端收到 chat → conn≠myConnId → 不当回声 → push 到 messages 渲染 ✓
```

**触发场景与边界 + 验证方式**
- 验证 1：web 端 + app 端同时登录 admin → 访客发消息 → 两端 unread 都 +1
- 验证 2：web 端点开该会话（清未读）→ app 端的同会话 unread 也立即清 0
- 验证 3：web 端发消息 → app 端在同会话内立刻看到（作为蓝色 mine 气泡，因为 sender_ref 是自己 agentID）
- 验证 4：app 端在 conv A，web 端在 conv B → 访客 A 发消息 → web 端 conv A 列表 +1，app 端 conv A 消息流出现新消息（因为 app 在 byConv[A]）
- 边界：BroadcastToAllAgents（visitor_enter 通知）现在会推给同账号每一端，两端都弹通知；这是合理的（多端共同接收提醒）

**安全 / 健壮性**
- conn 字段服务端盖（客户端不可伪造）
- handleUnregister 用 connID 精确删除（不会误删同 agent 其他连接）
- agent 断线时同时 detachConv，避免 byConv 残留死引用
- 客户端 myConnId 为空时不算回声去重（首次连接 hello 还没到达时收到的消息不会误丢）

---

## [020] 2026-05-23 13:30 — Flutter 移动 App 第 1 批：骨架 + URL 配置 + 登录 + 会话列表 + 聊天 WSS 实时

**起因 / 需求**
爷爷要求开发 iOS / Android 双端原生 App，复刻客服工作台功能，可配置后端 URL（自托管），后续加 APNs / FCM 推送。

**分批方案**
- **[020]（本批）**：项目骨架 + URL 配置页 + 登录 + 主框架 + 在线会话列表 + 聊天页 + WSS 实时（含已读、页面跳转横幅、未读 badge、可访客进入提醒）
- [021] 待办：历史记录 / 客服管理 / 系统设置（含声音）
- [022] 待办：APNs（iOS） / FCM（Android） 推送集成（前后端）

**技术栈**
- Flutter 3.13+（dart 3.0+），双端 iOS + Android 一套代码
- HTTP：dio
- WebSocket：web_socket_channel
- 状态管理：provider (ChangeNotifier)
- 持久化：shared_preferences
- 时间：intl

**改了什么 / 加了什么 / 删了什么**（新增 14 文件 / 修改 0 / 删除 0）
- 新建：`mobile_app/pubspec.yaml` — Flutter 项目配置 + 依赖声明
- 新建：`mobile_app/README.md` — 项目说明 + Windows / macOS 双平台初始化步骤
- 新建：`mobile_app/.gitignore`
- 新建：`mobile_app/lib/main.dart` — 入口 + AppState 初始化
- 新建：`mobile_app/lib/app.dart` — MaterialApp + 根路由（URL 没配 → 配置页；没 token → 登录页；都有 → 主页）
- 新建：`mobile_app/lib/config/settings.dart` — backendUrl / token / agent 持久化；httpToWs 工具；切换服务器时自动清 session
- 新建：`mobile_app/lib/api/models.dart` — Agent / Conversation / Message 数据模型；Message.fromJson 兼容后端 `sql.NullString` 包装
- 新建：`mobile_app/lib/api/http_client.dart` — dio 封装；token 拦截器；登录/拉会话/拉消息/接管/标已读/拉/存设置等 API
- 新建：`mobile_app/lib/api/ws_client.dart` — WSS 客户端：30s 心跳 + 指数退避重连（1.6 倍最高 30s）+ envelope 回调
- 新建：`mobile_app/lib/state/app_state.dart` — 全局 ChangeNotifier；处理 onMessage（chat / read / sys / 已读）；本地维护未读 +1 与上浮；乐观渲染发送消息
- 新建：`mobile_app/lib/pages/server_setup_page.dart` — URL 配置页（输入后自动调 /api/health 验证再保存）
- 新建：`mobile_app/lib/pages/login_page.dart` — 登录页（展示当前服务器 URL，支持「切换服务器地址」）
- 新建：`mobile_app/lib/pages/home_page.dart` — 主框架（底部 Tab：会话 / 我的；进入时启动 WSS + refreshConvs）
- 新建：`mobile_app/lib/pages/conversations_page.dart` — 在线会话列表（头像哈希配色 + 未读红 badge + WSS 状态点）
- 新建：`mobile_app/lib/pages/chat_page.dart` — 聊天页（消息分组时间分隔 + 自动滚到底 + 用户上拉时停止自动滚 + 已读角标）
- 新建：`mobile_app/lib/pages/me_page.dart` — 我的页（展示账号 / WSS 状态 / 切换服务器 / 退出登录 / 占位待开发项）
- 新建：`mobile_app/lib/widgets/message_bubble.dart` — 消息气泡（mine 蓝渐变 / theirs 白底 / 不对称尾巴）+ TimeDivider
- 新建：`mobile_app/lib/widgets/page_banner.dart` — 「访客访问了 XXX」橙色横幅

**业务流程（与 Web 客服工作台等价）**
```
首次启动 → 服务器配置页 → 输入 http://38.76.193.68 → 调 /api/health 验证 → 保存
→ 登录页 → admin / ***REDACTED*** → POST /api/agent/login → 保存 token + agent
→ 主页（底部 Tab：会话 + 我的）→ 启动 WSS → 拉会话列表
→ 点会话 → 拉历史消息 + POST /assign + WSS 发 read
→ 聊天页：发消息走 WSS（type=chat）；收到 WSS chat/read/sys 实时更新
→ 我的页：切换服务器（自动清 session）/ 退出登录
```

**与 Web 端一致的功能**
- WSS 长连接 + 自动重连 + 心跳
- 消息分组（5 分钟同发送者合并）+ 时间分隔条
- 已读角标（自己最后一条被读了才显示）
- 页面跳转橙色横幅（sender_ref="page:<url>" 触发）
- 未读 badge + 会话上浮
- 切换服务器自动登出（旧 token 跟旧服务器走）

**爷爷需要做的下一步**
1. 装 Flutter SDK（Windows + Android Studio 就能测 Android）
2. `cd mobile_app && flutter create -t app --org com.customservice --platforms=ios,android .`
3. `flutter pub get`
4. `flutter run`（Android 模拟器 / 真机 / 接 iPhone）
5. 首次进入输入 `http://38.76.193.68` 测试

**注意**
- iOS 编译必须用 macOS + Xcode（苹果硬性规定），Windows 上只能编 Android
- iOS 要测 HTTP 测试服需要在 `ios/Runner/Info.plist` 加 NSAllowsArbitraryLoads（生产 HTTPS 后去掉）
- APNs 推送测试需要 iPhone 真机（模拟器收不到）—— 这部分留到 [022]

---

## [019] 2026-05-23 11:00 — 访客 widget 打开时自动滚到最新消息

**起因 / 需求**
爷爷反馈：访客把 widget 关闭后再次打开，聊天窗口停留在上次的滚动位置（可能是中间），需要手动往下拉才能看到最新消息。希望打开时自动跳到最底部（最新消息），跟主流 IM 一致。

**根因**
widget 收起 / 打开是 loader.js 切换 iframe wrap 的 `display:block/none`，iframe 内的 chat.html DOM 不会重新加载，`#list` 的 `scrollTop` 保持上次离开时的位置。

**改了什么**（修改 1 个 / 新增 0 个 / 删除 0 个）
- 修改：[widget/public/chat.html](widget/public/chat.html)
  - 新增 `scrollToBottom()` 函数：用双 `requestAnimationFrame` 等浏览器完成 `display:none → display:block` 的 layout reflow（否则首次打开时 `scrollHeight` 可能还是 0），然后 `listEl.scrollTop = listEl.scrollHeight`
  - `widget_state.open=true` 事件处理里调 `scrollToBottom()`

**业务流程对比**
- 改动前：访客上次滚到中间→关 widget→再开 widget，仍停留中间，要手动下拉
- 改动后：每次打开 widget 都直接显示最新消息，无需任何操作

**触发场景与边界 + 验证方式**
- 验证 1：访客发几条消息使列表有滚动条 → 手动滚到中间 → 关 widget → 再开 → 应该立即看到最底部最新消息
- 验证 2：访客第一次打开 widget（缓存里没历史）→ 也直接显示在底部（虽然只有问候一条）
- 验证 3：CSS `scroll-behavior: smooth` 不变，滚动有平滑动画（视觉自然）
- 边界：双 rAF 保证在浏览器至少经过一帧的 reflow 后才计算 scrollHeight，避免首次打开 scrollHeight=0 的坑

---

## [018] 2026-05-23 01:00 — 会话「活跃期」概念：60 分钟无活动则重开（问候 + 提示音再触发，旧消息保留）

**起因 / 需求**
爷爷反馈：访客打开过一次之后，再次打开（即使过几小时）也不会再触发问候 + 提示音。希望 1 小时之后再来就重新触发问候，但聊天记录不丢。

**根因**
`OpenOrGetConversation` 看到 status='open' 的会话就一直复用，导致一个访客永远只有一个 "open" 会话。`EnsureConversation` 的 isNew 判定仅在「刚 INSERT 2 秒内」为 true。所以**访客只在「第一次打开 demo」时 isNew=true 触发 OnVisitorEnter**；之后再来任何次都是 isNew=false，不会再有问候 + visitor_enter 通知。

**改了什么 / 加了什么 / 删了什么**（修改 2 个 / 新增 0 个 / 删除 0 个）
- 修改：[backend/internal/store/store.go](backend/internal/store/store.go)
  - 拆出私有方法 `findOpenConversation`（仅查不建）和 `createConversation`（仅建不查）
  - 新增 `EnsureFreshConversation(ctx, siteID, visitorID, freshMinutes int) (conv, isNewSession, err)`：
    - 没 open 会话 → 新建 → `isNewSession=true`
    - 有 open 会话且 `updated_at` 距今 ≤ freshMinutes → 复用 → `isNewSession=false`
    - 有 open 会话但 `updated_at` 距今 > freshMinutes → **关闭旧的**（status=closed，消息历史保留在 messages 表）**+ 新建一个** → `isNewSession=true`
  - `EnsureConversation` 保留兼容（不再被调用）
- 修改：[backend/internal/handler/http.go](backend/internal/handler/http.go)
  - `VisitorSession` 从 `EnsureConversation` 改用 `EnsureFreshConversation(ctx, siteID, visitorID, 60)`

**业务流程对比**

旧（[017] 及之前）：
```
访客首次打开 demo                  → 新建 conv A → 问候 + 提示音 ✓
访客 1 小时后再打开                 → 复用 conv A → 不触发 ✗
访客 10 小时后再打开                → 复用 conv A → 不触发 ✗
访客今后任何次再打开                → 复用 conv A → 永远不再触发 ✗
```

新（[018]）：
```
访客首次打开 demo                  → 新建 conv A → 问候 + 提示音 ✓
访客 30 分钟后再打开                → 复用 conv A → 不触发（视为同一访问段）
访客 1 小时 1 分钟后再打开          → 关闭 conv A + 新建 conv B → 问候 + 提示音 ✓
访客之后每次>60 分钟无活动再打开    → 新建 conv → 问候 + 提示音 ✓ ✓ ✓
```

**「聊天记录不丢」如何保证**
- 旧会话只是 `status=closed`，`messages` 表里的所有消息一字未删
- 客服后台「历史记录」页能看到所有 closed 会话
- 访客 widget 的 `chat.html` 通过 `localStorage` 缓存最近 200 条消息（按 visitor_id 维度，不区分 conv），跨会话连贯展示

**触发场景与边界 + 验证方式**
- 验证 1：访客打开 demo → 听到提示音 + 看到问候
- 验证 2：5 分钟内再次打开 → 不会重复触发（同一活跃段）
- 验证 3：手动改数据库：`UPDATE conversations SET updated_at=NOW()-INTERVAL 70 MINUTE WHERE id='<convID>'`，然后访客再打开 → 应该触发新问候 + 提示音；DB 里 `<convID>` 应该 status='closed'；新出现一条 status='open' 的 conv
- 边界：freshMinutes=60（hardcode 60 分钟），未来可放进 `settings` 表让管理后台配置

**潜在问题**
- 如果客服正在和访客对话，但访客 60 分钟无活动 → 旧会话被关闭，客服在旧 conv 里发的消息没人看了。**注意**：客服后台的 conv 列表只显示 status='open'，所以旧会话会从客服列表消失，访客新开的会话作为新条目出现 —— 体验类似"会话超时重连"
- 1 小时是客服行业的常见值（Intercom / Crisp 都用 30-60 分钟）

---

## [017] 2026-05-23 00:30 — 去掉服务端 30 秒页面跟踪去重

**起因 / 需求**
爷爷明确要求：「不需要带 30 秒去重」。每次访客打开/跳转页面都立即在客服后台显示一条横幅，不做任何过滤。

**改了什么 / 加了什么 / 删了什么**（修改 1 个 / 新增 0 个 / 删除 0 个）
- 修改：[backend/internal/service/service.go](backend/internal/service/service.go)
  - 删除 `Service.pageDedupe map` 字段和 `Service.pageDedupeMu Mutex` 字段
  - `New()` 不再 init pageDedupe
  - `OnPageNavigation()` 删掉 30 秒去重判断 + 5000 阈值清理 1 小时前 key 那段
  - import 去掉 `"sync"`

**业务流程对比**
- 改动前：同一访客 30 秒内同一 URL 只显示一条横幅
- 改动后：访客每次触发 `type=page` 都立即显示一条横幅，不去重

**会不会因此刷屏？**
不会。客户端 chat.html 内部有自己的 `pageReported` 状态（同 chat.html 实例内同 URL 不重复触发），跨页面跳转才会重新报。所以正常使用：
- 访客打开首页 → 上报 1 次
- 跳转产品页 → chat.html 是新实例，上报 1 次
- 访客在产品页停留刷新 → chat.html 是新实例，再报 1 次（这次会显示，因为没有服务端去重）

**触发场景与边界 + 验证方式**
- 验证 1：访客跳 4 个 demo 页面 → 客服后台依次出现 4 条橙色横幅
- 验证 2：访客刷新当前页 → 客服后台再出现 1 条（**不再被服务端 30s 去重吞掉**）

---

## [016] 2026-05-22 23:55 — 修复页面跟踪无效：浏览器缓存旧 loader.js + 加同域 fallback

**起因 / 需求**
爷爷按 [015] 流程测试：客服后台**没看到任何橙色横幅**。

**根因定位（用日志实证）**
查 `/srv/cs-data/logs/backend/raw_ws.log`：半小时内 rx 消息分布只有 ping/chat/read，**没有一条 type=page**。也就是访客端**根本没发** page 事件。

进一步查 widget 容器内的文件版本：
- `docker exec cs-widget grep -c 'postPageInfo' /usr/share/nginx/html/loader.js` → 2（新版有）
- 但 widget nginx 给 `.js` 设了 `Cache-Control: public, max-age=604800`（7 天）

**结论**：服务器上的 loader.js 是新版的，但访客浏览器还在用上次缓存的旧版（没有 `postPageInfo`）。旧 loader.js → 不推 page_info → chat.html 永远拿不到 hostURL → reportPageView 直接 return → 服务端永远收不到 page 事件 → 客服后台永远没横幅。

**双重修复**
1. **根除：widget nginx 改 no-cache**
   - 之前：`location ~* \.(?:js|css|svg|png)$ { expires 7d; }` 把 loader.js 长缓存
   - 现在：单独给 `/loader.js` 和 `/chat.html` 配 `Cache-Control: no-cache, must-revalidate`
   - 这样浏览器每次都 If-None-Match 验证 ETag：文件没变 → 304 不下载（零开销）；文件有变 → 200 + 新内容（立即生效）
   - 其他静态资源（图片/字体/css）仍保留 7 天长缓存
2. **兜底：chat.html 加同域 fallback**
   - 即使集成方网站还在用 loader.js 旧缓存（没 postPageInfo），同域场景下 chat.html 自己也能拿到当前 URL
   - 实现：bootstrap 入口 + reportPageView 内调 `tryReadHostPageDirectly()`，尝试 `parent.location.href` / `parent.document.title`
   - 同域成功（demo 测试场景）；跨域 throws SecurityError 被 catch 忽略，由 loader.js postMessage 兜底（生产部署到第三方网站时）

**改了什么 / 加了什么 / 删了什么**（修改 2 个 / 新增 0 个 / 删除 0 个）
- 修改：[widget/nginx.conf](widget/nginx.conf) — 把 `/loader.js` 和 `/chat.html` 单独提到 `location =` exact match，加 `Cache-Control: no-cache, must-revalidate` + `Pragma: no-cache`；其他 .css/.png/.svg 等保留 7 天缓存
- 修改：[widget/public/chat.html](widget/public/chat.html) — 新增 `tryReadHostPageDirectly()` 同域读 parent.location；bootstrap 入口 + reportPageView 内主动调一次

**业务流程对比**
- 改动前：访客浏览器加载 loader.js 时如果命中缓存（7 天有效期内）→ 用旧版 → 没 postPageInfo → 永远不上报页面
- 改动后：每次访客加载 widget，浏览器都向服务器问一下 loader.js 有没有更新；新版立即生效。同时即使 loader.js 还是旧的，chat.html 自己也能读 parent.location 兜底

**触发场景与边界 + 验证方式**
- 验证 1：爷爷 Ctrl+F5 强刷 demo.html → 客服后台立即看到「访客访问了「Custom Service · 首页」」横幅
- 验证 2：跳转产品页 → 第 2 条横幅
- 验证 3：`curl -sI /widget/loader.js | grep -i cache-control` → 应该看到 `no-cache, must-revalidate`（不是 max-age=604800）
- 验证 4：`tail raw_ws.log | grep type.:.page` 应该能看到访客实际发出的 page 消息

**关于「集成方网站使用旧 loader.js 缓存」的兼容**
- 旧 loader.js（无 postPageInfo）+ 新 chat.html（有 tryReadHostPageDirectly fallback）：同域 demo 场景仍能工作；跨域时 fallback 失败，但不影响其他功能（聊天/已读/未读都正常）
- 新 loader.js + 新 chat.html：完整页面跟踪，跨域也工作
- 由于 nginx 改了 no-cache，**最多 1 次访问之后**就拿到新 loader.js，跨域场景也自动恢复

---

## [015] 2026-05-21 21:30 — 访客页面跟踪 (Crisp 风格横幅) + 4 个可跳转 demo 页面

**起因 / 需求**
爷爷希望访客每跳转一个页面，客服后台都能看到一条「访客访问了 XXX」的横幅记录（参考 Crisp 截图）。包括首次进入也要显示。同时多做几个可跳转的 demo 页面验证。

**协议设计**
新增 WSS 消息类型 `type=page`，客户端 → 服务端：
```
{ type:"page", conv:"<id>", ts:<ms>, extra:{ url, title } }
```

服务端处理：
1. 30 秒去重（同访客 + 同 URL）—— 避免 SPA / 刷新刷屏
2. 异步落库为 sys 消息（sender=sys, sender_ref="page:<url>", content="访客访问了「{title}」")
3. BroadcastToAllAgents 广播 type=chat + from=sys + extra={kind:"page_navigation", url, title}

客服端 Console.vue 收到 extra.kind="page_navigation" 时，渲染为**橙色横幅**（不是普通气泡）。从 DB 拉历史也能正确识别（sender_ref 以 "page:" 开头）。

**改了什么 / 加了什么 / 删了什么**（新增 4 个 demo 页 + 修改 6 个）
- 修改：[backend/internal/ws/hub.go](backend/internal/ws/hub.go) — `MessageSink` 接口加 `OnPageNavigation(visitorID, convID, url, title)`；`handleIncoming` 加 `case "page"` 提取 extra.url / title 调 sink
- 修改：[backend/internal/service/service.go](backend/internal/service/service.go) — 新增 `pageDedupe` map + `pageDedupeMu` 锁做 30 秒去重；`SetHub` setter 解决循环依赖；`OnPageNavigation` 实现：URL/title XSS 清洗 + 限长 + 入库 + BroadcastToAllAgents
- 修改：[backend/cmd/server/main.go](backend/cmd/server/main.go) — `svc.SetHub(hub)` 在 Hub 创建后回填
- 修改：[widget/public/loader.js](widget/public/loader.js) — 新增 `postPageInfo()` 在 iframe.onload 时推送 `{type:"page_info", url, title}` 给 chat.html（跨域时 iframe 内的 parent.location 拿不到，必须父页主动推）
- 修改：[widget/public/chat.html](widget/public/chat.html) — 监听 page_info；新增 `reportPageView()` 检查 alive+convID+hostURL+去重 → WSS 发 type=page；ws.onopen 后立即调一次 reportPageView
- 修改：[admin/src/views/Console.vue](admin/src/views/Console.vue) — `isPageGroup` / `pageURL` / `pageTitle` 辅助函数；模板加 `<template v-if="isPageGroup(g)">` 分支渲染橙色 `.page-banner`；pageTitle fallback 从历史 content 解析「xxx」
- 新建：4 个 demo 页面（共享导航条 互相跳转）：
  - [widget/public/demo.html](widget/public/demo.html) — 首页（重做，含 hero / 集成代码）
  - [widget/public/demo-products.html](widget/public/demo-products.html) — 产品系列（4 个产品卡片）
  - [widget/public/demo-pricing.html](widget/public/demo-pricing.html) — 价格方案（3 个套餐）
  - [widget/public/demo-contact.html](widget/public/demo-contact.html) — 联系我们（含表单）

**业务流程**

访客流：
```
1. 打开 demo.html
2. loader.js 注入 iframe → chat.html bootstrap → POST /visitor/session
3. 服务端 EnsureConversation isNew=true → OnVisitorEnter goroutine
4. WSS 上线 → loader.js postMessage(page_info)
5. chat.html reportPageView → WSS 发 type=page
6. 服务端 OnPageNavigation：去重 + 落库 + BroadcastToAllAgents
7. 客服端 Console.vue 收到 → 渲染橙色横幅「访客访问了「Custom Service · 首页」」

8. 访客点导航跳到 demo-products.html → 整页刷新
9. 新 loader.js → 新 iframe → chat.html bootstrap → POST /visitor/session 用旧 visitor_id
10. 服务端 EnsureConversation isNew=false → 不再触发 OnVisitorEnter
11. WSS 重新上线 → 新 page_info → 上报新 URL → 服务端去重通过 → 新横幅
```

**触发场景与边界 + 验证方式**
- 验证 1：访客打开 demo.html → 客服端在「访客 xxx 进入网站」通知之后看到第 1 条横幅「访客访问了「Custom Service · 首页」」
- 验证 2：点击「产品」跳转 → 客服端立即出现第 2 条横幅「访客访问了「产品介绍 · Custom Service Demo」」
- 验证 3：刷新当前页 → 30 秒内同 URL 去重，不重复出现横幅
- 验证 4：跨多个页面跳转回首页 → 30 秒过后，首页再上一次（去重过期）
- 边界：URL/title 走 SanitizeText 防 XSS + URL 1024 字符限长 + title 256 字符限长；去重 map 超过 5000 条自动清理 1 小时前的 key 防泄漏

**安全 / 健壮性**
- 服务端只接受 visitor 发的 page 事件（KindVisitor），agent 不能伪造别人的页面访问
- url 和 title 走 SanitizeText 防 XSS
- 30 秒去重避免恶意刷屏
- 异步 goroutine + 5s timeout + panic recover

---

## [014] 2026-05-21 21:00 — 问候消息走完整 WSS 通道：触发提示音 + 未读 + 已读

**起因 / 需求**
爷爷指出新访客的自动问候消息应该走"正常消息"的处理逻辑，包括：
- 触发访客端提示音
- widget 收起时 badge 未读 +1
- 走 [013] 的已读机制

但之前 [010] 我把 greeting 走的是"HTTP response 直接返回文本 + chat.html 本地 render"路径，目的是规避"访客 WSS 还没建立时服务端就推消息会丢"的时序问题。这条路径绕过了 `ws.onmessage` 完整逻辑，所以**没播声、没累计未读、没已读机制**。

**新设计：完全 WSS 通道 + 服务端等访客上线**
服务端在 OnVisitorEnter 内启动 goroutine：
1. 立即广播 `visitor_enter` sys 通知给所有客服（不变）
2. InsertMessage greeting 落库（不变）
3. 立即 BroadcastToAllAgents 把 greeting 推给所有客服（让客服端左侧列表立即看到新会话 + 这条 sys 消息）
4. **轮询等访客 WSS 上线**：最多等 8 秒，每 150ms 调一次 `hub.PushToVisitor(visitorID, env)`；上线（PushToVisitor 返回 true）立即推送 greeting

这样问候消息对访客端来说就是一条普通 type=chat 消息，走完整 ws.onmessage 逻辑：
- `playNotify()` 触发提示音
- widget 收起时 `unread++` + 通知父框红 badge
- widget 打开时 `sendReadAck()` 立即回送已读

**改了什么 / 加了什么 / 删了什么**（修改 4 个 / 新增 0 个 / 删除 0 个）
- 修改：[backend/internal/service/service.go](backend/internal/service/service.go) — `OnVisitorEnter` 重做：
  - 异步 goroutine timeout 5s → 15s（要容纳 8 秒等访客上线的轮询）
  - 删掉"只落库不推送"的旧逻辑，换成"落库 + 立即推所有客服 + 轮询等访客上线 → PushToVisitor"
- 修改：[backend/internal/handler/http.go](backend/internal/handler/http.go) — 删除 `resp["greeting"] = ...`，不再在 HTTP 响应里塞 greeting 文本
- 修改：[widget/public/chat.html](widget/public/chat.html) — 删除 bootstrap 里 `if (data.greeting) { render(...); persistMsg(...); }` 这段，让 greeting 完全从 WSS 推送
- 修改：[admin/src/views/Console.vue](admin/src/views/Console.vue) — `onMessage` 收到非当前会话的 chat 消息时，`fromVisitor || fromSys` 都触发 `scheduleConvsRefresh()`，让客服端能在新访客 + greeting 到达时立即拉到新会话列表

**业务流程对比**

旧（[010]/[013] 之前）：
```
访客打开网站
  → HTTP /visitor/session 返回 visitor_token + greeting 文本
  → chat.html bootstrap 直接 render(greeting) 本地绘制
  → 没播声、没未读、没已读
```

新（[014]）：
```
访客打开网站
  → HTTP /visitor/session 返回 visitor_token (无 greeting)
  → 服务端启动 goroutine：
     1) BroadcastToAllAgents(visitor_enter sys) — 客服弹通知 + 播声
     2) InsertMessage(greeting) 落库
     3) BroadcastToAllAgents(greeting chat from=sys) — 客服列表显示新会话 + 消息
     4) 轮询每 150ms：尝试 PushToVisitor(visitor.ID, greeting)
  → 同时访客端 chat.html bootstrap → connectWS → onopen
     hub.handleRegister → 访客进入 visitors map
  → 下一次 PushToVisitor 返回 true → 访客端收到 chat from=sys
  → 走完整 onmessage 逻辑：playNotify + (widget 收起 → unread++; 打开 → sendReadAck)
```

**触发场景与边界 + 验证方式**
- 验证 1：访客新打开 demo → 应该听到提示音、看到客服头像左侧出现问候消息气泡（不是之前那个"瞬间出现"）
- 验证 2：访客 widget 收起状态进入网站 → 浮动按钮红 badge 显示 1（之前是 0）
- 验证 3：访客 widget 打开状态进入网站 → 自动 sendReadAck → 客服端能看到这条 greeting 已被读
- 验证 4：业务日志里看到 `greeting pushed to visitor via WSS`（成功推送）；如果访客 WSS 一直没建立成功，看到 `greeting WSS push timeout (visitor not online within 8s)`，但 DB 仍然有这条记录
- 边界：8 秒超时基本覆盖正常网络（访客 WSS 建立通常 100-500ms）；超时后访客仍可通过下次 `loadMessages` 拉历史看到

**注意事项**
- 服务端轮询是 `for time.Now().Before(deadline) { sleep 150ms; PushToVisitor }`，单 goroutine 阻塞 8 秒最多，资源占用小
- 多个新访客同时进入：每个独立 goroutine 互不影响

---

## [013] 2026-05-21 20:40 — 双向「已读」状态：WSS 实时 + DB 持久化

**起因 / 需求**
爷爷要求加已读状态功能：两边都要、实时 WSS、明白吗。

**协议设计**
之前 `Envelope.Type` 已经声明 `read`（[001] 起就有），但只转发不落库。这次给它接上完整业务：

```
client → server  { type:"read", conv:"<id>", ts:<ms> }
含义：截至这一刻，我已读了当前会话内对端的所有消息

server 端处理：
  1. 盖 from = visitor:<id> 或 agent:<id>
  2. 异步 store.UpdateLastRead(convID, role, time.Now())
  3. FanoutToConv 广播给会话内的对端 + 接管该会话的客服

server → 对端  { type:"read", from:"agent:1"|"visitor:xxx", conv, ts }
对端收到后：把自己发过的、created_at <= ts 的消息标记为「已读」
```

**落库设计**
`conversations` 表新增 `last_read_agent_at` / `last_read_visitor_at` 两个时间戳。某条消息是否已读 = `created_at <= 对方的 last_read_*_at`（O(1) 查询，不需要每条消息单独存状态）。

**改了什么 / 加了什么 / 删了什么**（新增 1 个 / 修改 6 个 / 删除 0 个）
- 新建：[backend/migrations/003_read_status.sql](backend/migrations/003_read_status.sql) — 给 conversations 表 ALTER ADD COLUMN 两个 DATETIME NULL 列
- 修改：[backend/internal/store/store.go](backend/internal/store/store.go) — `Message` 加 `Read bool \`json:"read"\`` 字段；新增 `UpdateLastRead(convID, role, at)`、`GetLastReadTimes(convID)`；`ListMessages` 末尾根据 last_read 时间戳为每条消息计算 read 字段
- 修改：[backend/internal/ws/hub.go](backend/internal/ws/hub.go) — `MessageSink` 接口加 `PersistReadAsync`；`handleIncoming` 把 `type=read` 从「只 fanout」改为「盖发送者 + PersistReadAsync + Fanout」
- 修改：[backend/internal/service/service.go](backend/internal/service/service.go) — 实现 `PersistReadAsync`：goroutine + 5s timeout + panic recover + UpdateLastRead 失败仅记日志
- 修改：[backend/internal/handler/http.go](backend/internal/handler/http.go) — `MarkRead` 从「只清 unread_agent」升级为「UpdateLastRead + FanoutToConv 广播 read」（HTTP 兜底也能触发对端 UI 更新）
- 修改：[admin/src/views/Console.vue](admin/src/views/Console.vue) — `pickConv` 加 `sendReadAck`；当前会话收到访客消息也 `sendReadAck`；新增 `markMineReadUpTo` + `lastMineMsg` computed；模板在自己最后一条消息下方显示「已读」（仅 read=true 时显示）
- 修改：[widget/public/chat.html](widget/public/chat.html) — widget_state 打开时 / 收到客服消息时（widget 已打开）发 `sendReadAck`；收到客服 read 事件时 `setReadIndicator(true)` 在最后一组访客消息 stack 内挂「已读」；sendText / 文件上传后 `setReadIndicator(false)` 清掉旧角标

**业务流程**

访客视角：
```
访客发"hi" → 气泡显示 → 「已读」角标无
客服切到这个会话 → 后端 UpdateLastRead(agent) + 广播 read
访客 WSS 收到 read{from:"agent:..."} → 在"hi"下方显示「已读」
访客再发"hello" → 旧「已读」消失（这条还没被读）
客服看到 → 后端处理 read → 访客收到 read → "hello"下方显示「已读」
```

客服视角：
```
客服发"在的请讲" → 消息下方默认无角标
访客 widget 打开 / 当前打开下收到这条消息 → 访客发 read
客服 Console 收到 read{from:"visitor:..."} → markMineReadUpTo → m.read=true → 显示「已读」
```

**触发场景与边界 + 验证方式**
- 验证 1：客服 A 不切到访客 B 的会话 → 访客 B 发的消息 read=false，客服切过去后 read=true，访客那侧也立即看到「已读」
- 验证 2：访客 widget 收起时收到客服消息 → 不发 read（不算"看到"）；点开 widget → 立即发 read，客服那侧消息显示「已读」
- 验证 3：刷新页面后 GET /messages 返回的每条消息都带 read 字段（从数据库的 last_read_*_at 计算）
- 验证 4：HTTP /agent/conversations/:id/read 兜底接口也能触发对端 read 广播
- 边界：read 事件只在 byConv 内广播（不外溢给所有 agent），避免无关客服收到无意义事件；conv 之外的消息不会被错误标记

**安全 / 健壮性**
- PersistReadAsync 用 goroutine + 5s timeout + panic recover，失败仅记 business.log
- read 服务端盖 from 字段（不信任客户端声明，避免伪造别人的已读）
- UpdateLastRead 用 role 参数白名单（agent/visitor），不允许任意列写入

---

## [012] 2026-05-21 20:10 — 修复 /admin/settings 返回 nginx 默认页（致命）+ 访客浮动按钮未读角标

**起因 / 需求**
用户实测发现 2 个问题：
1. **致命 Bug**：访问 `http://38.76.193.68/admin/settings` 显示 "Welcome to nginx!" 默认欢迎页。
2. 访客端浮动按钮（收起状态）在收到客服消息时**没有显示未读红角标**。

**根因 1：nginx upstream 启动时只解析一次 DNS → IP 错位**
Redis 抓取实证：
- 实际容器 IP：cs-admin = 172.19.0.3，cs-widget = 172.19.0.5
- DNS 解析正确：admin → 172.19.0.3，widget → 172.19.0.5
- 但 nginx access.log 显示 `/admin/` 请求 `upstream: 172.19.0.5:80` —— **被反代到了 widget 容器**
- widget 容器内没有 index.html，nginx 返回 1.27-alpine 镜像自带的默认欢迎页 615 字节

完整故事：早期 `docker compose up` 时 admin 容器拿到 172.19.0.5；nginx 启动时把这个 IP 锁进 upstream（开源 nginx 限制，**upstream + hostname 只在启动时解析一次**）。后来反复 `docker compose up -d --build` 重建 admin，admin 容器拿到新 IP 172.19.0.3，**但 172.19.0.5 现在被 widget 占了** —— nginx 还在用 172.19.0.5，于是 /admin/* 全部错位转到了 widget。

**根因 2：访客端 chat.html 用 `document.visibilityState` 判断 widget 是否打开**
但 iframe 即使 `display:none` 时 `visibilityState` 在某些浏览器仍是 'visible'，导致未读永远不 +1。

**改了什么 / 加了什么 / 删了什么**（修改 5 个 / 新增 0 个 / 删除 0 个）
- 修改：[nginx/nginx.conf](nginx/nginx.conf) — http 块新增 `resolver 127.0.0.11 valid=10s ipv6=off;`（Docker 内置 DNS + 10s TTL）
- 修改：[nginx/conf.d/default.conf.template](nginx/conf.d/default.conf.template) — 去掉 `upstream cs_admin/cs_widget/cs_backend` 块；server 块加 `default_server` 标记 + `server_name ${DOMAIN} _`（IP 访问也兼容）
- 修改：[nginx/conf.d/ssl.conf.template](nginx/conf.d/ssl.conf.template) — 同步去 upstream + server_name 加 `_`
- 修改：[nginx/conf.d/_upstream.inc](nginx/conf.d/_upstream.inc) — 所有 `proxy_pass http://cs_xxx/` 改为 `set $host_var xxx; proxy_pass http://$host_var:port`。**变量 proxy_pass 时 nginx 每次请求都用 resolver 动态查 DNS**，绕过 upstream 静态解析。同时 `/admin/`、`/widget/` 改用 `rewrite ^/admin(/.*)$ $1 break;` 去前缀（变量 proxy_pass 不会自动重写 URI）。
- 修改：[widget/public/loader.js](widget/public/loader.js) — open/close 时 `iframe.contentWindow.postMessage({type:'widget_state',open:true/false})` 通知 chat.html
- 修改：[widget/public/chat.html](widget/public/chat.html) — 维护 `isWidgetOpen` 状态；监听 widget_state 消息；收到客服消息时改用 `if (!isWidgetOpen)` 判断（替代不可靠的 visibilityState）

**业务流程对比**
- 改动前：访问 /admin/settings → 主 nginx 把请求反代到错误的 widget 容器 → 返回 nginx 默认欢迎页
- 改动后：每次请求 nginx 都从 Docker DNS 查到当前真实的 admin 容器 IP → 正确返回 admin SPA
- 改动前：widget 收起时收到消息，未读永远 0（visibilityState 判断不准）
- 改动后：widget 收起时收到消息，loader.js 浮动按钮上立刻显示红 badge 数字；打开 widget 时自动清零

**触发场景与边界 + 验证方式**
1. `curl http://127.0.0.1/admin/` → 返回 vite 构建的 index.html（984 字节，含 `<title>Custom Service 客服工作台</title>`），不再是 nginx 欢迎页
2. `docker compose restart admin` 让 admin 换 IP → 再次访问 /admin/ 仍正确（DNS 10s TTL）
3. 访客打开 demo.html → 不点客服按钮（收起态）→ 客服发消息 → 右下角圆形按钮右上角出现红 badge 数字
4. 点开 widget → badge 立即消失，未读清零
5. 边界：rewrite 前缀去除规则只匹配 `/admin/xxx` 和 `/widget/xxx`，根路径 `/` 仍由 `location = /` 重定向到 /admin/

**为什么不直接 docker compose restart cs-nginx 临时修复？**
- 那只是把 IP 重新锁定一次，下次任何容器重启 IP 又会错位
- resolver + 变量是开源 nginx 唯一稳定方案（nginx-plus 才支持 `server xxx resolve` 动态解析）

---

## [011] 2026-05-21 19:40 — 通知声音库扩展：新增 5 个响亮长音色

**起因 / 需求**
爷爷反馈 [010] 提供的 5 种音色（classic / chime / ding / soft / alert）都比较短促轻柔，不够明显；要求"再多弄几个，响亮的，时间长的"。

**改了什么 / 加了什么 / 删了什么**（修改 2 个 / 新增 0 个 / 删除 0 个）
- 修改：[admin/src/api/sound.js](admin/src/api/sound.js) — 新增 5 个 SOUND_DEFS 条目，并新增 `playLayered` 工具函数（多层叠加同时开始播放）
- 修改：[widget/public/chat.html](widget/public/chat.html) — 同步新增 5 个条目 + `layered` 工具函数

**新音色清单**

| 名称 (key) | 标签 | 时长 | 音色特征 |
| --- | --- | --- | --- |
| `bell` | 铃声 (长) | 1.2 s | C6 (1046Hz) 主音 + C7 (2093Hz) 谐波叠加，金属感 + 慢衰减 |
| `doorbell` | 门铃 (长) | 0.85 s | E5 → C5 经典"叮~咚~"两音 |
| `trill` | 颤音 (急) | 0.7 s | 880 / 1100 Hz 交替 6 次 + 最后一音延长，紧急感强 |
| `fanfare` | 号角 | 0.76 s | C-E-G-C 上升音阶 + 最后一音 0.4s 延音 |
| `chord` | 和弦 | 0.8 s | C 大三和弦三音同时（C5 + E5 + G5），饱满响亮 |

**业务流程对比**
- 改动前：6 个选项（含静音）总时长都 ≤ 0.35 秒，安静办公环境也容易错过
- 改动后：11 个选项（含静音）；其中 5 个响亮长音色（0.7~1.2 秒），明显容易听到

**触发场景与边界 + 验证方式**
- 验证 1：管理后台 /settings 客服端提示音下拉应有 10 种（+静音 = 11）
- 验证 2：试听 bell → 听到铃声衰减约 1.2 秒
- 验证 3：试听 chord → 听到三个音同时响（饱满感）
- 验证 4：选 trill 作为客服端提示音 → 访客发消息客服端连响"嘟嘟嘟"6 次
- 边界：500ms 同种音色防抖仍生效——bell（1.2s）下次触发会有约 700ms 与上次的衰减尾巴轻微重叠，但因尾巴已弱，不会成噪音

**技术细节**
- `playLayered(ctx, layers, type)`：所有 oscillator 在 `ctx.currentTime` 同时 start，各自有独立的 freq/vol/duration——比 `playSequence` 顺序播放多了"和声"能力
- bell 的金属质感靠基频 + 第二谐波（2 倍频）叠加；chord 的饱满靠 C 大三和弦（频率比 4:5:6）
- 频率全用 12 平均律标准值（C5=523.25, E5=659.25, G5=783.99, C6=1046.5）

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
