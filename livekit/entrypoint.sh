#!/bin/sh
# cs-livekit 入口脚本（[119] 多人大厅媒体服务）
#
# 职责：
#   1. 校验 LIVEKIT_API_KEY / LIVEKIT_API_SECRET / NODE_IP，任一缺失或格式不对立即拒绝启动（FATAL，退出码 78）；
#   2. 由 LIVEKIT_API_KEY / LIVEKIT_API_SECRET 组装 LIVEKIT_KEYS（livekit-server 原生读取，只走环境变量）；
#      webhook 签名用的 api_key 走 LIVEKIT_WEBHOOK_API_KEY（v1.13.10 由配置键 webhook.api_key 自动生成）；
#   3. exec 启动 livekit-server：成为 PID 1，直接收到 docker stop 的 SIGTERM，优雅退出。
#
# 两个密钥变量都未设置：按契约「未配置 LIVEKIT_API_KEY 时大厅整体关闭」，后端同样关闭大厅，
# 本容器进入待机（不监听任何端口、不重启刷屏），健康检查视为健康。
#
# 安全：绝不打印任何密钥值（不开 set -x；日志只写变量名）。启动日志为 JSON Lines，北京时间带 +08:00。
set -eu

LIVEKIT_BIN=/livekit-server
CONFIG_FILE=/etc/livekit/livekit.yaml

# ---------- 日志（JSON Lines 写 stderr，消息均为固定文本，不拼接任何外部输入） ----------
now_ts() {
  raw=$(date '+%Y-%m-%dT%H:%M:%S|%N|%z')
  base=${raw%%|*}
  rest=${raw#*|}
  nano=${rest%%|*}
  zone=${rest#*|}
  frac=""
  case "$nano" in
    [0-9][0-9][0-9]*) frac=".$(printf '%s' "$nano" | cut -c1-3)" ;;
  esac
  # +0800 → +08:00
  printf '%s%s%s:%s' "$base" "$frac" "${zone%??}" "${zone#"${zone%??}"}"
}

log() {
  # $1=级别 $2=事件 $3=消息 [$4=附加 JSON 片段，只允许脚本内部构造的安全值]
  extra=${4:-}
  printf '{"ts":"%s","level":"%s","service":"cs-livekit","module":"entrypoint","environment":"prod","event":"%s","message":"%s"%s}\n' \
    "$(now_ts)" "$1" "$2" "$3" "$extra" >&2
}

fatal() {
  log FATAL "$1" "$2"
  exit 78
}

# ---------- 校验工具 ----------
# 字符串中不同字符的个数（拦截 aaaa… / 1234… 这类低熵占位值；纯 POSIX，不经管道传递密钥）
distinct_chars() {
  s=$1
  seen=""
  n=0
  while [ -n "$s" ]; do
    c=${s%"${s#?}"}
    s=${s#?}
    case "$seen" in
      *"$c"*) ;;
      *) seen="$seen$c"; n=$((n + 1)) ;;
    esac
  done
  printf '%s' "$n"
}

# NODE_IP 校验：返回 0=合法公网 IPv4；2=合法但属于私网/CGNAT（告警放行）；1=不合法
check_node_ip() {
  ip=$1
  case "$ip" in
    '' | *[!0-9.]* | .* | *. | *..*) return 1 ;;
  esac
  old_ifs=$IFS
  IFS=.
  # shellcheck disable=SC2086
  set -- $ip
  IFS=$old_ifs
  [ "$#" -eq 4 ] || return 1
  for o in "$@"; do
    case "$o" in
      0) ;;
      0*) return 1 ;;
    esac
    [ "${#o}" -le 3 ] || return 1
    [ "$o" -le 255 ] || return 1
  done
  a=$1
  b=$2
  # 0.0.0.0/8、127/8 回环、169.254/16 链路本地、224+ 组播与保留：不可作为媒体地址
  if [ "$a" -eq 0 ] || [ "$a" -eq 127 ] || [ "$a" -ge 224 ]; then return 1; fi
  if [ "$a" -eq 169 ] && [ "$b" -eq 254 ]; then return 1; fi
  # 私网与运营商 NAT 段：通常说明填成了内网 IP（腾讯云需填公网 IP），告警但不拦截（内网测试场景）
  if [ "$a" -eq 10 ]; then return 2; fi
  if [ "$a" -eq 172 ] && [ "$b" -ge 16 ] && [ "$b" -le 31 ]; then return 2; fi
  if [ "$a" -eq 192 ] && [ "$b" -eq 168 ]; then return 2; fi
  if [ "$a" -eq 100 ] && [ "$b" -ge 64 ] && [ "$b" -le 127 ]; then return 2; fi
  return 0
}

# ---------- 读取环境变量 ----------
api_key=${LIVEKIT_API_KEY:-}
api_secret=${LIVEKIT_API_SECRET:-}
node_ip=${NODE_IP:-}

