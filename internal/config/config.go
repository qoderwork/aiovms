package config

import (
	"fmt"
	"os"

	"aiovms/pkg/logger"
	"github.com/spf13/viper"
)

type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	CORS      CORSConfig      `mapstructure:"cors"`
	Database  DatabaseConfig  `mapstructure:"database"`
	Vault     VaultConfig     `mapstructure:"vault"`
	Logger    LoggerConfig    `mapstructure:"logger"`
	MediaMTX  MediaMTXConfig  `mapstructure:"mediamtx"`
	Recording RecordingConfig `mapstructure:"recording"`
}

// VaultConfig defines Vault integration settings. When Enabled is true,
// the application reads secrets from Vault at startup and overrides
// corresponding config.yaml values. If Required is true and Vault is
// unreachable, the application exits instead of falling back.
type VaultConfig struct {
	Enabled   bool   `mapstructure:"enabled"`
	Required  bool   `mapstructure:"required"`    // if true, vault failure is fatal
	Addr      string `mapstructure:"addr"`       // e.g. "https://vault:8200"
	Token     string `mapstructure:"token"`      // VAULT_TOKEN env var
	Path      string `mapstructure:"path"`       // e.g. "secret/data/vms" (KV v2)
	KVVersion int    `mapstructure:"kv_version"`  // 1 or 2; default 2
	CABase64  string `mapstructure:"ca_base64"`   // optional base64 PEM CA cert
	Insecure  bool   `mapstructure:"insecure"`    // skip TLS verify (dev only)
	TimeoutSec int   `mapstructure:"timeout_sec"` // HTTP timeout in seconds, default 10
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
	Path            string `mapstructure:"path"`
	RetentionDays   int    `mapstructure:"retention_days"`
	DiskWatermark   int    `mapstructure:"disk_watermark"`    // percentage, e.g. 90
	SegmentDuration string `mapstructure:"segment_duration"`  // MediaMTX recordSegmentDuration, e.g. "1m"
	ScanIntervalSec int    `mapstructure:"scan_interval_sec"` // disk scan fallback interval; hook is the fast path
	HookBaseURL     string `mapstructure:"hook_base_url"`     // aiovms URL reachable FROM the mediamtx container; empty disables the hook
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
	v.SetDefault("vault.enabled", false)
	v.SetDefault("vault.required", false)
	v.SetDefault("vault.insecure", false)
	v.SetDefault("vault.timeout_sec", 10)
	v.SetDefault("vault.kv_version", 2)
	v.SetDefault("vault.path", "secret/data/vms")
	v.SetDefault("recording.retention_days", 7)
	v.SetDefault("recording.disk_watermark", 90)
	v.SetDefault("recording.segment_duration", "1m")
	v.SetDefault("recording.scan_interval_sec", 30)

	// Allow VAULT_TOKEN / VAULT_ADDR env vars to override config.yaml values
	v.BindEnv("vault.token", "VAULT_TOKEN")
	v.BindEnv("vault.addr", "VAULT_ADDR")

	// database.password supports VMS_DB_PASSWORD env injection.
	// Final precedence: vault (main.go override, applied after Unmarshal) >
	// env (viper) > config.yaml.
	v.BindEnv("database.password", "VMS_DB_PASSWORD")

	// Viper ignores empty env values by default (allowEmptyEnv=false), so an
	// explicitly-set-but-empty VMS_DB_PASSWORD silently falls back to
	// config.yaml. Warn here (logger is not initialized yet) to avoid
	// confusion in production.
	if val, ok := os.LookupEnv("VMS_DB_PASSWORD"); ok && val == "" {
		fmt.Fprintln(os.Stderr, "warning: VMS_DB_PASSWORD is set but empty; ignored, database.password falls back to config.yaml")
	}

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
	if c.Vault.Enabled {
		if c.Vault.Addr == "" {
			return fmt.Errorf("vault.addr is required when vault.enabled is true")
		}
		if c.Vault.Path == "" {
			return fmt.Errorf("vault.path is required when vault.enabled is true")
		}
		// kv_version: 0 means unset and defaults to 2 in vault.NewClient
		if c.Vault.KVVersion < 0 || c.Vault.KVVersion > 2 {
			return fmt.Errorf("vault.kv_version must be 1 or 2, got %d", c.Vault.KVVersion)
		}
		if c.Vault.TimeoutSec < 0 {
			return fmt.Errorf("vault.timeout_sec must be >= 0, got %d", c.Vault.TimeoutSec)
		}
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
