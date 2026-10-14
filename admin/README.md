# admin — 客服工作台 + 管理后台

## 一句话
Vue 3 + Element Plus（原生样式，无自定义皮肤）实现的客服工作台和管理后台，提供登录、在线会话、历史记录、客服管理四个页面。

## 调用关系
- **被调用**：Nginx 把 `/admin/` 路径反代到本镜像的 Nginx 上（端口 80，仅 docker 内网）。
- **调用**：通过同源 `/api/*`（HTTP）和 `/ws/agent`（WSS）调用 backend。

## 关键开关 / 配置
| 项 | 文件 | 位置 |
| --- | --- | --- |
| 路由 base = `/admin/` | `src/router/index.js` | createWebHistory 第一参 |
| 接口 base | `src/api/http.js` | `baseURL` |
| WSS 客户端（自动重连、心跳） | `src/api/ws.js` | `AgentWS` 类 |
| 消息状态机 / 幂等合并 / 密码学 ID | `src/modules/messageState.js` | 纯函数统一实现 |
| 中文 + 北京时间 | `src/main.js` | `dayjs.tz.setDefault('Asia/Shanghai')` |
| 0.8.0 新功能纯逻辑（标签/话术/撤回/输入/下载/回收站校验） | `src/modules/*.js` | 契约 `_internal/reports/api-0.8.0-features.md`，均有 `tests/*.test.mjs` |
| 0.8.0 新功能接口封装 | `src/api/visitors.js` 等 | 下载走 `src/api/download.js`（blob + `rawResponse`） |
| 撤回时限 / 增量同步回退窗口 | `src/modules/recall.js` | `RECALL_WINDOW_MS` / `RECALL_SYNC_LOOKBACK_MS` |
| typing 节流与显示超时 | `src/modules/typing.js` | `TYPING_SEND_INTERVAL_MS` / `TYPING_DISPLAY_TIMEOUT_MS` |

## 已知坑
- 严禁加自定义 CSS（爷爷铁律：全 Element Plus 原生样式）。本目录已无 `.css` 文件。
- 直接 `npm run build` 生成的 dist 会被 Dockerfile COPY 进 nginx:alpine。CI/CD 不依赖本地 node_modules。

## 上次重大改动
- 2026-10-14 [113] 0.8.0：会话标签筛选/多选批量删除导出、置顶/未读/拉黑、客户资料、快捷回复、正在输入、消息撤回、全局搜索；新增快捷回复/黑名单/回收站页面，设置页自动清理与删除全部。
- 2026-08-22 [098] 修复访客维度缓存引用失联；新增持久 outbox 与逐条 `已发送/已送达/已读` 单调状态。
