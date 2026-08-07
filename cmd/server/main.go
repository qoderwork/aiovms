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
	_ "aiovms/pkg/metrics" // register prometheus metrics
	"aiovms/pkg/logger"
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

	// 8. Bootstrap layers
	camRepo := camera.NewRepository(db)
	cameraSvc := camera.NewService(camRepo, mtxClient, cfg.Recording.Path, cfg.Recording.SegmentDuration)

	recRepo := recording.NewRepository(db)
	recSvc := recording.NewService(recRepo, cameraSvc, mtxClient)

	schRepo := schedule.NewRepository(db)
	schSvc := schedule.NewService(schRepo)

	// 9. Start unified reconciler (replaces startup sync, cron triggerJob,
	//    event-driven MTX recovery, and camera status checker).
	reconciler := controller.NewReconciler(camRepo, schRepo, recRepo, mtxClient,
		cfg.Recording.Path, cfg.Recording.SegmentDuration)

	// 10. Start background workers
	var wg sync.WaitGroup

	wg.Add(1)
	go func() { defer wg.Done(); reconciler.Run() }()

	recScanner := recording.NewScanner(recSvc, cfg.Recording.Path, camRepo)
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

	// Prometheus metrics（公开端点，无需租户头）
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	api := router.Group("/")
	api.Use(middleware.TenantHeaderMiddleware())
	{
		camera.RegisterRoutes(api, camera.NewHandler(cameraSvc))
		recording.RegisterRoutes(api, recording.NewHandler(recSvc))
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

	reconciler.Stop()
	recScanner.Stop()
	retention.Stop()

	wg.Wait()
	logger.Info("server stopped")
}
