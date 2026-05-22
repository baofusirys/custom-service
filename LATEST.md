### 当前版本：v0.1.1 · 2026-05-21 16:42

> 本文件是 AI 接手项目时的「第一站」。看完这一份再去看 CHANGELOG，别凭印象答。

---

## 当前部署坐标（测试服）
- 服务器：[12] 零零壹测试服器 / 38.76.193.68:22 / root
- 远端代码目录：`/custom-service/`
- 远端数据目录：`/srv/cs-data/{logs,uploads,ssl}`（已 chown 100:101 给容器内 app 用户）
- 入口：
  - 管理后台 http://38.76.193.68/admin/
  - Widget 演示 http://38.76.193.68/widget/demo.html
  - 健康检查 http://38.76.193.68/api/health
- 超管账号：`admin` / `***REDACTED***`（仅测试服，本地 .env 内）
- 状态：6 容器 Up，3 个 healthy，业务通、安全通、日志通

---

## 一句话介绍
一套企业级、可嵌入任何网页的自托管在线客服系统。访客端是一段 JS（<script src> 引入即用，iframe 隔离，不污染宿主页样式），客服后台是 Vue 3 + Element Plus，后端是 Go + WebSocket，单机即可承载万级并发长连接。

## 最新代码在哪个目录
- 绝对路径：`e:\mycode\custom_service\`
- 服务器上：`/srv/custom-service/`（即整个仓库目录，部署时 rsync/sftp 全量同步到这里）

## 过期 / 归档目录
- 暂无（v0.1.0 是首版）

## 关键开关位置
| 用途 | 文件 | 位置 |
| --- | --- | --- |
| 服务总配置（端口/JWT/DB/Redis） | `.env`（部署时基于 `.env.example` 生成） | 根目录 |
| 全局时区 | `backend/internal/config/config.go` | `LoadTimezone()` |
| WSS 心跳/读写超时 | `backend/internal/ws/hub.go` | 文件顶部常量 |
| 限流参数（按 IP / 按访客） | `backend/internal/security/ratelimit.go` | 文件顶部常量 |
| 文件上传大小上限 | `backend/internal/config/config.go` | `MaxUploadSize` |
| 数据库自动迁移开关 | `backend/internal/db/migrate.go` | 启动时强制执行，无开关 |
| Widget 默认主题色 | `widget/src/config.ts` | `defaultTheme` |
| Nginx 限流 / 防 DDoS | `nginx/conf.d/default.conf` | `limit_req_zone` / `limit_conn_zone` 段 |

## 部署坐标
- 部署方式：把整个仓库目录 rsync/sftp 上传到服务器后，进入目录执行 **`docker compose up -d --build`**，一条命令完成。
- 默认开放端口：
  - `80/443` → Nginx 入口（HTTP 自动 301 跳 HTTPS，WSS 走 443）
  - 其余服务一律不对外，仅在 docker 内网通信
- 数据卷（**严禁动**）：
  - `cs_mysql_data`（named volume，MySQL 数据）
  - `cs_redis_data`（named volume，Redis AOF）
- 宿主机绑定目录（**仓库目录外，不会被部署清空**）：
  - `/srv/cs-data/logs/`（所有模块日志，长效存储）
  - `/srv/cs-data/uploads/`（访客/客服上传的图片、文件）
- 管理后台入口：`https://<your-domain>/admin/`
- 默认超管账号：首次启动从 `.env` 的 `ADMIN_BOOTSTRAP_USERNAME` / `ADMIN_BOOTSTRAP_PASSWORD` 创建；首次登录后必须改密。

## 集成方（别人嵌入自己网站）怎么用
一行代码搞定，详见 `docs/INTEGRATION.md`：
```html
<script src="https://<your-domain>/widget/loader.js"
        data-cs-endpoint="wss://<your-domain>/ws"
        data-cs-site="default" defer></script>
```

## 最近 3 次重大改动摘要
- **[001] 2026-05-21**：项目首版。建立 Go 后端 / Vue Admin / Widget / Nginx 四模块的骨架；MySQL + Redis 持久化；WSS 全双工通信；日志按天滚动落盘；启动自动迁移；Docker 一键部署。

## AI 接手必读顺序
1. 本文件（LATEST.md）
2. `CHANGELOG.md` 最近 5~10 条
3. 用户问到的模块的 `README.md`（每个子模块都有）
4. 真正动到的代码
