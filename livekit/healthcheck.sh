#!/bin/sh
# cs-livekit 健康检查（[119]）：容器内用 busybox wget 探测信令端口根路径，livekit 健康时返回 200 "OK"。
# 官方镜像（alpine）自带 busybox wget，无需额外安装 curl。
# 两个密钥都未设置时容器按设计待机（大厅关闭），视为健康，避免无意义的 unhealthy 告警。
if [ -z "${LIVEKIT_API_KEY:-}" ] && [ -z "${LIVEKIT_API_SECRET:-}" ]; then
  exit 0
fi
exec wget -q -T 4 -O /dev/null http://127.0.0.1:7880/
