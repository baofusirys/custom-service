package main

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/custom-service/backend/internal/config"
	"github.com/custom-service/backend/internal/db"
	"github.com/custom-service/backend/internal/hall"
	"github.com/custom-service/backend/internal/handler"
	"github.com/custom-service/backend/internal/livekit"
	"github.com/custom-service/backend/internal/logger"
	"github.com/custom-service/backend/internal/middleware"
	"github.com/custom-service/backend/internal/redisx"
	"github.com/custom-service/backend/internal/security"
	"github.com/custom-service/backend/internal/security/geoip"
	"github.com/custom-service/backend/internal/service"
	"github.com/custom-service/backend/internal/store"
	"github.com/custom-service/backend/internal/ws"
)

func main() {
	// === 配置 ===
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[FATAL] 配置加载失败: %v", err)
	}
	// 进程级时区也强制北京时间
	time.Local = cfg.Timezone

	// === 日志（4 路 zap，bind 到宿主机 /srv/cs-data/logs/backend）===
	logs, err := logger.Init(cfg.LogLevel, "/app/logs")
	if err != nil {
		log.Fatalf("[FATAL] 日志初始化失败: %v", err)
	}
	defer logs.Close()
	bizLog := logs.Business
	secLog := logs.Security
	auditLog := logs.Audit

	bizLog.Info("custom_service starting", zap.String("public_domain", cfg.PublicDomain))

	// === 数据库（先建库，再迁移）===
	if err := ensureDatabaseExists(cfg); err != nil {
		log.Fatalf("[FATAL] 建库失败: %v", err)
	}
	conn, err := db.Open(cfg.MySQLDSN())
	if err != nil {
		log.Fatalf("[FATAL] 连接 MySQL 失败: %v", err)
	}
	defer conn.Close()

	migCtx, migCancel := context.WithTimeout(context.Background(), 60*time.Second)
	if pending, pendingErr := db.PendingMigrations(migCtx, conn, "/app/migrations"); pendingErr != nil {
		migCancel()
		log.Fatalf("[FATAL] 检查迁移状态失败: %v", pendingErr)
	} else if len(pending) > 0 {
		bizLog.Info("migration_pre_backup_start", zap.String("version", pending[0]))
		if err := backupBeforeMigration(migCtx, cfg, pending[0], "/app/pre-migrate"); err != nil {
			migCancel()
			log.Fatalf("[FATAL] 迁移前备份失败，已中止迁移: %v", err)
		}
		bizLog.Info("migration_pre_backup_complete", zap.String("version", pending[0]))
	}
	applied, err := db.Migrate(migCtx, conn, "/app/migrations")
	migCancel()
	if err != nil {
		log.Fatalf("[FATAL] 自动迁移失败: %v", err)
	}
	if len(applied) > 0 {
		bizLog.Info("migrations applied", zap.Strings("versions", applied))
	}

	// === Redis ===
	rdb, err := redisx.New(cfg.Redis.Host, cfg.Redis.Port, cfg.Redis.Password)
	if err != nil {
		log.Fatalf("[FATAL] 连接 Redis 失败: %v", err)
	}
	defer rdb.Close()

	// === 加密器 + 限流器 ===
	cipher, err := security.NewCipher(cfg.DataAESKey)
	if err != nil {
		log.Fatalf("[FATAL] 加密器初始化失败: %v", err)
	}
	// 统一限流器：访客消息按会话，登录/上传/握手按 IP、账号、令牌组合计数。
	limiter := security.NewRateLimiter(rdb, secLog)

	// === [060] GeoIP 解析器（ip2region xdb v2，~11MB 全内存索引）===
	// 失败不 fatal：xdb 文件挂了不能拖垮主业务，VisitorSession 会拿到空 country/city 仍正常落库。
	// xdb 路径优先环境变量 GEOIP_PATH，默认 /app/data/ip2region.xdb（Dockerfile 已 COPY）。
	geoPath := os.Getenv("GEOIP_PATH")
	if geoPath == "" {
		geoPath = "/app/data/ip2region.xdb"
	}
	geoRes, geoErr := geoip.New(geoPath)
	if geoErr != nil {
		bizLog.Warn("geoip disabled (visitor country/city will be empty)",
			zap.String("path", geoPath), zap.Error(geoErr))
	} else {
		bizLog.Info("geoip loaded", zap.String("path", geoPath))
	}
	geoip.SetDefault(geoRes)

	// === Store / Service ===
	st := store.New(conn)
	svc := service.New(st, cipher, bizLog, secLog, auditLog, limiter, cfg.VisitorMsgPM)
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := svc.EnsureBootstrapAdmin(bootCtx, cfg.BootstrapUsername, cfg.BootstrapPassword); err != nil {
		bootCancel()
		log.Fatalf("[FATAL] 创建超管失败: %v", err)
	}
	bootCancel()

	// === WS Hub ===
	hub := ws.NewHub(ws.HubConfig{
		NodeID: uuid.NewString(),
		BizLog: bizLog, RawLog: logs.RawWS, SecLog: secLog,
		Cipher: cipher, Redis: rdb, Sink: svc, HeartbeatSec: 30,
	})
	// 反向注入 hub 引用，让 service 能给客服广播 page_navigation / visitor_enter 等通知
	svc.SetHub(hub)

	// === [119] 多人大厅：LiveKit 最小客户端 + 大厅服务（必须在 hub.Run 之前注入大厅帧处理器）===
	lk := livekit.NewClient(livekit.Config{
		APIKey:      cfg.LiveKit.APIKey,
		APISecret:   cfg.LiveKit.APISecret,
		InternalURL: cfg.LiveKit.InternalURL,
	}, bizLog)
	if cfg.LiveKit.Enabled() {
		bizLog.Info("hall_livekit_configured", zap.String("event", "hall_config"),
			zap.String("internal_url", cfg.LiveKit.InternalURL), zap.String("public_url", cfg.LiveKit.PublicURL))
	} else {
		bizLog.Warn("hall_disabled_livekit_not_configured", zap.String("event", "hall_config"),
			zap.String("note", "未配置 LIVEKIT_API_KEY，多人大厅关闭，原有功能不受影响"))
	}
	turnRealm, turnSecret := cfg.TurnRealm, cfg.TurnSecret
	hallSvc, err := hall.New(hall.Deps{
		Room:      store.HallDefaultRoom,
		Store:     st,
		Redis:     rdb,
		Media:     lk,
		Hub:       hall.NewHubBroadcaster(hub),
		Limiter:   limiter,
		DataKey:   cfg.DataAESKey,
		PublicURL: cfg.LiveKit.PublicURL,
		ICE: func(label string) []hall.ICEServer {
			if turnRealm == "" || turnSecret == "" {
				return []hall.ICEServer{}
			}
			cred := service.GenerateTurnCredential("hall-"+label, turnRealm, turnSecret)
			return []hall.ICEServer{{URLs: cred.URLs, Username: cred.Username, Credential: cred.Credential}}
		},
		BizLog: bizLog, SecLog: secLog, AuditLog: auditLog,
	})
	if err != nil {
		log.Fatalf("[FATAL] 大厅服务初始化失败: %v", err)
	}
	hub.SetHallHandler(hallSvc)

	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go hub.Run(hubCtx)
	hallSvc.Start(hubCtx)

	// === 上传目录 ===
	uploads := "/app/uploads"
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		log.Fatalf("[FATAL] 建上传目录失败: %v", err)
	}

	// === HTTP 路由 ===
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(middleware.Recovery(bizLog))
	r.Use(middleware.SecurityHeaders())
	r.Use(middleware.AccessLog(bizLog))
	// CORS：只允许 .env 明确配置的来源，拒绝 * 和任意 Origin 反射。
	r.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.CORSAllowedOrigins,
		AllowMethods:     []string{"GET", "POST", "OPTIONS"},
		AllowHeaders:     []string{"Authorization", "Content-Type"},
		ExposeHeaders:    []string{"Content-Length", "Content-Disposition"}, // [113] 导出下载需读取文件名
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	// [113] 回收站彻底删除时同步删除磁盘文件；后台维护（回收站到期清理、自动清理）
	svc.SetUploadsDir(uploads)
	svc.StartMaintenance(hubCtx)

	h := handler.NewHTTP(cfg, svc, hub, uploads)
	h.SetHall(hallSvc)
	handler.RegisterRoutes(r, h, st)

	srv := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // WSS 长连接需要禁掉 writeTimeout
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		bizLog.Info("http_server_listen", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[FATAL] listen: %v", err)
		}
	}()

	// 优雅退出
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	bizLog.Info("shutdown signal received")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	hubCancel()
	if !hallSvc.WaitTimeout(5 * time.Second) {
		bizLog.Warn("hall_background_tasks_still_running_on_shutdown")
	}
}

