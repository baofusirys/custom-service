#!/bin/bash
# nginx 自动 HTTPS 启动脚本。
# 行为（按 ENABLE_HTTPS / ACME_EMAIL 决定）：
#   1) ENABLE_HTTPS=false 或 ACME_EMAIL 为空 → 只跑 HTTP（开发模式）
#   2) ENABLE_HTTPS=true 且 ACME_EMAIL 已设：
#      - 证书不存在 → 后台启 nginx → acme.sh webroot 申请 → 拷证书 → reload 切 HTTPS
#      - 证书存在但 < 30 天到期 → 续期
#      - 证书 > 30 天 → 直接启用 HTTPS
#   3) 启动后台 cron，每天 03:00 自动检查续期
#
# 设计原则：完全自动化，零干预，docker compose up -d --build 一条命令完成全套。

set -e

DOMAIN="${PUBLIC_DOMAIN:-cs.example.com}"
ENABLE_HTTPS="${ENABLE_HTTPS:-true}"
ACME_EMAIL="${ACME_EMAIL:-}"

export DOMAIN

ACME_HOME=/opt/acme.sh
CERT_DIR=/etc/nginx/ssl
CERT_FILE="$CERT_DIR/${DOMAIN}.pem"
KEY_FILE="$CERT_DIR/${DOMAIN}.key"

log() {
  echo "[entrypoint] $(date '+%Y-%m-%d %H:%M:%S') $@"
}

# ---- HTTP 配置永远渲染（哪怕只跑 HTTPS 也要 80 端口接 ACME challenge）
envsubst '${DOMAIN}' < /etc/nginx/conf.d/default.conf.template > /etc/nginx/conf.d/default.conf

HTTPS_READY=0

if [ "$ENABLE_HTTPS" != "true" ]; then
  log "ENABLE_HTTPS=$ENABLE_HTTPS，跳过证书申请，只跑 HTTP"
elif [ -z "$ACME_EMAIL" ]; then
  log "WARNING: ENABLE_HTTPS=true 但 ACME_EMAIL 没设置 → 无法申请证书，只跑 HTTP"
else
  # 判断是否要申请/续期
  NEED_ISSUE=0
  if [ ! -f "$CERT_FILE" ] || [ ! -f "$KEY_FILE" ]; then
    log "证书不存在 ($CERT_FILE)，将申请新证书"
    NEED_ISSUE=1
  elif ! openssl x509 -checkend $((30*86400)) -noout -in "$CERT_FILE" 2>/dev/null; then
    log "证书 $CERT_FILE 即将过期 (剩余 < 30 天)，将续期"
    NEED_ISSUE=1
  else
    EXPIRE=$(openssl x509 -enddate -noout -in "$CERT_FILE" 2>/dev/null | cut -d= -f2)
    log "证书 $CERT_FILE 仍有效，到期日: $EXPIRE，跳过申请"
  fi

  if [ "$NEED_ISSUE" = "1" ]; then
    # 后台启 nginx，让 80 端口接 ACME HTTP-01 challenge
    log "启动后台 nginx 接收 ACME challenge..."
    nginx -g 'daemon on;' || { log "ERROR: 后台 nginx 启动失败"; exit 1; }
    sleep 2

    # 注册账户（幂等的，多次跑无害）
    "$ACME_HOME/acme.sh" --home "$ACME_HOME" \
      --register-account -m "$ACME_EMAIL" || true

    log "调用 acme.sh 申请证书 for $DOMAIN (Let's Encrypt, EC 256, webroot)..."
    # --force：如果之前申请到一半失败，acme.sh 内部目录可能已存在，--force 让它强制重签
    # 不用担心配额 — 我们只在 NEED_ISSUE=1 时才走这里，正常情况下证书有效会跳过
    if "$ACME_HOME/acme.sh" --home "$ACME_HOME" \
      --issue --force -d "$DOMAIN" \
      --webroot /var/www/acme \
      --keylength ec-256 \
      --server letsencrypt; then

      log "证书申请成功，安装到 $CERT_FILE..."
      "$ACME_HOME/acme.sh" --home "$ACME_HOME" \
        --install-cert -d "$DOMAIN" --ecc \
        --key-file "$KEY_FILE" \
        --fullchain-file "$CERT_FILE" \
        --reloadcmd "echo '[install-cert] reload skip during bootstrap'"
      HTTPS_READY=1
    else
      log "ERROR: acme.sh 申请证书失败！可能原因："
      log "  - DNS 没有把 $DOMAIN 解析到本服务器（必须先配 DNS A 记录）"
      log "  - 80 端口被防火墙挡了（Let's Encrypt 走 HTTP-01 验证）"
      log "  - ACME_EMAIL ($ACME_EMAIL) 不是有效邮箱"
      log "  - Let's Encrypt 每周对同一域名最多签 5 张证书，可能撞配额了"
      log "将以 HTTP 模式继续运行；下次容器重启会自动重试申请"
    fi

    # 停掉后台 nginx，下面以前台模式重启
    nginx -s quit 2>/dev/null || true
    sleep 1
  else
    HTTPS_READY=1
  fi
fi

# 启用 HTTPS：渲染 ssl.conf + 把 default.conf 改成 80 全 301 跳 HTTPS
if [ "$HTTPS_READY" = "1" ] && [ -f "$CERT_FILE" ] && [ -f "$KEY_FILE" ]; then
  envsubst '${DOMAIN}' < /etc/nginx/conf.d/ssl.conf.template > /etc/nginx/conf.d/ssl.conf
  envsubst '${DOMAIN}' < /etc/nginx/conf.d/redirect.conf.template > /etc/nginx/conf.d/default.conf
  log "HTTPS 已启用 for $DOMAIN（HTTP 80 → 301 → HTTPS 443）"
fi

# ---- 启动 cron 守护进程，每天 03:00 检查续期
# 把环境变量塞进 crontab（cron 子进程默认不继承 docker env）
mkdir -p /etc/crontabs /var/log/nginx
cat > /etc/crontabs/root <<EOF
PUBLIC_DOMAIN=${DOMAIN}
ACME_EMAIL=${ACME_EMAIL}
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
0 3 * * * /usr/local/bin/acme-renew.sh >> /var/log/nginx/acme-renew.log 2>&1
EOF
crond -b -l 8 -L /var/log/nginx/cron.log
log "cron 已启动，每天 03:00 检查证书续期（< 30 天自动续期，否则跳过）"

log "启动 nginx (前台模式)..."
exec "$@"
