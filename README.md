# Custom Service — 企业级自托管在线客服系统

[![Build Images](https://github.com/baofusirys/custom-service/actions/workflows/build-images.yml/badge.svg)](https://github.com/baofusirys/custom-service/actions/workflows/build-images.yml)
[![Release](https://img.shields.io/github/v/release/baofusirys/custom-service?label=release)](https://github.com/baofusirys/custom-service/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

> 一行 `<script>` 嵌入任意网页 · WSS 实时通信 · Go + Vue3 + Element Plus · Docker 一键部署

> **第一次部署？看 [INSTALL.md](INSTALL.md) 完整小白教程**（从买服务器到 widget 上线全流程，含 iPhone App build / 推送 / 数据迁移）

## 5 分钟自托管（推荐）

```bash
# 海外 / 港台服务器
bash <(curl -fsSL https://raw.githubusercontent.com/baofusirys/custom-service/main/install.sh)

# 🇨🇳 国内服务器（加 --cn 走南京大学 GHCR 反代，10x 快）
bash <(curl -fsSL https://raw.githubusercontent.com/baofusirys/custom-service/main/install.sh) --cn
```

自动装 Docker / 生成强密码 / 拉预编译镜像 / 启动。无需 git clone、无需本地编译。

镜像在 GitHub Container Registry，公开免授权拉取：`ghcr.io/baofusirys/cs-{backend,admin,widget,nginx,mysql,redis,coturn}:latest`

## 检查版本 / 升级

```bash
# 查你部署的版本
curl -s https://你的域名/api/version
# {"version":"0.2.0","name":"custom-service","repo":"..."}

# 查 upstream 最新版
curl -s https://raw.githubusercontent.com/baofusirys/custom-service/main/VERSION
# 0.2.0

# 升级到最新（不停机数据库，仅秒级重启容器）
cd /opt/custom-service && docker compose pull && docker compose up -d
```

锁定特定版本（生产推荐，避免 latest 突变）：把 `docker-compose.yml` 里 `:latest` 改成具体 tag 如 `:0.2.0`，全部 release tag 见 https://github.com/baofusirys/custom-service/releases

---

## 项目目标
- **可嵌入**：访客端是一段轻量 JS，引入即用，iframe 隔离不污染宿主页样式。
- **自托管**：每个用户/公司独立部署一份，数据完全在自己服务器上。
- **企业级**：高并发（单机万级 WSS 长连接）、消息秒达、长效日志、防 DDoS / SQL 注入 / XSS / 暴力请求、敏感字段加密。
- **一键部署**：仓库目录上传到服务器 → `docker compose up -d --build` 完事。

## 模块清单
| 目录 | 是什么 | 技术栈 |
| --- | --- | --- |
| `backend/` | API + WSS 服务，业务核心 | Go 1.22 · Gin · gorilla/websocket · MySQL · Redis · zap |
| `admin/` | 客服工作台 + 管理后台 | Vue 3 · Vite · Element Plus（原生样式） · Pinia |
| `widget/` | 嵌入访客端的聊天气泡 | TypeScript · Vite · iframe 隔离 |
| `nginx/` | 反代 / SSL / 限流 / 静态资源 | Nginx 1.27 |
| `docs/` | 集成 / 部署 / 安全 / 架构文档 | Markdown |

## 30 秒上手
1. 准备一台 Linux 服务器（含 Docker + Docker Compose 插件）。
2. 把本仓库目录整个上传到 `/srv/custom-service/`。
3. 复制 `.env.example` 为 `.env`，至少修改：`PUBLIC_DOMAIN`、`MYSQL_ROOT_PASSWORD`、`JWT_SECRET`、`ADMIN_BOOTSTRAP_PASSWORD`。
4. 进入目录执行：
   ```bash
   docker compose up -d --build
   ```
5. 浏览器访问 `https://<你的域名>/admin/`，用 `.env` 里的 bootstrap 账号登录。
6. 给目标网站塞一行（见 `docs/INTEGRATION.md`）：
   ```html
   <script src="https://<你的域名>/widget/loader.js"
           data-cs-endpoint="wss://<你的域名>/ws"
           data-cs-site="default" defer></script>
   ```

## 文档索引
- **`INSTALL.md`** — 小白也能看懂的完整安装指南（**新手从这开始**）
- `LATEST.md` — 当前版本最新坐标 / 入口 / 关键开关（AI 接手必读）
- `CHANGELOG.md` — 全部变更日志
- `docs/INTEGRATION.md` — 集成方怎么把 widget 装到自己网站
- `docs/DEPLOY.md` — 部署 / 备份 / 升级（精简版，给熟悉 Docker 的人）
- `docs/ARCHITECTURE.md` — 架构总览 / 消息流转 / 长连接管理
- `docs/SECURITY.md` — 安全机制 + 自验证脚本
- 各子模块根目录的 `README.md`

## 许可
[MIT License](LICENSE) — 免费用、改、商用，保留版权声明即可。