func backupBeforeMigration(ctx context.Context, cfg *config.Config, version, dir string) error {
	if version == "001_init" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	name := fmt.Sprintf("pre-migrate-%s-%s.sql.gz", time.Now().In(cfg.Timezone).Format("20060102-150405-0700"), version)
	path := filepath.Join(dir, name)
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	zw := gzip.NewWriter(out)
	cmd := exec.CommandContext(ctx, "mysqldump", "--single-transaction", "--routines", "--events", "--hex-blob",
		"-h", cfg.MySQL.Host, "-P", cfg.MySQL.Port, "-u", cfg.MySQL.User, cfg.MySQL.Database)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.MySQL.Password)
	cmd.Stdout = zw
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		_ = zw.Close()
		return fmt.Errorf("mysqldump failed: %w", err)
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	info, err := out.Stat()
	if err != nil {
		return err
	}
	if info.Size() <= 0 {
		return fmt.Errorf("backup file is empty: %s", name)
	}
	return nil
}

// ensureDatabaseExists 用 root 连接建库（CREATE DATABASE IF NOT EXISTS）。
// 实现「数据库自动判断是否需要弄然后去弄」铁律。
func ensureDatabaseExists(cfg *config.Config) error {
	if cfg.MySQL.Database == "" {
		return fmt.Errorf("MYSQL_DATABASE 未设置")
	}
	rdsn := cfg.MySQLRootDSN()
	rc, err := db.Open(rdsn)
	if err != nil {
		return err
	}
	defer rc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = rc.ExecContext(ctx, fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
		cfg.MySQL.Database))
	return err
}
