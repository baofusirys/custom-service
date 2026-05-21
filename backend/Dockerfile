# ===== 构建阶段 =====
FROM golang:1.22-alpine AS builder
ENV CGO_ENABLED=0 GOOS=linux GOFLAGS=-trimpath GOPROXY=https://goproxy.cn,direct
WORKDIR /src
# 把源码先全部 COPY 进来，go mod tidy 需要分析 import 来补全间接依赖
COPY . .
# tidy 会同时：1) 补全间接依赖到 go.mod  2) 生成 go.sum  3) 下载所有依赖
RUN go mod tidy && go mod download
RUN go build -ldflags="-s -w" -o /out/server ./cmd/server

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
RUN mkdir -p /app/logs /app/uploads && chown -R app:app /app
USER app
EXPOSE 8080
ENTRYPOINT ["/app/server"]
