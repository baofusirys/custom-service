package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// Open 用业务账号建立连接池。强制超时、最大连接、最大空闲。
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(200)
	db.SetMaxIdleConns(50)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(10 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	// 校验连接级时区为东八区（铁律：所有时区必须北京时间）
	var tz string
	_ = db.QueryRowContext(ctx, "SELECT @@session.time_zone").Scan(&tz)
	if tz != "+08:00" {
		// 立即修正（不依赖 server-level 默认设错的情况）
		_, _ = db.ExecContext(ctx, "SET time_zone = '+08:00'")
	}
	return db, nil
}
