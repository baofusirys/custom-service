# 自托管安装指南（小白也能看懂）

> 这一份是手把手教程：**从你买一台空服务器，到访客在你网站上能用客服 widget 跟客服对话**，全过程。
> 如果你已经熟悉 Docker，可以直接看 [docs/DEPLOY.md](docs/DEPLOY.md) 的精简版。

---

## 0. 这玩意是干啥的？

一套**自己托管在自己服务器上**的在线客服系统：
- **访客端**：一行 `<script>` 嵌到你自己的网站任意一页，右下角立刻出现"在线客服"按钮。访客点开 → 跟你的客服实时聊天。
- **客服端**：网页后台（Vue + Element Plus）+ iPhone App，客服回消息、看访客信息、接语音电话。
- **后端**：Go + WebSocket + MySQL + Redis，全部容器化。
- **可选**：CoTURN（WebRTC 中继，让语音通话在严格 NAT / VPN 下也能通）。

**跟商用 SaaS 客服的区别**：所有数据在你自己服务器，不发给第三方。

---

## 1. 你需要先准备什么

### 1.1 一台 Linux 服务器
- **最小配置**：1C2G 内存 + 20G 硬盘 + 3Mbps 带宽。够 500 同时在线访客。
- **推荐配置**：2C4G + 50G + 5Mbps。够 5000 同时在线 + 语音通话。
- **系统**：Ubuntu 22.04 / Debian 12 / CentOS Stream 9 都行（任何能跑 Docker 的）。
- **公网 IP**：必须有（VPS 默认都有）。
- **谁家买**：阿里云 / 腾讯云 / DigitalOcean / Vultr / Linode 任一。月费 ~$5-10。

### 1.2 一个域名（强烈推荐）
- 没有域名也能跑（直接用 IP 访问），但**没法启用 HTTPS**（很多浏览器现在不让 HTTP 用麦克风 → 语音通话用不了）。
- 域名买完后做一步：**A 记录把域名指向你服务器公网 IP**。
- 域名哪买：阿里云 / Cloudflare / Namesilo 任一，一年几十块。

### 1.3 在你本地装 Git
Windows: <https://git-scm.com/download/win>  Mac: `brew install git`  Linux: `sudo apt install git`

---

## 2. 服务器侧：从空机器到能用

### 2.1 SSH 登录服务器
```bash
ssh root@<你的服务器 IP>
```

### 2.2 装 Docker 和 Docker Compose
```bash
curl -fsSL https://get.docker.com | sh
# 验证
docker --version          # 应输出 Docker version 25+
docker compose version    # 应输出 v2+
```

### 2.3 开放端口（关键，常被忽略！）
**阿里云 / 腾讯云**：到控制台 → 安全组 → 入方向规则，放行：
- **80**（HTTP，acme.sh 申请证书要用）
- **443**（HTTPS）
- **3478** TCP+UDP（CoTURN，语音通话；不用语音可以省）
- **5349** TCP（CoTURN over TLS；不用语音可以省）
- **49152-49200** UDP（CoTURN 媒体中继端口范围；不用语音可以省）

**服务器自己防火墙**（如果开了 ufw）：
```bash
ufw allow 80,443/tcp
ufw allow 3478
ufw allow 5349/tcp
ufw allow 49152:49200/udp
```

### 2.4 创建数据目录（必须在代码仓库**外**）
```bash
mkdir -p /srv/cs-data/{logs,uploads,ssl}
# 容器内 app 用户 uid:gid 是 100:101，让它们能写
chown -R 100:101 /srv/cs-data
```
> **为什么要在外面**：以后你重新 `git pull` 或全量上传代码时，仓库目录可能被覆盖，data 在外面就不会丢。**铁律**：除了日志、上传文件这种你想直接 ls 看的，所有数据库类数据都用 Docker named volume，不要 bind 到仓库目录。

### 2.5 把代码拉到服务器
```bash
cd /
git clone <你刚刚 push 到 GitHub 的仓库 URL> custom-service
cd custom-service
```

### 2.6 配置 `.env`（最关键的一步）
```bash
cp .env.example .env
# 然后用 vim 或 nano 改下面这几项必填：
vim .env
```

