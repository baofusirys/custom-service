# ===== 构建阶段 =====
FROM golang:1.22-alpine AS builder
ENV CGO_ENABLED=0 GOOS=linux GOFLAGS=-trimpath GOPROXY=https://goproxy.cn,direct
WORKDIR /src
# 把源码先全部 COPY 进来，go mod tidy 需要分析 import 来补全间接依赖
COPY . .
# tidy 会同时：1) 补全间接依赖到 go.mod  2) 生成 go.sum  3) 下载所有依赖
RUN go mod tidy && go mod download
RUN go build -ldflags="-s -w" -o /out/server ./cmd/server

# ===== ip2region 数据下载阶段（[060]）=====
# 单独一层 + 多 mirror 兜底；GHA runner 在国外，github raw 秒拉。
# 国内服务器 build 时走第二/第三个 mirror。任一成功即可，全部失败构建失败。
# xdb 文件 ~11MB，更新频率：ip2region 仓库 1-2 月一次，跟着新版镜像走即可。
FROM alpine:3.20 AS geoip
RUN apk add --no-cache wget ca-certificates
WORKDIR /geo
RUN set -eux; \
    URLS="https://raw.githubusercontent.com/lionsoul2014/ip2region/master/data/ip2region.xdb \
          https://github.com/lionsoul2014/ip2region/raw/master/data/ip2region.xdb \
          https://gitee.com/lionsoul/ip2region/raw/master/data/ip2region.xdb"; \
    for u in $URLS; do \
        echo ">>> try $u"; \
        if wget --timeout=30 --tries=2 -O ip2region.xdb "$u"; then \
            SZ=$(stat -c%s ip2region.xdb); \
            echo ">>> downloaded $SZ bytes from $u"; \
            if [ "$SZ" -gt 1000000 ]; then \
                echo ">>> OK"; \
                exit 0; \
            fi; \
            echo ">>> file too small, try next"; \
            rm -f ip2region.xdb; \
        fi; \
    done; \
    echo "FATAL: all ip2region mirrors failed"; exit 1

# ===== 运行阶段 =====
FROM alpine:3.20
ENV TZ=Asia/Shanghai
RUN apk add --no-cache ca-certificates tzdata wget && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone && \
    addgroup -S app && adduser -S -G app app

WORKDIR /app
COPY --from=builder /out/server /app/server
COPY migrations /app/migrations
COPY --from=geoip /geo/ip2region.xdb /app/data/ip2region.xdb
RUN mkdir -p /app/logs /app/uploads && chown -R app:app /app
USER app
EXPOSE 8080
ENTRYPOINT ["/app/server"]
