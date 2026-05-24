#!/usr/bin/env bash
# ============================================================
# CoTURN 启动 entrypoint
#   1) 校验必填环境变量（缺一不可的话直接 fail-fast 别静默跑）
#   2) 把 cs_ssl_data 命名卷里的 maihaocs.icu.{pem,key} 软链到 /etc/coturn/certs/
#   3) envsubst 渲染 turnserver.conf
#   4) exec turnserver 作为 PID 1
# ============================================================
set -euo pipefail

# ---- 必填变量校验（fail-fast）----
: "${TURN_EXTERNAL_IP:?TURN_EXTERNAL_IP not set (服务器公网 IP，必填)}"
: "${TURN_REALM:?TURN_REALM not set (例如 maihaocs.icu)}"
: "${TURN_STATIC_AUTH_SECRET:?TURN_STATIC_AUTH_SECRET not set (与后端共享的 HMAC 密钥)}"

# ---- TLS 证书软链（cs_ssl_data 命名卷由 nginx acme.sh 写入）----
mkdir -p /etc/coturn/certs
CERT_SRC=/etc/coturn/ssl-src/${TURN_REALM}.pem
KEY_SRC=/etc/coturn/ssl-src/${TURN_REALM}.key
if [ -f "$CERT_SRC" ] && [ -f "$KEY_SRC" ]; then
    ln -sf "$CERT_SRC" /etc/coturn/certs/cert.pem
    ln -sf "$KEY_SRC"  /etc/coturn/certs/key.pem
    echo "[entrypoint] TLS certs linked: $CERT_SRC -> /etc/coturn/certs/cert.pem"
else
    echo "[entrypoint] WARN: TLS certs missing ($CERT_SRC). 5349/TLS will fail; 3478 仍可用"
    # 生成自签证书避免 turnserver 启动失败（5349 监听需要）
    openssl req -x509 -nodes -newkey rsa:2048 -days 30 \
        -keyout /etc/coturn/certs/key.pem \
        -out /etc/coturn/certs/cert.pem \
        -subj "/CN=${TURN_REALM}" 2>/dev/null
    echo "[entrypoint] 已生成 30 天临时自签证书占位（仅让 turnserver 启动；5349 真生产请用 LE 证书）"
fi

# ---- 渲染配置 ----
envsubst < /etc/coturn/turnserver.conf.tmpl > /etc/coturn/turnserver.conf
echo "[entrypoint] turnserver.conf 渲染完成，关键参数："
grep -E '^(listening-port|tls-listening-port|external-ip|realm|min-port|max-port)' /etc/coturn/turnserver.conf

# ---- 启动 ----
exec turnserver -c /etc/coturn/turnserver.conf