**必改的 8 项**：
| 项 | 说明 | 怎么填 |
|---|---|---|
| `PUBLIC_DOMAIN` | 你的域名 | `cs.yourcompany.com`（不带 https://） |
| `ACME_EMAIL` | 申请 Let's Encrypt 证书的邮箱 | 你自己的邮箱，证书到期通知用 |
| `MYSQL_ROOT_PASSWORD` | MySQL root 密码 | 随机 64 字符（见 .env 顶部的命令） |
| `MYSQL_PASSWORD` | 业务用户密码 | 同上换一个 |
| `REDIS_PASSWORD` | Redis 密码 | 同上换一个 |
| `JWT_SECRET` | JWT 签名密钥 | 同上换一个 |
| `DATA_AES_KEY` | 数据加密密钥 | **必须正好 64 个 hex 字符** = 32 字节 AES-256 |
| `ADMIN_BOOTSTRAP_PASSWORD` | 首次创建的超管密码 | 你能记住的、登录后立刻改掉 |
| `TURN_EXTERNAL_IP` | 服务器公网 IP | 给 CoTURN 用；运行 `curl ifconfig.me` 拿到 |
| `TURN_REALM` | TURN realm | 通常等于 `PUBLIC_DOMAIN` |
| `TURN_STATIC_AUTH_SECRET` | TURN 凭证签名密钥 | 随机 64 字符 |

**生成随机密码命令**（Linux）：
```bash
openssl rand -hex 32
```
每次输出不同的 64 字符，复制粘贴进 `.env`。

### 2.7 启动！
```bash
docker compose up -d --build
```
第一次会下载 + 编译镜像，**大约 5-10 分钟**。之后看：
```bash
docker compose ps
```
应该看到 7 个容器，关键的 `cs-backend / cs-mysql / cs-redis` 都是 `healthy`。

### 2.8 看日志确认无错
```bash
docker compose logs backend --tail=50
tail -f /srv/cs-data/logs/backend/business.log
```
看到 `http_server_listen :8080` 就成了。

### 2.9 浏览器验证
- `https://<你的域名>/admin/` → 客服后台登录页（首次申请证书可能要 1-2 分钟）
- `https://<你的域名>/widget/demo.html` → 访客 demo 页
- `https://<你的域名>/api/health` → 应返回 `{"status":"ok"}`

用 `.env` 里的 `ADMIN_BOOTSTRAP_USERNAME` / `ADMIN_BOOTSTRAP_PASSWORD` 登录后台，**第一件事改密码**。

---

## 3. 把 widget 嵌到你自己的网站

打开你网站的任意一页 HTML，在 `</body>` 之前粘一行：

```html
<script src="https://<你的域名>/widget/loader.js"
        data-cs-endpoint="wss://<你的域名>"
        data-cs-site="default" defer></script>
```

刷新你的网站 → 右下角出现一个蓝色聊天泡泡 → 点开 → 客服后台立刻收到。

---

## 4. iPhone App 自己 build（可选）

App 让客服在手机上接消息、接电话、收推送。**需要 Mac + 付费 Apple Developer 账号**（年费 99 USD；免费 Apple ID 也能装，但 App 每 7 天会过期需重装）。

### 4.1 改 Bundle ID 和 Team
仓库里 `mobile_app/ios/Runner.xcodeproj/project.pbxproj` 是占位的：
- `PRODUCT_BUNDLE_IDENTIFIER = com.example.customservice` → 改成你自己的（如 `com.yourcompany.cs`）
- `DEVELOPMENT_TEAM = ""` → 改成你 Apple Developer 后台的 10 位 Team ID

用 vim 全局替换最快：
```bash
cd mobile_app/ios
sed -i.bak 's/com\.example\.customservice/com.yourcompany.cs/g' Runner.xcodeproj/project.pbxproj
sed -i.bak 's/DEVELOPMENT_TEAM = "";/DEVELOPMENT_TEAM = ABCD123456;/g' Runner.xcodeproj/project.pbxproj
```

### 4.2 在 Mac 上 build
```bash
cd mobile_app
flutter pub get
cd ios && pod install && cd ..
flutter build ios --release
```

### 4.3 装到 iPhone
USB 连 iPhone，开发者模式打开，然后：
```bash
flutter install --release -d <iPhone 设备 ID>
```
设备 ID 用 `flutter devices` 查。

### 4.4 App 内首次启动
- 输入"服务器地址" → 填 `https://<你的域名>`
- 用超管账号登录

