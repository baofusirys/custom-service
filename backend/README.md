# backend — Go API + WSS 服务

## 一句话
客服系统的业务核心：访客 / 客服 WSS 实时通信、消息持久化、文件上传、JWT 鉴权、按 IP / 访客的多层限流和拉黑、数据库自动迁移、4 路长效日志。

## 调用关系
- **被调用**：`nginx`（反代 `/api/*`、`/ws/*`、`/files/*`）；浏览器中的 `admin` Vue 工程和 `widget` 嵌入端通过 HTTP/WSS 调用。
- **调用**：`mysql`（业务数据持久化）、`redis`（在线状态 / 限流 / 跨节点 Pub-Sub）。

## 关键开关 / 配置
| 项 | 文件 | 位置 |
| --- | --- | --- |
| 全局时区（北京时间） | `internal/config/config.go` | `time.LoadLocation("Asia/Shanghai")` |
| MySQL DSN 强制 +08:00 + 关闭 interpolateParams（防 SQL 注入） | `internal/config/config.go` | `MySQLDSN()` |
| WSS 心跳 / 单连接出队队列长度 | `internal/ws/client.go` | 文件顶部常量 |
| 限流阈值 | 环境变量 `SECURITY_*`（见 `.env.example`） | — |
| 4 路日志保留 365 天 + 单文件 200MB rotate | `internal/logger/logger.go` | 文件顶部常量 |
| 数据库自动迁移 | `internal/db/migrate.go` | 启动时强制执行 |

## 已知坑 / 历史遗留
- 首版 v0.1.0。go.sum 文件不入库 —— 由 Dockerfile 构建时 `go mod download all` 现场生成（保证 go.mod 是唯一源，但本地直接 `go build` 时需先 `go mod tidy`）。

## 上次重大改动
- 2026-05-21 [001] 首版上线。
