FROM nginx:1.27-alpine
ENV TZ=Asia/Shanghai
RUN apk add --no-cache tzdata gettext && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone

# 用户铁律：所有静态配置 COPY 进镜像，不允许 volume 挂载静态文件
COPY nginx.conf /etc/nginx/nginx.conf
COPY conf.d/ /etc/nginx/conf.d/
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh && \
    # 确保日志目录存在（实际会被 bind 到 /srv/cs-data/logs/nginx）
    mkdir -p /var/log/nginx
ENTRYPOINT ["/entrypoint.sh"]
CMD ["nginx", "-g", "daemon off;"]
