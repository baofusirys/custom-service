# [119] cs-livekit：多人大厅媒体服务（自托管 LiveKit SFU）
#
# 基础镜像固定 v1.13.10（禁止 latest / 省略标签；升级必须是单独的、有意识的提交）。
# 已用 curl 核对官方 https://raw.githubusercontent.com/livekit/livekit/v1.13.10/Dockerfile：
#   - 运行层 FROM alpine:3.24.2（busybox 提供 sh、wget），ENTRYPOINT ["/livekit-server"]，无 CMD；
#   - 未声明 USER，官方镜像默认以 root 运行 → 本镜像创建固定 UID/GID 10001 的专用用户并切换；
#     livekit 只监听 7880/7881/7882（均 >1024），不需要任何特权或 capability。
# 配置文件与脚本全部 COPY 进镜像（不挂载静态文件）；密钥只走环境变量，镜像内不含任何密钥。
FROM livekit/livekit-server:v1.13.10

ENV TZ=Asia/Shanghai

# tzdata：容器内北京时间（官方运行层不带时区数据）；专用非 root 用户 livekit（UID/GID 10001）
RUN apk add --no-cache tzdata \
 && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
 && echo "Asia/Shanghai" > /etc/timezone \
 && addgroup -S -g 10001 livekit \
 && adduser -S -D -H -u 10001 -G livekit -h /nonexistent -s /sbin/nologin livekit \
 && mkdir -p /etc/livekit

COPY livekit.yaml /etc/livekit/livekit.yaml
COPY entrypoint.sh /usr/local/bin/cs-livekit-entrypoint
COPY healthcheck.sh /usr/local/bin/cs-livekit-healthcheck

# 配置与脚本归 root 所有、运行用户只读/只执行（容器内进程无法篡改自身配置）；
# 去掉可能混入的 CRLF（Windows 检出时兜底，.gitattributes 已强制 LF）
RUN sed -i 's/\r$//' /etc/livekit/livekit.yaml /usr/local/bin/cs-livekit-entrypoint /usr/local/bin/cs-livekit-healthcheck \
 && chown root:root /etc/livekit/livekit.yaml /usr/local/bin/cs-livekit-entrypoint /usr/local/bin/cs-livekit-healthcheck \
 && chmod 0444 /etc/livekit/livekit.yaml \
 && chmod 0555 /usr/local/bin/cs-livekit-entrypoint /usr/local/bin/cs-livekit-healthcheck

USER 10001:10001

# 7880 信令（只在容器内网，由 cs-nginx 反代 /lk/rtc）；7881/tcp、7882/udp 媒体（对公网，需安全组放行）
EXPOSE 7880/tcp 7881/tcp 7882/udp

# 健康检查：busybox wget 探测 http://127.0.0.1:7880/（livekit 健康时返回 200 "OK"）；
# 两个密钥都未设置时容器按设计待机，视为健康（见 healthcheck.sh）。
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 CMD ["/usr/local/bin/cs-livekit-healthcheck"]

ENTRYPOINT ["/usr/local/bin/cs-livekit-entrypoint"]