---

## 5. 启用 iPhone 锁屏推送通知（可选，免费方案）

让访客发消息 / 来电话时，客服 iPhone 锁屏也能收到推送（即使 App 没开）。

**方案**：用 [luckfast 消息推送助手](https://message-push.com)（免费，App Store 搜"消息推送助手"）。

步骤：
1. 客服 iPhone 装"消息推送助手" App，注册账号，拿到 `User ID` + `User Key`
2. 后台登录 → 设置 → iPhone APNs 推送
3. 填入 User ID 和 User Key 保存
4. 测试：访客发条消息 → 客服 iPhone 锁屏应弹通知

---

## 6. 升级到新版本

```bash
cd /custom-service
git pull
docker compose up -d --build
```
完全无停机（除了 backend 容器重启的几秒）。数据库迁移自动跑。

---

## 7. 常见坑

### 7.1 `acme.sh` 一直申请不到证书
- 检查 80 端口是否真开放（`curl http://<你域名>` 应连得通，云厂商安全组别忘了）
- 检查域名 DNS 是不是真指到这台服务器（`dig +short <你域名>` 应输出服务器 IP）
- 看 nginx 容器日志：`docker compose logs nginx --tail=100`

### 7.2 语音通话"连接失败"
- 99% 是 CoTURN 端口没开放或 `TURN_EXTERNAL_IP` 没填对
- 测试 TURN：`docker compose logs coturn --tail=50` 看有没有 binding request
- 临时关 CoTURN：访客和客服在同 WiFi 下用 STUN 还能通话；跨网必须 TURN

### 7.3 忘了超管密码
后台数据库直接改：
```bash
docker compose exec mysql mysql -uroot -p
# 输入 MYSQL_ROOT_PASSWORD
USE custom_service;
-- 把密码改成 admin123（bcrypt 加密后）
UPDATE agents SET password_hash='$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy' WHERE username='admin';
EXIT;
```
然后用 `admin / admin123` 登录，立刻去后台改密。

### 7.4 我改了代码怎么部署？
- 改完 commit → push 到 GitHub
- SSH 到服务器 → `cd /custom-service && git pull && docker compose up -d --build`
- 一条命令搞定所有重建

### 7.5 数据丢了怎么办？
- MySQL 数据在 Docker named volume `cs_mysql_data`，**只要不 `docker volume rm`**，重启 / 重建容器都不会丢
- 日志在 `/srv/cs-data/logs/`
- 上传文件在 `/srv/cs-data/uploads/`
- 备份：`docker run --rm -v cs_mysql_data:/data -v /backup:/backup alpine tar czf /backup/mysql-$(date +%F).tgz /data`

---

## 8. 我要把数据迁移到新服务器

```bash
# 旧服务器
docker run --rm -v cs_mysql_data:/data -v /tmp:/out alpine tar czf /out/mysql.tgz /data
docker run --rm -v cs_redis_data:/data -v /tmp:/out alpine tar czf /out/redis.tgz /data
tar czf /tmp/cs-data.tgz /srv/cs-data
scp /tmp/*.tgz root@<新服务器>:/tmp/

# 新服务器
git clone <仓库> /custom-service
cd /custom-service && cp .env.example .env && vim .env   # 改成跟旧的一致
tar xzf /tmp/cs-data.tgz -C /
docker compose up -d --build mysql redis     # 先起 db
docker compose down
docker run --rm -v cs_mysql_data:/data -v /tmp:/in alpine tar xzf /in/mysql.tgz -C /
docker run --rm -v cs_redis_data:/data -v /tmp:/in alpine tar xzf /in/redis.tgz -C /
docker compose up -d --build                  # 全启
```

---

## 9. 出问题去哪问

- 看 [CHANGELOG.md](CHANGELOG.md)：项目演进历史，每条都带"起因 / 改了什么 / 验证方式"
- 看每个模块的 README：
  - [backend/README.md](backend/README.md)
  - [admin/README.md](admin/README.md)
  - [widget/README.md](widget/README.md)
  - [mobile_app/README.md](mobile_app/README.md)
  - [turn/README.md](turn/README.md)
- 看 [LATEST.md](LATEST.md)：最新版本 + 关键开关位置速查
- 提 issue：到 GitHub 仓库 Issues 区
