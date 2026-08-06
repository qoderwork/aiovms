package logger

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

type Config struct {
	Filename      string
	Level         string
	MaxSizeMB     int
	MaxBackups    int
	RetentionDays int
	Compress      bool
	Stdout        bool
}

var DefaultConfig = Config{
	Filename:      "logs/aiovms.log",
	Level:         "info",
	MaxSizeMB:     100,
	MaxBackups:    10,
	RetentionDays: 30,
	Compress:      true,
	Stdout:        true,
}

var (
	globalLogger      *zap.SugaredLogger
	globalAtomicLevel zap.AtomicLevel
	once              sync.Once
)

func Init(cfg ...Config) {
	once.Do(func() {
		c := DefaultConfig
		if len(cfg) > 0 {
			c = cfg[0]
		}
		globalLogger = buildLogger(c)
	})
}

func buildLogger(cfg Config) *zap.SugaredLogger {
	globalAtomicLevel = zap.NewAtomicLevel()
	setLevel(cfg.Level)

	encoderCfg := zapcore.EncoderConfig{
		MessageKey:    "msg",
		LevelKey:      "level",
		TimeKey:       "ts",
		CallerKey:     "caller",
		LineEnding:    zapcore.DefaultLineEnding,
		EncodeLevel:   zapcore.LowercaseLevelEncoder,
		EncodeTime:    utcTimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:  zapcore.ShortCallerEncoder,
	}

	var cores []zapcore.Core

	if cfg.Filename != "" {
		dir := filepath.Dir(cfg.Filename)
		os.MkdirAll(dir, 0755)
		fileWriter := &lumberjack.Logger{
			Filename:   cfg.Filename,
			MaxSize:    cfg.MaxSizeMB,
			MaxBackups: cfg.MaxBackups,
			MaxAge:     cfg.RetentionDays,
			Compress:   cfg.Compress,
		}
		cores = append(cores, zapcore.NewCore(
			zapcore.NewJSONEncoder(encoderCfg),
			zapcore.AddSync(fileWriter),
			globalAtomicLevel,
		))
	}

	if cfg.Stdout {
		encoderCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
		cores = append(cores, zapcore.NewCore(
			zapcore.NewConsoleEncoder(encoderCfg),
			zapcore.AddSync(os.Stdout),
			globalAtomicLevel,
		))
	}

	if len(cores) == 0 {
		cores = append(cores, zapcore.NewCore(
			zapcore.NewConsoleEncoder(encoderCfg),
			zapcore.AddSync(os.Stdout),
			globalAtomicLevel,
		))
	}

	core := zapcore.NewTee(cores...)
	l := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
	return l.Sugar()
}

func utcTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.UTC().Format("2006-01-02T15:04:05.000Z"))
}

func setLevel(level string) {
	switch strings.ToLower(level) {
	case "debug":
		globalAtomicLevel.SetLevel(zap.DebugLevel)
	case "info":
		globalAtomicLevel.SetLevel(zap.InfoLevel)
	case "warn":
		globalAtomicLevel.SetLevel(zap.WarnLevel)
	case "error":
		globalAtomicLevel.SetLevel(zap.ErrorLevel)
	default:
		globalAtomicLevel.SetLevel(zap.InfoLevel)
	}
}

func Sync() {
	if globalLogger != nil {
		_ = globalLogger.Sync()
	}
}

func Cleanup() { Sync() }

func getLogger() *zap.SugaredLogger {
	if globalLogger == nil {
		Init()
	}
	return globalLogger
}

func Debug(args ...interface{})                 { getLogger().Debug(args...) }
func Info(args ...interface{})                  { getLogger().Info(args...) }
func Warn(args ...interface{})                  { getLogger().Warn(args...) }
func Error(args ...interface{})                 { getLogger().Error(args...) }
func Fatal(args ...interface{})                 { getLogger().Fatal(args...) }
func Debugf(template string, args ...interface{}) { getLogger().Debugf(template, args...) }
func Infof(template string, args ...interface{})  { getLogger().Infof(template, args...) }
func Warnf(template string, args ...interface{})  { getLogger().Warnf(template, args...) }
func Errorf(template string, args ...interface{}) { getLogger().Errorf(template, args...) }
func Fatalf(template string, args ...interface{}) { getLogger().Fatalf(template, args...) }
func Infow(msg string, keysAndValues ...interface{})  { getLogger().Infow(msg, keysAndValues...) }
func Errorw(msg string, keysAndValues ...interface{}) { getLogger().Errorw(msg, keysAndValues...) }
