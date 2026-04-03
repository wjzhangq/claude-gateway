package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/awsproxy"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/handler"
	"github.com/wjzhangq/claude-gateway/internal/logger"
	"github.com/wjzhangq/claude-gateway/internal/middleware"
	"github.com/wjzhangq/claude-gateway/internal/model"
	"github.com/wjzhangq/claude-gateway/internal/proxy"
	"github.com/wjzhangq/claude-gateway/internal/stats"
)

func main() {
	cfgPath := "config/config.yaml"
	if v := os.Getenv("CONFIG_PATH"); v != "" {
		cfgPath = v
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	logger.Init(cfg.Log.Level, cfg.Log.Format)

	if err := os.MkdirAll("data", 0755); err != nil {
		logger.Fatalf("create data dir: %v", err)
	}

	database, err := db.Init(cfg.Database.Path)
	if err != nil {
		logger.Fatalf("failed to init database: %v", err)
	}
	defer database.Close()

	if cfg.Auth.AdminItcode != "" {
		if err := database.EnsureAdmin(cfg.Auth.AdminItcode); err != nil {
			logger.Warnf("ensure admin: %v", err)
		}
	}

	keyStore := auth.NewKeyStore()
	if err := loadKeyStore(database, keyStore); err != nil {
		logger.Fatalf("load key store: %v", err)
	}

	codeStore := auth.NewCodeStore(cfg.Auth.CodeExpiry)

	if cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.RequestLogger())

	store := cookie.NewStore([]byte(cfg.Auth.SessionSecret))
	r.Use(sessions.Sessions("gateway_session", store))
	r.Use(sessionLoader())

	collector := stats.NewCollector(database, keyStore, 1024)

	// Flush last_used_at from memory to DB every minute
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			keyTimes := keyStore.FlushLastUsed()
			if len(keyTimes) > 0 {
				if err := database.BatchUpdateKeyLastUsedAt(keyTimes); err != nil {
					logger.Errorf("flush last_used_at: %v", err)
				}
			}
		}
	}()

	aggregator := stats.NewAggregator(database, cfg.UsageSync)
	aggregator.Start()

	lb := proxy.NewLoadBalancer(cfg.Backends)
	proxyH := proxy.NewHandler(lb, collector, keyStore, cfg, cfg.ModelReplacements)
	lb.ValidateBackends()

	// ─── AWS Bedrock initialization ───────────────────────────────────
	var awsProxyH *awsproxy.Handler
	var awsCollector *stats.AWSCollector
	var awsAggregator *stats.AWSAggregator

	if cfg.AWS.Region != "" && cfg.AWS.AccessKeyID != "" {
		bedrockClient, err := awsproxy.NewBedrockClient(
			cfg.AWS.Region, cfg.AWS.AccessKeyID, cfg.AWS.SecretAccessKey,
		)
		if err != nil {
			logger.Fatalf("init bedrock client: %v", err)
		}
		awsCollector = stats.NewAWSCollector(database, keyStore, 1024)
		awsProxyH = awsproxy.NewHandler(bedrockClient, awsCollector, keyStore, &cfg.AWS)
		awsAggregator = stats.NewAWSAggregator(database, cfg.UsageSync)
		awsAggregator.Start()
		logger.Infof("AWS Bedrock channel initialized (region: %s)", cfg.AWS.Region)
	} else {
		logger.Infof("AWS Bedrock channel not configured, skipping initialization")
	}

	authH := handler.NewAuthHandler(database, codeStore, &cfg.Auth)
	keyH := handler.NewAPIKeyHandler(database, keyStore)
	userH := handler.NewUserHandler(database, keyStore)
	statsH := handler.NewStatsHandler(database, cfg)
	appH := handler.NewApplicationHandler(database, keyStore)
	awsStatsH := handler.NewAWSStatsHandler(database, cfg)

	apiAuth := r.Group("/api/auth")
	apiAuth.Use(middleware.RateLimit(10, time.Minute))
	{
		apiAuth.POST("/send-code", authH.SendCode)
		apiAuth.POST("/login", authH.Login)
		apiAuth.POST("/logout", authH.Logout)
	}

	// /v1/* — dispatch by channel attribute of the API key
	v1 := r.Group("/v1")
	v1.Use(middleware.AuthMiddleware(keyStore))
	{
		v1.Any("/*path", func(c *gin.Context) {
			ch, _ := c.Get("channel")
			if ch == "aws" && awsProxyH != nil {
				awsProxyH.Passthrough(c)
			} else if ch == "aws" {
				c.JSON(503, gin.H{"error": "AWS channel not configured"})
			} else {
				proxyH.Passthrough(c)
			}
		})
	}

	// Public API (no auth required)
	r.POST("/api/check_key", keyH.CheckKey)

	// User API routes (session auth)
	apiUser := r.Group("/api")
	apiUser.Use(middleware.SessionAuthMiddleware())
	{
		apiUser.GET("/keys", keyH.ListKeys)
		apiUser.POST("/keys", keyH.CreateKey)
		apiUser.PUT("/keys/:id/disable", keyH.DisableKey)
		apiUser.PUT("/keys/:id/enable", keyH.EnableKey)
		apiUser.PUT("/keys/:id/auto-downgrade", keyH.SetAutoDowngrade)
		apiUser.PUT("/keys/:id/rename", keyH.RenameKey)
		apiUser.PUT("/keys/:id/channel", keyH.SwitchChannel) // channel migration
		apiUser.DELETE("/keys/:id", keyH.DeleteKey)
		apiUser.GET("/usage", statsH.GetMyUsage)
		apiUser.GET("/usage/daily", statsH.GetMyDailyStats)
		apiUser.POST("/applications", appH.Submit)
		apiUser.GET("/applications", appH.ListMine)
	}

	// User AWS API (requires aws_enabled)
	apiAWS := r.Group("/api/aws")
	apiAWS.Use(middleware.SessionAuthMiddleware())
	apiAWS.Use(middleware.AWSEnabledRequired(database))
	{
		apiAWS.GET("/dashboard", awsStatsH.GetMyDashboard)
		apiAWS.GET("/keys", keyH.ListAWSKeys)
		apiAWS.POST("/keys", keyH.CreateAWSKey)
		apiAWS.GET("/usage", awsStatsH.GetMyUsage)
		apiAWS.GET("/usage/daily", awsStatsH.GetMyDailyStats)
	}

	adminAPI := r.Group("/admin/api")
	adminAPI.Use(middleware.SessionAuthMiddleware())
	adminAPI.Use(middleware.AdminRequired())
	{
		adminAPI.GET("/users", userH.ListUsers)
		adminAPI.GET("/users/:id", userH.GetUser)
		adminAPI.POST("/users", userH.CreateUser)
		adminAPI.PUT("/users/:id", userH.UpdateUser)
		adminAPI.PUT("/users/:id/itcode", userH.UpdateItcode)
		adminAPI.GET("/keys", keyH.AdminListKeys)
		adminAPI.PUT("/keys/:id/rename", keyH.RenameKey)
		adminAPI.PUT("/keys/:id/transfer", keyH.TransferKey)
		adminAPI.GET("/usage", statsH.GetUsage)
		adminAPI.GET("/usage/daily", statsH.GetDailyStats)
		adminAPI.GET("/usage/user-daily", statsH.GetUserDailyCostRanking)
		adminAPI.GET("/backends/stats", statsH.GetBackendStats)
		adminAPI.GET("/backends/status", func(c *gin.Context) {
			c.JSON(200, lb.GetBackends())
		})
		adminAPI.GET("/groups", statsH.GetGroups)
		adminAPI.GET("/groups/stats", statsH.GetGroupStats)
		adminAPI.GET("/applications", appH.ListAll)
		adminAPI.PUT("/applications/:id/review", appH.Review)
	}

	// Admin AWS API
	adminAWS := r.Group("/admin/api/aws")
	adminAWS.Use(middleware.SessionAuthMiddleware())
	adminAWS.Use(middleware.AdminRequired())
	{
		adminAWS.GET("/users", awsStatsH.ListAWSUsers)
		adminAWS.PUT("/users/:id/toggle", awsStatsH.ToggleAWSEnabled)
		adminAWS.POST("/users/enable", awsStatsH.EnableAWSByItcode)
		adminAWS.GET("/keys", keyH.AdminListAWSKeys)
		adminAWS.GET("/usage", awsStatsH.GetUsage)
		adminAWS.GET("/usage/daily", awsStatsH.GetDailyStats)
		adminAWS.GET("/usage/user-daily", awsStatsH.GetUserDailyCostRanking)
		adminAWS.GET("/bedrock/stats", awsStatsH.GetBedrockStats)
	}

	// Serve frontend static files
	r.Static("/assets", "web/dist/assets")
	r.StaticFile("/favicon.ico", "web/dist/favicon.ico")
	r.NoRoute(func(c *gin.Context) {
		c.File("web/dist/index.html")
	})

	// Register SIGHUP before starting server to avoid race
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	go handleReload(sigCh, cfgPath, collector, awsCollector, aggregator, awsAggregator,
		lb, proxyH, awsProxyH, statsH, awsStatsH, database, keyStore, cfg)

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	logger.Infof("Claude Gateway listening on %s", addr)
	if err := r.Run(addr); err != nil {
		logger.Fatalf("server error: %v", err)
	}
}

