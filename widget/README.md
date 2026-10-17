# widget — 可嵌入第三方网站的聊天小部件

## 一句话
纯 vanilla JS + HTML 静态文件，给任意网站塞一行 `<script>` 就能在右下角出现客服气泡，点击展开聊天 iframe（CSS / DOM 完全隔离不污染宿主页）。

## 调用关系
- **被调用**：第三方网站的 HTML 通过 `<script src="https://<your-domain>/widget/loader.js">` 引入。
- **调用**：通过 HTTP 调用 `/api/visitor/session`、`/api/upload`；通过 WSS 调用 `/ws/visitor`。

## 关键开关 / 配置
| 项 | 文件 | 位置 |
| --- | --- | --- |
| 默认主题色 | `public/loader.js` | `data-cs-theme-color` 标签属性 |
| 默认按钮文字 | `public/loader.js` | `data-cs-button-text` 标签属性 |
| iframe 内消息持久缓存（最近 200 条） | `public/chat.html` | `persistMsg()` |
| delivery/read 回执与重放去重 | `public/chat.html` | `sendDeliveryAck()` / `sendReadAck()` |
| 重连退避（最高 30s） | `public/chat.html` | `connectWS()` 中的 backoff |
| 聊天记录清空 / 撤回 / 正在输入 | `public/chat.js` | `onHistoryCleared()` / `applySessionHistoryState()` / `applyRecall()` / `maybeSendTyping()` |
| 已处理清空时间本地键 | `public/chat.js` | `HISTORY_RESET_KEY`（`cs_visitor_<site>_history_reset_at`） |
| 大厅开关（总开关 / 文字聊天 / 语音 / 视频）→ 界面映射 | `public/hall-core.js` | `normalizeSettings()` / `switchView()` / `switchToastText()` |
| 大厅开关运行时处理（关闭断开、重新打开错峰入厅、停止开麦） | `public/hall.js` | `applySettings()` / `disableHall()` / `reopenHall()` / `stopMicBySwitch()` |
| 公开设置缓存（大厅关闭时本页向服务端确认一次） | `public/chat.js` | `loadPublicSettings()` / `updatePublicHallSettings()` |

## 已知坑
- 嵌入第三方网站时，`endpoint` 必须用 `wss://`（生产）或 `ws://`（本地）；HTTP 接口自动从 endpoint 推断。
- 因为给任意第三方域名嵌入，X-Frame-Options 必须留空（已在 nginx.conf 中处理）。

## 上次重大改动
- 2026-10-18 [123] 客服可控的大厅开关（契约第 10 节）：总开关关闭时与上线前一致只剩私聊（断开 LiveKit、停止重连、宿主页胶囊隐藏），重新打开后不刷新恢复并错峰自动入厅；文字聊天关闭隐藏输入框与附件并提示「大厅文字聊天已暂停」；语音关闭隐藏开麦并立即停止发布、提示「大厅语音已暂停」；错误码 40377 / 40378。
- 2026-10-14 [113] 0.8.0：处理 history_cleared / history_reset_at（清空界面与本地缓存）、recall / recalled_ids（撤回占位）、typing（每 3 秒最多一次，客服输入提示 5 秒隐藏）；新增 vm 假环境行为测试 `tests/historyRecallTyping.test.mjs`。
- 2026-08-22 [098] 新增密码学稳定消息 ID、客服消息送达/已读回执、重连补发去重且不重复累加未读。
