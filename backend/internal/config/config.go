package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config 是全局运行时配置。所有值都从环境变量读取，
// 这样 docker-compose 和 .env 是唯一来源，避免代码硬编码。
type Config struct {
	PublicDomain string
	EnableHTTPS  bool
	Timezone     *time.Location

	HTTPPort string
	LogLevel string

	MySQL MySQLConfig
	Redis RedisConfig

	JWTSecret   []byte
	DataAESKey  []byte
	MaxUploadMB int

	// 安全
	IPHTTPRPM            int
	IPWSHandshakePM      int
	VisitorMsgPM         int
	IPBlacklistThreshold int

	// Bootstrap 超管
	BootstrapUsername string
	BootstrapPassword string

	// TURN/STUN（CoTURN 短期凭证）
	// 与 coturn 容器共享同一个 secret，用 HMAC-SHA1 生成 password
	TurnRealm  string
	TurnSecret string
}

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
		PublicDomain:         os.Getenv("PUBLIC_DOMAIN"),
		EnableHTTPS:          os.Getenv("ENABLE_HTTPS") == "true",
		Timezone:             tz,
		HTTPPort:             defaultStr(os.Getenv("BACKEND_HTTP_PORT"), "8080"),
		LogLevel:             defaultStr(os.Getenv("BACKEND_LOG_LEVEL"), "info"),
		MaxUploadMB:          defaultInt(os.Getenv("BACKEND_MAX_UPLOAD_MB"), 20),
		IPHTTPRPM:            defaultInt(os.Getenv("SECURITY_IP_HTTP_RPM"), 60),
		IPWSHandshakePM:      defaultInt(os.Getenv("SECURITY_IP_WS_HANDSHAKE_PM"), 5),
		VisitorMsgPM:         defaultInt(os.Getenv("SECURITY_VISITOR_MSG_PM"), 10),
		IPBlacklistThreshold: defaultInt(os.Getenv("SECURITY_IP_BLACKLIST_THRESHOLD"), 200),
		BootstrapUsername:    defaultStr(os.Getenv("ADMIN_BOOTSTRAP_USERNAME"), "admin"),
		BootstrapPassword:    os.Getenv("ADMIN_BOOTSTRAP_PASSWORD"),
		TurnRealm:            os.Getenv("TURN_REALM"),
		TurnSecret:           os.Getenv("TURN_STATIC_AUTH_SECRET"),
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
	if len(jwtSecret) < 32 {
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

	if cfg.MySQL.Database == "" || cfg.MySQL.User == "" || cfg.MySQL.Password == "" {
		return nil, errors.New("MYSQL_DATABASE / MYSQL_USER / MYSQL_PASSWORD 必须设置")
	}
	if cfg.Redis.Password == "" {
		return nil, errors.New("REDIS_PASSWORD 必须设置")
	}
	if cfg.BootstrapPassword == "" || len(cfg.BootstrapPassword) < 8 {
		return nil, errors.New("ADMIN_BOOTSTRAP_PASSWORD 必须至少 8 字符")
	}

	return cfg, nil
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
