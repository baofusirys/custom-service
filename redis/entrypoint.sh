#!/bin/sh
set -e
# 把 REDIS_PASSWORD 注入到运行时配置（不写进镜像 layer）
if [ -z "$REDIS_PASSWORD" ]; then
  echo "[FATAL] REDIS_PASSWORD must be set" >&2
  exit 1
fi
exec redis-server /usr/local/etc/redis/redis.conf --requirepass "$REDIS_PASSWORD"
