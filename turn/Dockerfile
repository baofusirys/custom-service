# ============================================================
# CoTURN: 客服系统的 TURN/STUN relay server
# 让 WebRTC 通话在严格 NAT / VPN / 防火墙后仍能 100% 接通。
#
# 端口（host 模式，无需在 compose ports 段重复声明）：
#   3478/udp + 3478/tcp  STUN + TURN 明文
#   5349/tcp             TURN over TLS（复用 maihaocs.icu Let's Encrypt 证书）
#   49152-49200/udp      relay 端口范围（够 ~50 路并发，企业级再调大）
#
# 鉴权：use-auth-secret + static-auth-secret（RFC 5389/draft-uberti-turn-rest 短期凭证）
#   - 后端 /api/turn-credential 用同一个 secret 生成 24h 时限 username/password
#   - 防止匿名滥用（任何人拿到密码就能用我们的服务器中转流量）
# ============================================================
FROM coturn/coturn:4.6

# 装 envsubst（gettext-base 提供）用来渲染配置模板
USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends gettext-base ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
 && echo "Asia/Shanghai" > /etc/timezone

# 配置模板 + entrypoint
COPY turnserver.conf.tmpl /etc/coturn/turnserver.conf.tmpl
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh \
 && mkdir -p /var/log/coturn

# 日志目录由 docker-compose bind 到 /srv/cs-data/logs/coturn，重启不丢
VOLUME ["/var/log/coturn"]

ENTRYPOINT ["/entrypoint.sh"]
