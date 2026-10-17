package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/idna"

	"github.com/custom-service/backend/internal/security"
)

// Config 是全局运行时配置。所有值都从环境变量读取，
// 这样 docker-compose 和 .env 是唯一来源，避免代码硬编码。
type Config struct {
	PublicDomain       string
	CORSAllowedOrigins []string
	EnableHTTPS        bool
	Timezone           *time.Location

	HTTPPort string
	LogLevel string

	MySQL MySQLConfig
	Redis RedisConfig

	JWTSecret   []byte
	DataAESKey  []byte
	MaxUploadMB int

	// 安全
	// 访客消息按会话限流；登录、上传和批量入口另按 IP/账号/令牌多维限流。
	VisitorMsgPM int

	// Bootstrap 超管
	BootstrapUsername string
	BootstrapPassword string

	// TURN/STUN（CoTURN 短期凭证）
	// 与 coturn 容器共享同一个 secret，用 HMAC-SHA1 生成 password
	TurnRealm  string
	TurnSecret string

	// [119] 多人大厅：自托管 LiveKit 媒体服务（未配置 LIVEKIT_API_KEY 时大厅整体关闭）
	LiveKit LiveKitConfig
}

// LiveKitConfig 大厅媒体服务配置。APISecret 只在内存中用于签发/校验 JWT，任何日志都不得输出。
type LiveKitConfig struct {
	APIKey      string // LiveKit API Key（JWT 的 iss，非机密，但禁止占位值）
	APISecret   string // LiveKit API Secret（≥32 字符随机）
	InternalURL string // 后端调用 Twirp 服务端 API 的内网地址，默认 http://cs-livekit:7880
	PublicURL   string // 下发给客户端的信令地址，默认 wss://<PUBLIC_DOMAIN>/lk
}

// Enabled 是否配置了 LiveKit（未配置时大厅关闭，不影响原有功能）。
func (l LiveKitConfig) Enabled() bool { return l.APIKey != "" }

type MySQLConfig struct {
	Host     string
	Port     string
	Database string
	User     string
	Password string
}

type RedisConfig struct {
	Host     string
	Port     string
	Password string
}

