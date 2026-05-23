FROM nginx:1.27-alpine

ENV TZ=Asia/Shanghai

# 装 acme.sh 自动证书所需依赖：
#   bash      - acme.sh 必需的 shell（busybox sh 不够用）
#   curl      - acme.sh 下载/HTTP 验证
#   openssl   - 证书操作
#   gettext   - envsubst 注入 ${DOMAIN}
#   dcron     - alpine 轻量 cron 守护，用来跑续期定时任务
#   socat     - 仅在 acme.sh standalone 模式用，留着兜底
#   tzdata    - 北京时间
RUN apk add --no-cache tzdata gettext curl openssl socat bash dcron && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone && \
    # 装 acme.sh 到 /opt/acme.sh —— 直接从 GitHub tar.gz 装，绕开 get.acme.sh 包装器
    # 包装器对 sh -s 的参数转发有 bug（会把 --install-online 变成 ----install-online）
    mkdir -p /tmp/acme.sh && \
    curl -sSL https://github.com/acmesh-official/acme.sh/archive/refs/heads/master.tar.gz \
      | tar xz -C /tmp/acme.sh --strip-components=1 && \
    cd /tmp/acme.sh && \
    ./acme.sh --install --home /opt/acme.sh --nocron --no-profile && \
    cd / && rm -rf /tmp/acme.sh && \
    # 默认 CA 设成 Let's Encrypt（acme.sh 默认是 zerossl，需要邮箱注册更繁）
    /opt/acme.sh/acme.sh --home /opt/acme.sh --set-default-ca --server letsencrypt && \
    # 准备 ACME HTTP-01 verify 目录 + 证书目录
    mkdir -p /var/www/acme /etc/nginx/ssl /var/log/nginx

# 静态配置全部 COPY 进镜像（铁律：不允许 volume 挂载静态文件）
COPY nginx.conf /etc/nginx/nginx.conf
COPY conf.d/ /etc/nginx/conf.d/
COPY entrypoint.sh /entrypoint.sh
COPY acme-renew.sh /usr/local/bin/acme-renew.sh
RUN chmod +x /entrypoint.sh /usr/local/bin/acme-renew.sh

ENTRYPOINT ["/entrypoint.sh"]
CMD ["nginx", "-g", "daemon off;"]
