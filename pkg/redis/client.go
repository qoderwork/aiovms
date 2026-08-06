// Package redis provides a minimal Redis client for pub/sub messaging.
// Used by Go VMS to notify Java NMS of camera status changes.
package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/go-redis/redis/v8"

	"aiovms/pkg/logger"
)

type Config struct {
	Host     string
	Port     int
	Password string
	DB       int
}

var client *goredis.Client

// Init creates the Redis client. Must be called once at startup.
func Init(cfg Config) error {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	client = goredis.NewClient(&goredis.Options{
		Addr:     addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping %s: %w", addr, err)
	}

	logger.Infof("redis connected: %s (db=%d)", addr, cfg.DB)
	return nil
}

// Publish sends a message to a Redis channel.
func Publish(ctx context.Context, channel string, message string) error {
	if client == nil {
		return fmt.Errorf("redis not initialized")
	}
	return client.Publish(ctx, channel, message).Err()
}

// Close shuts down the Redis connection.
func Close() {
	if client != nil {
		client.Close()
	}
}

// Ping checks Redis connectivity. Returns nil if healthy.
func Ping() error {
	if client == nil {
		return fmt.Errorf("redis not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return client.Ping(ctx).Err()
}