// Load 从环境读取并校验配置。任何关键字段缺失/弱值都立即 fail-fast，
// 防止把不安全的服务跑起来。
func Load() (*Config, error) {
	tz, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, fmt.Errorf("加载北京时区失败: %w", err)
	}

	cfg := &Config{
		PublicDomain:       os.Getenv("PUBLIC_DOMAIN"),
		CORSAllowedOrigins: splitCSV(os.Getenv("CORS_ALLOWED_ORIGINS")),
		EnableHTTPS:        os.Getenv("ENABLE_HTTPS") == "true",
		Timezone:           tz,
		HTTPPort:           defaultStr(os.Getenv("BACKEND_HTTP_PORT"), "8080"),
		LogLevel:           defaultStr(os.Getenv("BACKEND_LOG_LEVEL"), "info"),
		MaxUploadMB:        defaultInt(os.Getenv("BACKEND_MAX_UPLOAD_MB"), 20),
		// 0 = 不限制访客消息
		VisitorMsgPM:      defaultInt(os.Getenv("SECURITY_VISITOR_MSG_PM"), 20),
		BootstrapUsername: defaultStr(os.Getenv("ADMIN_BOOTSTRAP_USERNAME"), "admin"),
		BootstrapPassword: os.Getenv("ADMIN_BOOTSTRAP_PASSWORD"),
		TurnRealm:         os.Getenv("TURN_REALM"),
		TurnSecret:        os.Getenv("TURN_STATIC_AUTH_SECRET"),
		LiveKit: LiveKitConfig{
			APIKey:      strings.TrimSpace(os.Getenv("LIVEKIT_API_KEY")),
			APISecret:   os.Getenv("LIVEKIT_API_SECRET"),
			InternalURL: strings.TrimSpace(os.Getenv("LIVEKIT_INTERNAL_URL")),
			PublicURL:   strings.TrimSpace(os.Getenv("LIVEKIT_PUBLIC_URL")),
		},
		MySQL: MySQLConfig{
			Host:     defaultStr(os.Getenv("MYSQL_HOST"), "mysql"),
			Port:     defaultStr(os.Getenv("MYSQL_PORT"), "3306"),
			Database: os.Getenv("MYSQL_DATABASE"),
			User:     os.Getenv("MYSQL_USER"),
			Password: os.Getenv("MYSQL_PASSWORD"),
		},
		Redis: RedisConfig{
			Host:     defaultStr(os.Getenv("REDIS_HOST"), "redis"),
			Port:     defaultStr(os.Getenv("REDIS_PORT"), "6379"),
			Password: os.Getenv("REDIS_PASSWORD"),
		},
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if len(jwtSecret) < 32 || isPlaceholder(jwtSecret) {
		return nil, errors.New("JWT_SECRET 必须至少 32 个字符，请改 .env")
	}
	cfg.JWTSecret = []byte(jwtSecret)

	aesKeyHex := os.Getenv("DATA_AES_KEY")
	if len(aesKeyHex) != 64 {
		return nil, errors.New("DATA_AES_KEY 必须是 64 个十六进制字符（即 32 字节 AES-256 密钥），请改 .env")
	}
	key, err := hexDecode(aesKeyHex)
	if err != nil {
		return nil, fmt.Errorf("DATA_AES_KEY 不是合法 hex: %w", err)
	}
	cfg.DataAESKey = key

	rootPassword := os.Getenv("MYSQL_ROOT_PASSWORD")
	if cfg.MySQL.Database == "" || cfg.MySQL.User == "" || len(cfg.MySQL.Password) < 32 || len(rootPassword) < 32 || isPlaceholder(cfg.MySQL.Password) || isPlaceholder(rootPassword) {
		return nil, errors.New("MYSQL_ROOT_PASSWORD / MYSQL_PASSWORD 必须至少 32 个字符，数据库账号字段也必须设置")
	}
	if len(cfg.Redis.Password) < 32 || isPlaceholder(cfg.Redis.Password) {
		return nil, errors.New("REDIS_PASSWORD 必须至少 32 个字符")
	}
	if cfg.TurnSecret != "" && (len(cfg.TurnSecret) < 32 || isPlaceholder(cfg.TurnSecret)) {
		return nil, errors.New("TURN_STATIC_AUTH_SECRET 必须使用随机强密钥")
	}
	if cfg.BootstrapPassword == "" || !security.StrongPassword(cfg.BootstrapPassword, cfg.BootstrapUsername) || isPlaceholder(cfg.BootstrapPassword) {
		return nil, errors.New("ADMIN_BOOTSTRAP_PASSWORD 必须至少 12 字符、包含大小写数字符号且不能使用占位密码")
	}
	for _, origin := range cfg.CORSAllowedOrigins {
		u, parseErr := url.Parse(origin)
		if origin == "*" || parseErr != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" ||
			(u.Scheme != "https" && u.Scheme != "http") {
			return nil, errors.New("CORS_ALLOWED_ORIGINS 必须是明确的 http(s) 来源，禁止 *")
		}
	}
	if len(cfg.CORSAllowedOrigins) == 0 {
		return nil, errors.New("CORS_ALLOWED_ORIGINS 必须至少配置一个明确来源")
	}
	if err := normalizeLiveKit(&cfg.LiveKit, cfg.PublicDomain); err != nil {
		return nil, err
	}

	return cfg, nil
}

