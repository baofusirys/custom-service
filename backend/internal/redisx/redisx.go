package redisx

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// New 建立 Redis 客户端，含连接池、超时和密码鉴权。
// 用途：在线状态、Pub/Sub 跨节点消息广播、IP 限流计数、会话锁。
func New(host, port, password string) (*redis.Client, error) {
	c := redis.NewClient(&redis.Options{
		Addr:            fmt.Sprintf("%s:%s", host, port),
		Password:        password,
		DB:              0,
		PoolSize:        200,
		MinIdleConns:    20,
		ConnMaxIdleTime: 5 * time.Minute,
		DialTimeout:     5 * time.Second,
		ReadTimeout:     3 * time.Second,
		WriteTimeout:    3 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return c, nil
}
