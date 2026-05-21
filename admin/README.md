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
| 中文 + 北京时间 | `src/main.js` | `dayjs.tz.setDefault('Asia/Shanghai')` |

## 已知坑
- 严禁加自定义 CSS（爷爷铁律：全 Element Plus 原生样式）。本目录已无 `.css` 文件。
- 直接 `npm run build` 生成的 dist 会被 Dockerfile COPY 进 nginx:alpine。CI/CD 不依赖本地 node_modules。

## 上次重大改动
- 2026-05-21 [001] 首版上线。
