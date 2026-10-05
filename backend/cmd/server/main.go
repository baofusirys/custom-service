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
	"github.com/custom-service/backend/internal/handler"
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
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go hub.Run(hubCtx)

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
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	h := handler.NewHTTP(cfg, svc, hub, uploads)
	api := r.Group("/api")
	{
		api.GET("/health", h.Health)
		api.GET("/version", h.Version) // [053] 集成方对比 deployed vs upstream 最新版用

		// 访客侧（无需登录，靠 visitor JWT token、Origin/CORS、SSL 和 IP/会话限流）。
		visitor := api.Group("/visitor")
		{
			visitor.POST("/session", h.VisitorSession)
			visitor.GET("/settings", h.VisitorPublicSettings)
			// TURN/STUN 短期凭证（每次通话前 fetch；24h TTL；返回 username/credential/urls）
			visitor.GET("/turn-credential", h.TurnCredential)
		}

		// 客服登录：IP/账号限流、失败锁定/验证码和 bcrypt 共同防爆破。
		api.POST("/agent/login", h.AgentLogin)
		// [064] token 续期：解决 [068] iOS App 12h 后 401 死循环问题
		// 不挂 AgentAuth middleware 因为 token 可能已过期（ParseAgentTokenAllowExpired 自己处理）
		api.POST("/agent/login/refresh", h.RefreshAgentToken)

		// 客服已登录
		ag := api.Group("/agent", middleware.AgentAuthWithStore(cfg.JWTSecret, st))
		{
			ag.POST("/logout", h.AgentLogout)
			ag.GET("/conversations", h.ListConversations)
			ag.GET("/conversations/:id/messages", h.ListMessages)
			ag.POST("/conversations/:id/assign", h.AssignSelf)
			ag.POST("/conversations/:id/read", h.MarkRead)
			ag.POST("/conversations/:id/close", h.CloseConv)
			// TURN/STUN 短期凭证（客服 web/iPhone 通话前 fetch；与 visitor 接口同源 service.GenerateTurnCredential）
			ag.GET("/turn-credential", h.TurnCredential)
			// [055] 关联访客（同 IP 30 天内出现的其他 vid，给客服参考"疑似同一人"）
			ag.GET("/visitor/:vid/related", h.RelatedVisitors)
			// [090] 按客户聚合的完整历史对话（跨该访客所有会话段，含已结束）
			ag.GET("/visitor/:vid/messages", h.ListMessagesByVisitor)
		}

		// 管理（仅 admin）
		adm := api.Group("/admin", middleware.AgentAuthWithStore(cfg.JWTSecret, st), middleware.AdminOnly())
		{
			adm.GET("/agents", h.ListAgents)
			adm.POST("/agents", h.CreateAgent)
			adm.POST("/agents/active", h.DisableAgent)
			adm.GET("/settings", h.GetSettings)
			adm.POST("/settings", h.UpdateSettings)
		}

		// 文件上传 / 下载
		api.POST("/upload", h.Upload)
	}

	// WSS endpoint
	r.GET("/ws/visitor", h.VisitorWS)
	r.GET("/ws/agent", h.AgentWS)

	// 文件直出（Nginx 也可以代理）
	r.GET("/files/*key", h.ServeFile)

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
