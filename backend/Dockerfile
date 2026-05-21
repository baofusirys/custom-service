# ===== 构建阶段 =====
FROM golang:1.22-alpine AS builder
ENV CGO_ENABLED=0 GOOS=linux GOFLAGS=-trimpath GOPROXY=https://goproxy.cn,direct
WORKDIR /src
COPY go.mod ./
# 没有 go.sum 时让 download 自动生成；以下命令做了三件事：
#   1) 拉所有 go.mod 中声明的依赖
#   2) 生成 go.sum（校验数据库一致性）
#   3) 失败立即中断构建
RUN go mod download all
COPY . .
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
