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
| 重连退避（最高 30s） | `public/chat.html` | `connectWS()` 中的 backoff |

## 已知坑
- 嵌入第三方网站时，`endpoint` 必须用 `wss://`（生产）或 `ws://`（本地）；HTTP 接口自动从 endpoint 推断。
- 因为给任意第三方域名嵌入，X-Frame-Options 必须留空（已在 nginx.conf 中处理）。

## 上次重大改动
- 2026-05-21 [001] 首版。
