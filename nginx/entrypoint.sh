#!/bin/sh
# 把 PUBLIC_DOMAIN 注入到 server_name；同时根据有无证书自动选择 80/443
set -e

DOMAIN="${PUBLIC_DOMAIN:-cs.example.com}"
export DOMAIN

# 用 envsubst 把模板中的 ${DOMAIN} 替换掉
envsubst '${DOMAIN}' < /etc/nginx/conf.d/default.conf.template > /etc/nginx/conf.d/default.conf

# 如果证书存在，启用 HTTPS server；否则继续用 HTTP（生产强烈建议加证书）
if [ -f "/etc/nginx/ssl/${DOMAIN}.pem" ] && [ -f "/etc/nginx/ssl/${DOMAIN}.key" ]; then
  envsubst '${DOMAIN}' < /etc/nginx/conf.d/ssl.conf.template > /etc/nginx/conf.d/ssl.conf
  echo "[entrypoint] HTTPS enabled for ${DOMAIN}"
else
  echo "[entrypoint] WARNING: no SSL cert at /etc/nginx/ssl/${DOMAIN}.{pem,key}; running HTTP only"
fi

exec "$@"
