package logger

import (
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// 日志体系（爷爷铁律「长效存储、原始、细致」实现）：
//   business.log  —— 业务事件（连接、消息、上传……）
//   security.log  —— 安全事件（注入尝试、被拒、IP 拉黑）
//   audit.log     —— 管理操作审计（登录、修改、删除）
//   raw_ws.log    —— 原始 WSS 报文（最原始性，便于事后取证）
//
// 全部按天 rotate（命名 yyyy-mm-dd），保留 365 天，单文件最大 200MB，超出再轮转。
// 落盘目录由 docker bind 到 /srv/cs-data/logs/backend，重启不丢、清空仓库不丢。

const (
	maxSizeMB    = 200
	maxBackups   = 0   // 0 = 不按数量限制（按天命名 + 时间清理）
	maxAgeDays   = 365 // 长效保留一年
	compressGzip = true
)

type Loggers struct {
	Business *zap.Logger
	Security *zap.Logger
	Audit    *zap.Logger
	RawWS    *zap.Logger
}

func Init(level string, baseDir string) (*Loggers, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, err
	}

	lvl := parseLevel(level)
	encCfg := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "lvl",
		NameKey:        "ch",
		MessageKey:     "msg",
		StacktraceKey:  "stack",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     beijingTimeEncoder, // 强制北京时间
		EncodeDuration: zapcore.MillisDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
	encoder := zapcore.NewJSONEncoder(encCfg)

	build := func(name string) *zap.Logger {
		writer := &lumberjack.Logger{
			Filename:   filepath.Join(baseDir, name+".log"),
			MaxSize:    maxSizeMB,
			MaxBackups: maxBackups,
			MaxAge:     maxAgeDays,
			Compress:   compressGzip,
			LocalTime:  true,
		}
		core := zapcore.NewCore(encoder, zapcore.AddSync(writer), lvl)
		return zap.New(core, zap.AddCaller()).Named(name)
	}

	return &Loggers{
		Business: build("business"),
		Security: build("security"),
		Audit:    build("audit"),
		RawWS:    build("raw_ws"),
	}, nil
}

func parseLevel(s string) zapcore.Level {
	switch s {
	case "debug":
		return zapcore.DebugLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

// 强制北京时间编码（爷爷铁律之一：时区必须东八区）。
func beijingTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	tz, _ := time.LoadLocation("Asia/Shanghai")
	enc.AppendString(t.In(tz).Format("2006-01-02 15:04:05.000"))
}

// Sync 在进程退出前调用，保证日志缓冲落盘。
func (l *Loggers) Sync() {
	_ = l.Business.Sync()
	_ = l.Security.Sync()
	_ = l.Audit.Sync()
	_ = l.RawWS.Sync()
}
