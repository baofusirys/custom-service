package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Migrate 自动执行 migrations 目录下所有 *.sql，已执行的跳过。
// 设计目标（爷爷铁律）：用户绝不需要手动迁移；启动 docker 后自动跑。
//
// 表 schema_migrations 记录已执行版本号（文件名前缀如 001_、002_）。
//
// 单文件可包含多条语句（DSN 已开启 multiStatements=true）。
func Migrate(ctx context.Context, db *sql.DB, dir string) (applied []string, err error) {
	_, err = db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version VARCHAR(64) NOT NULL PRIMARY KEY,
  applied_at DATETIME NOT NULL,
  checksum VARCHAR(64) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`)
	if err != nil {
		return nil, fmt.Errorf("创建 schema_migrations 表失败: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读 migrations 目录失败: %w", err)
	}

	type mig struct {
		Version string
		Path    string
	}
	var migrations []mig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version := strings.TrimSuffix(e.Name(), ".sql")
		migrations = append(migrations, mig{Version: version, Path: filepath.Join(dir, e.Name())})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })

	for _, m := range migrations {
		var exists int
		err = db.QueryRowContext(ctx, "SELECT 1 FROM schema_migrations WHERE version=?", m.Version).Scan(&exists)
		if err == nil {
			// 已执行
			continue
		}
		if err != sql.ErrNoRows {
			return applied, fmt.Errorf("查询 %s 状态失败: %w", m.Version, err)
		}

		raw, e := os.ReadFile(m.Path)
		if e != nil {
			return applied, fmt.Errorf("读 %s 失败: %w", m.Path, e)
		}
		sum := simpleChecksum(raw)

		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return applied, fmt.Errorf("开启事务失败: %w", e)
		}
		if _, e := tx.ExecContext(ctx, string(raw)); e != nil {
			_ = tx.Rollback()
			return applied, fmt.Errorf("执行 %s 失败: %w", m.Version, e)
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at, checksum) VALUES(?, ?, ?)",
			m.Version, time.Now(), sum); e != nil {
			_ = tx.Rollback()
			return applied, fmt.Errorf("记录 %s 失败: %w", m.Version, e)
		}
		if e := tx.Commit(); e != nil {
			return applied, fmt.Errorf("提交 %s 失败: %w", m.Version, e)
		}
		applied = append(applied, m.Version)
	}
	return applied, nil
}

func simpleChecksum(b []byte) string {
	// 用于校验同一版本号文件不被偷偷修改；不需要密码学强度，但要稳定。
	var h uint64 = 1469598103934665603
	for _, c := range b {
		h ^= uint64(c)
		h *= 1099511628211
	}
	return fmt.Sprintf("%016x", h)
}
