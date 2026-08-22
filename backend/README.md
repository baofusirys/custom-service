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
| 4 路日志永久压缩归档 + 单文件 200MB rotate | `internal/logger/logger.go` | 文件顶部常量 |
| 消息事务落库、ACK、回执与离线补发 | `internal/service/message_pipeline.go` | 有界异步流水线 |
| 数据库自动迁移 | `internal/db/migrate.go` | 启动时强制执行 |

## 已知坑 / 历史遗留
- 历史消息曾无条件写 `delivered_ws=1`；只有 `m2-*` 新协议消息才把该字段解释为真实送达，旧消息统一按已持久化展示。
- `go.mod`、`go.sum` 必须同时入库，Docker 构建使用锁定依赖，不允许现场漂移。

## 上次重大改动
- 2026-08-22 [098] 消息改为事务提交后 ACK，新增幂等冲突保护、送达/已读归属校验、离线补发和乱序读游标保护。
