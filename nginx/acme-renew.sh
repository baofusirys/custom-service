#!/bin/bash
# 每天 cron 调用：检查证书是否需要续期。
# acme.sh --cron 是幂等的，证书有效期 > 30 天会跳过。
# 续期成功后会自动 reload nginx 让新证书生效。
#
# 注意：环境变量 PUBLIC_DOMAIN / ACME_EMAIL 由 entrypoint.sh 写入 /etc/crontabs/root，
# cron 子进程能继承。

set -e

DOMAIN="${PUBLIC_DOMAIN:-cs.example.com}"
ACME_HOME=/opt/acme.sh

echo "=========================================="
echo "[$(date '+%Y-%m-%d %H:%M:%S')] 开始检查 $DOMAIN 证书续期"
echo "=========================================="

# acme.sh --cron 模式：自己判断是否要续期
# --home 必须指定，避免它找默认的 ~/.acme.sh
if "$ACME_HOME/acme.sh" --home "$ACME_HOME" --cron 2>&1; then
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] cron 任务执行完成"
else
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] cron 任务返回非 0，可能没到续期时机（正常）"
fi

# 如果证书目录存在且 acme.sh 续期了文件，重新安装并 reload nginx
ACME_CERT_DIR="$ACME_HOME/${DOMAIN}_ecc"
if [ -d "$ACME_CERT_DIR" ] && [ -f "$ACME_CERT_DIR/${DOMAIN}.cer" ]; then
  # 比较 acme.sh 内的证书时间 vs nginx ssl 目录的，确认是否真的换了
  ACME_MTIME=$(stat -c %Y "$ACME_CERT_DIR/${DOMAIN}.cer" 2>/dev/null || echo 0)
  NGINX_MTIME=$(stat -c %Y "/etc/nginx/ssl/${DOMAIN}.pem" 2>/dev/null || echo 0)

  if [ "$ACME_MTIME" -gt "$NGINX_MTIME" ]; then
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] 检测到 acme.sh 已续期，重新安装到 nginx ssl 目录..."
    "$ACME_HOME/acme.sh" --home "$ACME_HOME" \
      --install-cert -d "$DOMAIN" --ecc \
      --key-file "/etc/nginx/ssl/${DOMAIN}.key" \
      --fullchain-file "/etc/nginx/ssl/${DOMAIN}.pem" \
      --reloadcmd "nginx -s reload"
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] 续期完成，nginx 已 reload"
  else
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] 证书未变更，无需 reload"
  fi
else
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] acme.sh 内还没有 $DOMAIN 的证书目录，可能首次还没申请成功"
fi

echo "[$(date '+%Y-%m-%d %H:%M:%S')] 续期检查结束"
