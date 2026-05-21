# 部署指南

## 系统要求
- Linux 服务器（Ubuntu / Debian / CentOS / Rocky 等，64-bit）
- Docker 24+ 与 Docker Compose v2（`docker compose` 子命令）
- 公网 IP + 已解析的域名（用于 HTTPS / WSS）
- 至少 2 GB 内存 / 2 核 CPU（万人级再扩容到 4G / 4 核）
- 已开放 80 + 443 端口

## 一次性准备

```bash
# 1. 把仓库目录整个上传到服务器（/srv/custom-service/）
rsync -av --delete ./ user@server:/srv/custom-service/

# 2. 创建数据 / 日志 / 上传 / 证书目录（仓库外，避免被部署清空）
ssh user@server "sudo mkdir -p /srv/cs-data/{logs,uploads,ssl} && sudo chown -R $USER /srv/cs-data"

# 3. 把证书 PEM/KEY 放进去（命名必须 = PUBLIC_DOMAIN）
#    例：cs.example.com.pem / cs.example.com.key
scp cs.example.com.pem user@server:/srv/cs-data/ssl/
scp cs.example.com.key user@server:/srv/cs-data/ssl/
```

## 配置

```bash
cd /srv/custom-service
cp .env.example .env
nano .env  # 必改：PUBLIC_DOMAIN / 所有 password / JWT_SECRET / DATA_AES_KEY
```

DATA_AES_KEY 必须正好 64 个十六进制字符（=32 字节），可以这样生成：
```bash
openssl rand -hex 32
```

JWT_SECRET 同样建议：
```bash
openssl rand -hex 32
```

## 启动

```bash
docker compose up -d --build
```

第一次启动会做：
1. 构建 5 个镜像（mysql / redis / backend / admin / widget / nginx）
2. MySQL 自检 + 创建 cs_app 用户 + 等待 healthy
3. Backend 启动时自动 `CREATE DATABASE IF NOT EXISTS` + 执行 migrations/*.sql
4. 自动创建超管账号（来自 .env）

观察启动：
```bash
docker compose ps
docker compose logs -f backend
```

确认健康：
```bash
curl -k https://<your-domain>/api/health
# {"agents":0,"now":"2026-05-21 14:30:01","status":"ok","tz":"Asia/Shanghai","visitors":0}
```

## 后续更新

由于每次部署是「全量上传代码 → `docker compose up -d --build`」，工作流就是：

```bash
# 本地把改动 commit + push 到自己的 git
# 服务器
cd /srv/custom-service
git pull           # 或 rsync 全量覆盖
docker compose up -d --build   # 一条命令完成
```

数据库迁移会被 backend 启动时自动判断 + 自动执行；不需要手动跑任何 SQL 脚本。

## 备份

需要备份的（每天定时）：
- Docker named volume `cs_mysql_data`（MySQL 业务数据）
- `/srv/cs-data/uploads/`（用户文件）
- `/srv/cs-data/logs/`（合规审计需要）

示例（备份 MySQL volume 到本机文件）：
```bash
docker run --rm \
  -v cs_mysql_data:/data \
  -v /srv/cs-backup:/backup \
  alpine tar czf /backup/mysql-$(date +%F).tgz -C /data .
```

## 回滚

```bash
cd /srv/custom-service
git checkout <previous-tag>     # 或 rsync 旧版本
docker compose up -d --build
```

数据库 schema 是「只前进不回滚」，迁移文件不删；如果新版迁移加了字段不影响老代码，回滚后老 backend 依旧能工作。

## 卸载

```bash
docker compose down               # 停容器
# 真要彻底清，再执行：
docker volume rm cs_mysql_data cs_redis_data
sudo rm -rf /srv/cs-data /srv/custom-service
```

注意：`docker compose down` 不会动 named volume，数据安全。