func loadKeyStore(database *db.DB, ks *auth.KeyStore) error {
	keys, err := database.ListAllActiveAPIKeys()
	if err != nil {
		return err
	}
	users, err := database.ListUsers()
	if err != nil {
		return err
	}
	userMap := make(map[int64]*model.User, len(users))
	for _, u := range users {
		userMap[u.ID] = u
	}
	apiKeys := make([]model.APIKey, len(keys))
	for i, k := range keys {
		apiKeys[i] = *k
	}
	logger.Infof("loadKeyStore: loading %d active keys, %d users", len(keys), len(users))
	ks.Load(apiKeys, userMap)
	return nil
}

func sessionLoader() gin.HandlerFunc {
	return func(c *gin.Context) {
		sess := sessions.Default(c)
		if uid := sess.Get("user_id"); uid != nil {
			c.Set("session_user_id", uid)
			c.Set(middleware.CtxUserID, uid)
		}
		if role := sess.Get("user_role"); role != nil {
			c.Set(middleware.CtxUserRole, role)
		}
		c.Next()
	}
}

func handleReload(sigCh <-chan os.Signal, cfgPath string,
	collector *stats.Collector, awsCollector *stats.AWSCollector,
	aggregator *stats.Aggregator, awsAggregator *stats.AWSAggregator,
	lb *proxy.LoadBalancer, proxyH *proxy.Handler, awsProxyH *awsproxy.Handler,
	statsH *handler.StatsHandler, awsStatsH *handler.AWSStatsHandler,
	database *db.DB, keyStore *auth.KeyStore, currentCfg *config.Config) {

	for range sigCh {
		logger.Infof("received SIGHUP, starting reload...")

		// Step 1: flush pending usage records
		logger.Infof("reload: flushing collector...")
		collector.Flush()
		if awsCollector != nil {
			awsCollector.Flush()
		}

		// Step 2: aggregate daily stats
		logger.Infof("reload: aggregating daily stats...")
		aggregator.RunNow()
		if awsAggregator != nil {
			awsAggregator.RunNow()
		}

		// Step 3: reload config file
		logger.Infof("reload: loading config from %s", cfgPath)
		newCfg, err := config.Load(cfgPath)
		if err != nil {
			logger.Errorf("reload: failed to load config: %v — keeping old config", err)
			continue
		}

		// Step 4: apply new config
		lb.UpdateBackends(newCfg.Backends)
		proxyH.UpdateModelReplacements(newCfg.ModelReplacements)
		proxyH.UpdateConfig(newCfg)
		statsH.UpdateConfig(newCfg)
		awsStatsH.UpdateConfig(newCfg)
		if awsProxyH != nil {
			awsProxyH.UpdateConfig(&newCfg.AWS)
		}

		// Step 5: reload key store
		if err := loadKeyStore(database, keyStore); err != nil {
			logger.Errorf("reload: failed to reload key store: %v", err)
		}

		*currentCfg = *newCfg
		logger.Infof("reload: completed successfully")
	}
}