// liveKitKeyPattern LiveKit API Key 白名单：LIVEKIT_KEYS 以 "key: secret" 组装，禁止冒号、空白等破坏格式的字符。
var liveKitKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,64}$`)

// liveKitKeyPlaceholders LiveKit 文档/开发模式常见的示例 Key，生产一律拒绝。
var liveKitKeyPlaceholders = map[string]bool{
	"devkey": true, "apikey": true, "api_key": true, "key": true, "key1": true, "key2": true,
	"test": true, "demo": true, "changeme": true, "your-api-key": true, "livekit": true,
}

// normalizeLiveKit [119] 大厅媒体服务配置自检：
//   - LIVEKIT_API_KEY 为空：大厅关闭（main 打 WARN），其余字段不校验；
//   - 非空：Key 必须合法且不是示例值，Secret 必须 ≥32 字符且不命中占位黑名单，地址必须合法，否则拒绝启动。
//
// 错误信息只写变量名，绝不包含变量值。
func normalizeLiveKit(lk *LiveKitConfig, publicDomain string) error {
	if lk.APIKey == "" {
		lk.APISecret = ""
		return nil
	}
	if !liveKitKeyPattern.MatchString(lk.APIKey) || liveKitKeyPlaceholders[strings.ToLower(lk.APIKey)] {
		return errors.New("LIVEKIT_API_KEY 只允许 3-64 位字母、数字、下划线、中划线，且不能使用示例值")
	}
	if len(lk.APISecret) < 32 || isPlaceholder(lk.APISecret) || strings.ContainsAny(lk.APISecret, "\r\n") {
		return errors.New("LIVEKIT_API_SECRET 必须是至少 32 个字符的随机强密钥（请用 openssl rand 生成）")
	}
	internal := lk.InternalURL
	if internal == "" {
		internal = "http://cs-livekit:7880"
	}
	iu, err := url.Parse(internal)
	if err != nil || (iu.Scheme != "http" && iu.Scheme != "https") || iu.Host == "" || (iu.Path != "" && iu.Path != "/") ||
		iu.RawQuery != "" || iu.User != nil || iu.Fragment != "" {
		return errors.New("LIVEKIT_INTERNAL_URL 必须是不带路径的 http(s) 地址，例如 http://cs-livekit:7880")
	}
	lk.InternalURL = iu.Scheme + "://" + iu.Host
	public := lk.PublicURL
	if public == "" {
		domain := strings.TrimSpace(publicDomain)
		if domain == "" {
			return errors.New("启用大厅时必须设置 LIVEKIT_PUBLIC_URL 或 PUBLIC_DOMAIN")
		}
		public = "wss://" + domain + "/lk"
	}
	pu, err := url.Parse(public)
	if err != nil || (pu.Scheme != "wss" && pu.Scheme != "ws") || pu.Host == "" || pu.RawQuery != "" || pu.User != nil || pu.Fragment != "" {
		return errors.New("LIVEKIT_PUBLIC_URL 必须是 ws(s) 地址，例如 wss://<域名>/lk")
	}
	// 中文域名（IDN）统一转成 punycode，客户端与浏览器 CSP 看到的都是 ASCII 主机名。
	host, port := pu.Hostname(), pu.Port()
	asciiHost, err := idna.Lookup.ToASCII(host)
	if err != nil || asciiHost == "" {
		return errors.New("LIVEKIT_PUBLIC_URL 的主机名不合法")
	}
	if port != "" {
		asciiHost += ":" + port
	}
	pu.Host = asciiHost
	lk.PublicURL = strings.TrimRight(pu.String(), "/")
	return nil
}

func splitCSV(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, strings.TrimRight(item, "/"))
		}
	}
	return out
}

func isPlaceholder(v string) bool {
	l := strings.ToLower(strings.TrimSpace(v))
	for _, bad := range []string{"secret", "changeme", "your-secret-key", "123456", "password", "admin", "test", "demo"} {
		if l == bad || strings.Contains(l, bad) {
			return true
		}
	}
	return false
}

// MySQLDSN 构造连接串，强制 utf8mb4 + 时区。
// parseTime=true 让 driver 把 DATETIME 解析为 time.Time。
// loc=Asia%2FShanghai 让 driver 用东八区解释时间字段。
func (c *Config) MySQLDSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&loc=Asia%%2FShanghai&multiStatements=true&interpolateParams=false",
		c.MySQL.User, c.MySQL.Password, c.MySQL.Host, c.MySQL.Port, c.MySQL.Database)
}

// MySQLRootDSN 用 root 账号连接 server 级别（建库/查表用），同一连接串结构。
// 不带 database，便于 CREATE DATABASE IF NOT EXISTS。
func (c *Config) MySQLRootDSN() string {
	rootPass := os.Getenv("MYSQL_ROOT_PASSWORD")
	return fmt.Sprintf("root:%s@tcp(%s:%s)/?charset=utf8mb4&parseTime=true&loc=Asia%%2FShanghai&multiStatements=true",
		rootPass, c.MySQL.Host, c.MySQL.Port)
}

func defaultStr(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func defaultInt(v string, d int) int {
	if v == "" {
		return d
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return d
	}
	return n
}

func hexDecode(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, errors.New("hex 长度必须为偶数")
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		hi, err := hexVal(s[i])
		if err != nil {
			return nil, err
		}
		lo, err := hexVal(s[i+1])
		if err != nil {
			return nil, err
		}
		out[i/2] = hi<<4 | lo
	}
	return out, nil
}

func hexVal(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	}
	return 0, errors.New("非法 hex 字符")
}
