# 构建阶段
FROM node:24.19.0-alpine3.23 AS builder
ENV TZ=Asia/Shanghai
ARG VITE_API_BASE=/api
ARG VITE_WS_BASE=/ws
ENV VITE_API_BASE=$VITE_API_BASE VITE_WS_BASE=$VITE_WS_BASE
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm config set registry https://registry.npmmirror.com && \
    npm ci --no-audit --no-fund
COPY . .
RUN npm run build

# 运行阶段：把构建产物嵌进 nginx:alpine 镜像直接给静态资源用
FROM nginx:1.30.3-alpine3.23
ENV TZ=Asia/Shanghai
RUN apk add --no-cache tzdata && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone
# 本镜像不直接对外，由顶层 nginx 反代。
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /src/dist /usr/share/nginx/html
EXPOSE 80
