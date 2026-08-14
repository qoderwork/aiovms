package database

import (
	"fmt"
	"log"
	"os"
	"time"

	"aiovms/pkg/logger"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type Config struct {
	Host         string
	Port         int
	User         string
	Password     string
	DBName       string
	Charset      string
	MaxIdleConns int
	MaxOpenConns int
	LogLevel     string
}

// EnsureDatabase creates the target database if it does not exist.
// VMS shares the same MySQL instance as NMS — this is idempotent
// and safe to call even when NMS already created the database.
func EnsureDatabase(cfg Config) error {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/?charset=%s&parseTime=True&loc=UTC",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Charset)

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return fmt.Errorf("failed to connect MySQL server: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to get sql.DB: %w", err)
	}
	defer sqlDB.Close()

	stmt := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET %s", cfg.DBName, cfg.Charset)
	if err := db.Exec(stmt).Error; err != nil {
		return fmt.Errorf("failed to create database %s: %w", cfg.DBName, err)
	}
	logger.Infof("database '%s' ensured", cfg.DBName)
	return nil
}

func Init(cfg Config) (*gorm.DB, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=True&loc=UTC",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.DBName, cfg.Charset)

	logLevel := gormlogger.Silent
	switch cfg.LogLevel {
	case "warn":
		logLevel = gormlogger.Warn
	case "info":
		logLevel = gormlogger.Info
	case "error":
		logLevel = gormlogger.Error
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: gormlogger.New(
			log.New(os.Stdout, "\r\n", log.LstdFlags),
			gormlogger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  logLevel,
				IgnoreRecordNotFoundError: true, // 查不到记录只返回 null，不刷错误日志
				Colorful:                  true,
			},
		),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get sql.DB: %w", err)
	}

	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Hour)

	logger.Info("database connected successfully")
	return db, nil
}

// AutoMigrate creates/updates VMS-specific tables only.
func AutoMigrate(db *gorm.DB, models ...interface{}) error {
	return db.AutoMigrate(models...)
}
