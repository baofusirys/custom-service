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
#
# 文件名说明：仓库 v3.0 后把 ip2region.xdb 拆成 ip2region_v4.xdb（~10MB IPv4，国内全市级）
# 和 ip2region_v6.xdb（~35MB IPv6）。我们只用 IPv4 库够用（国内访客 100%、海外访客主流），
# 落地文件名统一存成 ip2region.xdb 方便运行时代码不区分 v4/v6 命名。
FROM alpine:3.20 AS geoip
RUN apk add --no-cache wget ca-certificates
WORKDIR /geo
RUN set -eux; \
    URLS="https://raw.githubusercontent.com/lionsoul2014/ip2region/master/data/ip2region_v4.xdb \
          https://github.com/lionsoul2014/ip2region/raw/master/data/ip2region_v4.xdb \
          https://gitee.com/lionsoul/ip2region/raw/master/data/ip2region_v4.xdb"; \
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
