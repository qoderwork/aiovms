package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "aiovms/docs" // swagger generated docs
	"aiovms/internal/audit"
	"aiovms/internal/camera"
	"aiovms/internal/config"
	"aiovms/internal/controller"
	"aiovms/internal/mediamtx"
	"aiovms/internal/middleware"
	"aiovms/internal/model"
	"aiovms/internal/recording"
	"aiovms/internal/schedule"
	"aiovms/pkg/crypto"
	"aiovms/pkg/database"
	"aiovms/pkg/logger"
	_ "aiovms/pkg/metrics" // register prometheus metrics
	"aiovms/pkg/vault"
)

// @title AIO VMS API
// @version 1.0
// @description Video Management Service for AIO NMS.
// @description Provides camera management, recording, and scheduling APIs. Internal service invoked by NMS proxy.

// @contact.name API Support
// @contact.url https://github.com/qoderwork/aiovms

// @license.name Apache 2.0
// @license.url http://www.apache.org/licenses/LICENSE-2.0.html

// @host
// @BasePath /

// @securityDefinitions.apikey TenantHeader
// @in header
// @name X-License-Id
// @description Tenant (license) ID, forwarded by NMS. Use "default" for tenant 1.

// @securityDefinitions.apikey UserHeader
// @in header
// @name X-User-Id
// @description User ID, forwarded by NMS.

