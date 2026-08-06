package config

import (
	"fmt"

	"github.com/spf13/viper"
	"aiovms/pkg/logger"
)

type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	CORS      CORSConfig      `mapstructure:"cors"`
	Database  DatabaseConfig  `mapstructure:"database"`
	Redis     RedisConfig     `mapstructure:"redis"`
	Logger    LoggerConfig    `mapstructure:"logger"`
	MediaMTX  MediaMTXConfig  `mapstructure:"mediamtx"`
	Recording RecordingConfig `mapstructure:"recording"`
}

type ServerConfig struct {
	Port int `mapstructure:"port"`
}

// CORSConfig defines allowed origins for browser cross-origin requests (Swagger UI / dev frontend).
// Production should restrict to NMS frontend origin; VMS is normally not accessed directly.
type CORSConfig struct {
	AllowedOrigins []string `mapstructure:"allowed_origins"`
}

type DatabaseConfig struct {
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	User         string `mapstructure:"user"`
	Password     string `mapstructure:"password"`
	DBName       string `mapstructure:"dbname"`
	Charset      string `mapstructure:"charset"`
	MaxIdleConns int    `mapstructure:"max_idle_conns"`
	MaxOpenConns int    `mapstructure:"max_open_conns"`
	LogLevel     string `mapstructure:"log_level"`
}

type RedisConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type LoggerConfig struct {
	Filename      string `mapstructure:"filename"`
	Level         string `mapstructure:"level"`
	MaxSizeMB     int    `mapstructure:"max_size_mb"`
	MaxBackups    int    `mapstructure:"max_backups"`
	RetentionDays int    `mapstructure:"retention_days"`
	Compress      bool   `mapstructure:"compress"`
	Stdout        bool   `mapstructure:"stdout"`
}

type MediaMTXConfig struct {
	URL string `mapstructure:"url"`
}

type RecordingConfig struct {
	Path          string `mapstructure:"path"`
	RetentionDays int    `mapstructure:"retention_days"`
	DiskWatermark int    `mapstructure:"disk_watermark"` // percentage, e.g. 90
}

func Load(configPath string) (*Config, error) {
	v := viper.New()

	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./configs")
	if configPath != "" {
		v.SetConfigFile(configPath)
	}

	v.SetDefault("server.port", 8081)
	v.SetDefault("cors.allowed_origins", []string{"http://localhost:8080", "http://localhost:8081"})
	v.SetDefault("database.charset", "utf8mb4")
	v.SetDefault("database.max_idle_conns", 10)
	v.SetDefault("database.max_open_conns", 50)
	v.SetDefault("database.log_level", "info")
	v.SetDefault("redis.db", 0)
	v.SetDefault("recording.retention_days", 7)
	v.SetDefault("recording.disk_watermark", 90)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port must be 1-65535, got %d", c.Server.Port)
	}
	if c.Database.Host == "" {
		return fmt.Errorf("database.host is required")
	}
	if c.MediaMTX.URL == "" {
		return fmt.Errorf("mediamtx.url is required")
	}
	return nil
}

func (c *Config) InitLogger() {
	logCfg := logger.Config{
		Filename:      c.Logger.Filename,
		Level:         c.Logger.Level,
		MaxSizeMB:     c.Logger.MaxSizeMB,
		MaxBackups:    c.Logger.MaxBackups,
		RetentionDays: c.Logger.RetentionDays,
		Compress:      c.Logger.Compress,
		Stdout:        c.Logger.Stdout,
	}
	if logCfg.Filename == "" {
		logCfg.Filename = "logs/aiovms.log"
	}
	if logCfg.Level == "" {
		logCfg.Level = "info"
	}
	logger.Init(logCfg)
}
