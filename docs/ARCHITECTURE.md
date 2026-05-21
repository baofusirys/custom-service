# 架构总览

## 整体拓扑

```
                                ┌──────────────────────────────┐
                                │      公网 (用户浏览器)        │
                                │  · 嵌入 widget 的第三方网站   │
                                │  · 客服自己访问 /admin/       │
                                └────────────┬─────────────────┘
                                             │ HTTPS / WSS
                                  ┌──────────▼──────────┐
                                  │     nginx 容器       │
                                  │ · SSL 终结           │
                                  │ · 限流 / 防 DDoS     │
                                  │ · 反代 + WSS upgrade │
                                  └──┬─────┬─────┬─────┬┘
                                     │     │     │     │
                          /admin/    │     │     │     │   /widget/
                  ┌──────────────────▼┐   │   ┌─▼──────────────┐
                  │   admin 容器       │   │   │  widget 容器    │
                  │ Vue3 + Element     │   │   │  纯静态 HTML/JS │
                  │ Plus 客服工作台    │   │   │  iframe 内 UI   │
                  └────────────────────┘   │   └────────────────┘
                                           │
                              /api/ + /ws/ │ + /files/
                                  ┌────────▼────────┐
                                  │  backend (Go)   │  ← 业务核心
                                  │  Gin + WSS Hub  │
                                  └──────┬──────┬───┘
                                         │      │
                                ┌────────▼───┐  │
                                │  mysql 8.0 │  │
                                └────────────┘  │
                                                │
                                        ┌───────▼─────┐
                                        │  redis 7.2  │
                                        └─────────────┘
```

## WSS 消息流转

### 访客 → 客服

```
1. 访客打开嵌入 widget 的页面
2. widget loader.js 注入 iframe -> chat.html
3. chat.html POST /api/visitor/session 拿到 visitor_id / conv_id / visitor_token
4. chat.html 建立 wss:// /ws/visitor?token=<visitor_token>
5. 服务端 Hub 把该连接注册到 byConv[conv_id]
6. 访客发送 chat 消息
7. Hub 收到 -> service.OnVisitorMessage(限流 + 清洗 + 入库)
8. Hub.FanoutToConv(conv) -> 同会话内所有客户端实时收到（包括分配到该会话的客服）
9. 同步 Redis PUBLISH 到 cs:bcast，多节点部署时其他节点也广播
```

### 客服 → 访客

```
1. 客服浏览器登录 /admin/login 拿到 agent_token
2. /admin/console 打开后建立 wss:// /ws/agent?token=<agent_token>
3. 客服点击某个会话 -> POST /api/agent/conversations/:id/assign
   服务端 Hub.AttachAgentToConv(agentID, convID) 把客服加入 byConv 索引
4. 客服发送 chat 消息
5. Hub -> service.OnAgentMessage -> 入库 + Fanout 到 byConv 全员
6. 访客 iframe 立即收到
```

## 消息优先级（爷爷需求：WSS 通道优先）

每个 `*ws.Client` 内有两个发送队列：
- `high` (容量 256)：所有实时聊天 / 通知，优先消费
- `low`  (容量 1024)：历史回放 / 系统批量

`writePump` 永远先榨干 `high`，最多 32 条后再轮询 `low`。这样在 `low` 有大量积压时，新到达的 WSS 实时消息绝对不会被排到队尾。

## 高并发模型

- WSS Hub 用单 goroutine 串行化注册 / 注销 / 入队（消除 map 锁竞争）。
- 每个连接独立 readPump + writePump，互不阻塞。
- 连接队列满（high 队列 = 256）会主动断连，保护 Hub 不被慢客户端拖死。
- Redis Pub/Sub 让多副本部署时跨容器消息互通（v0.1.0 单副本即可，未来横扩无需改业务代码）。
- MySQL 连接池 200 max / 50 idle，足够单机万人并发的非实时落库需求（聊天消息 ~ 100 写/秒级别）。

## 长效日志（4 路）

- `business.log` — 业务事件
- `security.log` — 安全告警（限流、注入嫌疑、IP 拉黑）
- `audit.log` — 管理操作审计
- `raw_ws.log` — 原始 WSS 报文（最原始，便于事后取证）

存放位置：宿主机 `/srv/cs-data/logs/backend/`（bind 在仓库外，重启 / 重新上传代码绝不丢）；按天 rotate + 单文件 200MB 分卷 + 365 天保留。

Nginx 日志同样 bind 到 `/srv/cs-data/logs/nginx/`。

## 时区一致性

所有环节强制北京时间（东八区）：
- 容器：`TZ=Asia/Shanghai`
- MySQL：`default-time-zone='+08:00'`（my.cnf）+ DSN `loc=Asia%2FShanghai` + 连接级 `SET time_zone='+08:00'`
- Go：`time.Local = LoadLocation("Asia/Shanghai")`
- 日志：`beijingTimeEncoder` 输出 `2006-01-02 15:04:05.000`
- 前端 (Vue Admin)：`dayjs.tz.setDefault('Asia/Shanghai')`