func main() {
	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	// 1. Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	cfg.InitLogger()
	defer logger.Cleanup()

	// 1b. Override config values from Vault (optional, with graceful fallback)
	if cfg.Vault.Enabled {
		vaultClient, err := vault.NewClient(vault.Config{
			Enabled:    cfg.Vault.Enabled,
			Addr:       cfg.Vault.Addr,
			Token:      cfg.Vault.Token,
			Path:       cfg.Vault.Path,
			KVVersion:  cfg.Vault.KVVersion,
			CABase64:   cfg.Vault.CABase64,
			Insecure:   cfg.Vault.Insecure,
			TimeoutSec: cfg.Vault.TimeoutSec,
		})
		if err != nil {
			msg := fmt.Sprintf("vault client init: %v", err)
			if cfg.Vault.Required {
				logger.Fatalf("%s (vault.required=true, refusing to start)", msg)
			}
			logger.Warnf("%s (falling back to config.yaml values)", msg)
		} else {
			// required=true: retry transient failures (vault restart/unseal
			// window) before giving up; required=false: single attempt,
			// fall back to config.yaml fast.
			var backoff []time.Duration
			if cfg.Vault.Required {
				backoff = vault.DefaultRetryBackoff
			}
			secrets, err := vaultClient.ReadWithRetry(context.Background(), backoff)
			if err != nil {
				msg := fmt.Sprintf("vault: %v", err)
				if cfg.Vault.Required {
					logger.Fatalf("%s (vault.required=true, refusing to start)", msg)
				}
				logger.Warnf("%s (falling back to config.yaml values)", msg)
			} else {
				// Override database password from vault
				if v, ok := secrets["database.password"]; ok && v != "" {
					cfg.Database.Password = v
					logger.Info("database.password loaded from vault")
				}
				// Override encryption key from vault (replaces VMS_ENCRYPTION_KEY env var)
				if v, ok := secrets["encryption.key"]; ok && v != "" {
					if err := os.Setenv("VMS_ENCRYPTION_KEY", v); err != nil {
						logger.Errorf("set VMS_ENCRYPTION_KEY from vault: %v", err)
					} else {
						logger.Info("encryption.key loaded from vault")
					}
				}
				logger.Info("vault integration active, secrets loaded")
			}
		}
	}

	// 2. Initialize database (shared MySQL with NMS)
	dbCfg := database.Config{
		Host: cfg.Database.Host, Port: cfg.Database.Port,
		User: cfg.Database.User, Password: cfg.Database.Password,
		DBName: cfg.Database.DBName, Charset: cfg.Database.Charset,
		MaxIdleConns: cfg.Database.MaxIdleConns, MaxOpenConns: cfg.Database.MaxOpenConns,
		LogLevel: cfg.Database.LogLevel,
	}
	if err := database.EnsureDatabase(dbCfg); err != nil {
		logger.Fatalf("ensure database: %v", err)
	}
	db, err := database.Init(dbCfg)
	if err != nil {
		logger.Fatalf("database init: %v", err)
	}

	// 3. AutoMigrate VMS tables only
	if err := database.AutoMigrate(db,
		&model.Camera{}, &model.Recording{}, &model.RecordingSession{},
		&model.RecordSchedule{}, &model.VMSAuditLog{},
	); err != nil {
		logger.Fatalf("auto migrate: %v", err)
	}

	// 3b. Init audit logger
	audit.Init(db)

	// 5. Initialize crypto (camera password encryption) — fail-fast on missing key.
	if err := crypto.Init(); err != nil {
		logger.Fatalf("crypto init: %v (VMS_ENCRYPTION_KEY must be set to a 32+ byte string)", err)
	}

	// 6. Initialize MediaMTX client
	mtxClient := mediamtx.NewClient(cfg.MediaMTX.URL)
	if err := mtxClient.HealthCheck(); err != nil {
		logger.Warnf("mediaMTX health check: %v", err)
	}

	// 7. Start MediaMTX actuator — the single writer for all MTX mutations.
	//    Services and the reconciler enqueue intent; the actuator applies it
	//    serially with retries, so MTX state always converges to DB intent.
	actuator := controller.NewActuator(mtxClient)

	// 7b. Segment-complete hook command pushed to cam-* paths (empty when
	//     recording.hook_base_url is unset → scanner-only mode).
	hookCommand := mediamtx.SegmentCompleteHookCommand(cfg.Recording.HookBaseURL)

	// 8. Bootstrap layers
	camRepo := camera.NewRepository(db)
	cameraSvc := camera.NewService(camRepo, actuator, mtxClient, cfg.Recording.Path, cfg.Recording.SegmentDuration, hookCommand)

	recRepo := recording.NewRepository(db)
	recSvc := recording.NewService(recRepo, cameraSvc, actuator)

	schRepo := schedule.NewRepository(db)
	schSvc := schedule.NewService(schRepo)

	// 9. Start unified reconciler (replaces startup sync, cron triggerJob,
	//    event-driven MTX recovery, and camera status checker).
	reconciler := controller.NewReconciler(camRepo, schRepo, recRepo, mtxClient, actuator,
		cfg.Recording.Path, cfg.Recording.SegmentDuration, hookCommand)

	// 10. Start background workers
	var wg sync.WaitGroup

	wg.Add(1)
	go func() { defer wg.Done(); actuator.Run() }()

	wg.Add(1)
	go func() { defer wg.Done(); reconciler.Run() }()

	recScanner := recording.NewScanner(recSvc, recRepo, cfg.Recording.Path, camRepo,
		time.Duration(cfg.Recording.ScanIntervalSec)*time.Second)
	wg.Add(1)
	go func() { defer wg.Done(); recScanner.Run() }()

	retention := recording.NewRetention(recRepo, cfg.Recording.Path,
		cfg.Recording.RetentionDays, cfg.Recording.DiskWatermark)
	wg.Add(1)
	go func() { defer wg.Done(); retention.Run() }()

	// 11. Setup Gin router
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery(), middleware.CORSMiddleware(cfg.CORS.AllowedOrigins))

	router.GET("/healthz", func(c *gin.Context) {
		if sqlDB, err := db.DB(); err == nil {
			if err := sqlDB.Ping(); err != nil {
				logger.Errorf("healthz: db ping: %v", err)
				c.JSON(503, gin.H{"status": "unhealthy"})
				return
			}
		} else {
			logger.Errorf("healthz: db handle: %v", err)
			c.JSON(503, gin.H{"status": "unhealthy"})
			return
		}
		c.JSON(200, gin.H{"status": "ok"})
	})
	router.GET("/ready", func(c *gin.Context) {
		if err := mtxClient.HealthCheck(); err != nil {
			logger.Errorf("ready: mediamtx: %v", err)
			c.JSON(503, gin.H{"status": "not ready"})
			return
		}
		if sqlDB, err := db.DB(); err == nil {
			if err := sqlDB.Ping(); err != nil {
				logger.Errorf("ready: db ping: %v", err)
				c.JSON(503, gin.H{"status": "not ready"})
				return
			}
			stats := sqlDB.Stats()
			if stats.Idle == 0 && stats.OpenConnections == stats.MaxOpenConnections {
				logger.Errorf("ready: db connection pool exhausted")
				c.JSON(503, gin.H{"status": "not ready"})
				return
			}
		} else {
			logger.Errorf("ready: db handle: %v", err)
			c.JSON(503, gin.H{"status": "not ready"})
			return
		}
		c.JSON(200, gin.H{"status": "ready"})
	})

	// Swagger API documentation
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// MediaMTX segment-complete hook (fast ingestion path). Registered
	// OUTSIDE the tenant middleware: MediaMTX sends no tenant headers; the
	// tenant is derived from the camera owning the path.
	router.POST("/internal/segments/complete",
		recording.NewHookHandler(recScanner).HandleSegmentComplete)

	// Self-test only: serve recorded .mp4 files bypassing tenant middleware so
	// the playback URL returned by Get() is directly openable in a browser
	// (Chrome <video> + HTTP Range). Production serves via the integrated
	// deployment layer (Java NMS backend / its nginx), not aiovms.
	recHandler := recording.NewHandler(recSvc, cfg.Recording.Path)
	recording.RegisterPublicRoutes(router, recHandler)

	// Prometheus metrics（公开端点，无需租户头）
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	api := router.Group("/")
	api.Use(middleware.TenantHeaderMiddleware())
	{
		camera.RegisterRoutes(api, camera.NewHandler(cameraSvc))
		recording.RegisterRoutes(api, recHandler)
		schedule.RegisterRoutes(api, schedule.NewHandler(schSvc))
	}

	// 12. Start server with graceful shutdown
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	logger.Infof("starting AIO VMS server on %s", addr)

	srv := &http.Server{Addr: addr, Handler: router}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("server failed: %v", err)
		}
	}()

	<-quit
	logger.Info("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Errorf("server shutdown: %v", err)
	}

	reconciler.Stop() // stop enqueueing new commands first
	recScanner.Stop()
	retention.Stop()
	actuator.Stop() // then drain/stop the writer

	wg.Wait()
	logger.Info("server stopped")
}