if [ -z "$api_key" ] && [ -z "$api_secret" ]; then
  log WARN hall_media_standby "LIVEKIT_API_KEY 与 LIVEKIT_API_SECRET 均未设置：多人大厅关闭（后端同样关闭大厅），cs-livekit 待机，不监听任何端口"
  trap 'log INFO hall_media_standby_stop "收到停止信号，待机退出"; exit 0' TERM INT
  while :; do
    sleep 3600 &
    wait $! || true
  done
fi

# ---------- LIVEKIT_API_KEY ----------
[ -n "$api_key" ] || fatal config_invalid "LIVEKIT_API_KEY 未设置（已设置 LIVEKIT_API_SECRET 时必须同时设置）"
case "$api_key" in
  *[!A-Za-z0-9_-]*) fatal config_invalid "LIVEKIT_API_KEY 只允许字母、数字、下划线、中划线" ;;
esac
if [ "${#api_key}" -lt 3 ] || [ "${#api_key}" -gt 64 ]; then
  fatal config_invalid "LIVEKIT_API_KEY 长度必须在 3-64 之间"
fi
key_lc=$(printf '%s' "$api_key" | tr 'A-Z' 'a-z')
case "$key_lc" in
  devkey | apikey | api_key | key | key1 | key2 | test | demo | changeme | your-api-key | livekit)
    fatal config_invalid "LIVEKIT_API_KEY 不能使用示例值，请用 openssl rand 生成"
    ;;
esac

# ---------- LIVEKIT_API_SECRET ----------
[ -n "$api_secret" ] || fatal config_invalid "LIVEKIT_API_SECRET 未设置（已设置 LIVEKIT_API_KEY 时必须同时设置）"
if [ "${#api_secret}" -lt 32 ] || [ "${#api_secret}" -gt 256 ]; then
  fatal config_invalid "LIVEKIT_API_SECRET 长度必须在 32-256 之间（请用 openssl rand -hex 32 生成）"
fi
case "$api_secret" in
  *[!A-Za-z0-9+/=_.~-]*) fatal config_invalid "LIVEKIT_API_SECRET 只允许 hex / base64 / base64url 字符（请用 openssl rand -hex 32 生成）" ;;
esac
secret_lc=$(printf '%s' "$api_secret" | tr 'A-Z' 'a-z')
case "$secret_lc" in
  *changeme* | *change-me* | *change_me* | *example* | *your-secret* | *yoursecret* | *placeholder* | *password* | *secret-key* | *secretkey*)
    fatal config_invalid "LIVEKIT_API_SECRET 命中占位值黑名单，请用 openssl rand -hex 32 生成"
    ;;
esac
if [ "$(distinct_chars "$api_secret")" -lt 10 ]; then
  fatal config_invalid "LIVEKIT_API_SECRET 熵过低（不同字符少于 10 个），请用 openssl rand -hex 32 生成"
fi

# ---------- NODE_IP ----------
[ -n "$node_ip" ] || fatal config_invalid "NODE_IP 未设置（必须是服务器公网 IPv4，生产复用 TURN_EXTERNAL_IP）"
ip_rc=0
check_node_ip "$node_ip" || ip_rc=$?
case "$ip_rc" in
  0) ;;
  2) log WARN node_ip_private "NODE_IP 是私网或运营商 NAT 地址，公网客户端可能无法建立媒体连接；生产请填服务器公网 IP" ;;
  *) fatal config_invalid "NODE_IP 格式不对：必须是合法的公网 IPv4（不能是 0.0.0.0、回环、链路本地、组播地址）" ;;
esac

[ -r "$CONFIG_FILE" ] || fatal config_invalid "配置文件不可读：/etc/livekit/livekit.yaml"
[ -x "$LIVEKIT_BIN" ] || fatal config_invalid "找不到 livekit-server 可执行文件"

# ---------- 组装并启动 ----------
# 键和值都用 YAML 双引号（字符集已限制，不含引号与反斜杠），避免纯数字等值被解析成非字符串
LIVEKIT_KEYS="\"$api_key\": \"$api_secret\""
LIVEKIT_WEBHOOK_API_KEY=$api_key
NODE_IP=$node_ip
export LIVEKIT_KEYS LIVEKIT_WEBHOOK_API_KEY NODE_IP
# 原始密钥变量不再传给 livekit-server；LIVEKIT_CONFIG 一律忽略，只用镜像内配置文件
unset LIVEKIT_API_SECRET LIVEKIT_CONFIG api_secret secret_lc

log INFO livekit_starting "配置校验通过，启动 livekit-server" ",\"node_ip\":\"$node_ip\",\"config\":\"/etc/livekit/livekit.yaml\",\"tcp_port\":7881,\"udp_port\":7882"
exec "$LIVEKIT_BIN" --config "$CONFIG_FILE" --node-ip "$node_ip" "$@"
