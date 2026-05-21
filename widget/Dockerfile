# Widget 是纯静态文件（vanilla JS + 一份 HTML + CSS），不需要构建步骤。
# 直接 COPY 到 nginx:alpine，访问 /widget/loader.js 即可。
FROM nginx:1.27-alpine
ENV TZ=Asia/Shanghai
RUN apk add --no-cache tzdata && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone

COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY public/ /usr/share/nginx/html/
EXPOSE 80
