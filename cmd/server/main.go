package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
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
	"github.com/wjzhangq/claude-gateway/internal/perftest"
	"github.com/wjzhangq/claude-gateway/internal/proxy"
	"github.com/wjzhangq/claude-gateway/internal/publicproxy"
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
	logger.InitErrorLog(cfg.Log.Dir)
	logger.InitBackendLog(cfg.Log.Dir)

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

	// Seed today's backend daily costs from daily_stats (for quota enforcement after restart)
	today := time.Now().Format("2006-01-02")
	if dailyCosts, err := database.GetUserDailyCostByDate(today); err != nil {
		logger.Warnf("init daily costs: %v", err)
	} else {
		keyStore.InitDailyCosts(today, dailyCosts)
		logger.Infof("init daily costs: loaded %d user records for %s", len(dailyCosts), today)
	}

	// Seed today's AWS daily costs from aws_usage_logs
	if awsDailyCosts, err := database.GetUserAWSDailyCostByDate(today); err != nil {
		logger.Warnf("init aws daily costs: %v", err)
	} else {
		keyStore.InitAWSDailyCosts(today, awsDailyCosts)
		logger.Infof("init aws daily costs: loaded %d user records for %s", len(awsDailyCosts), today)
	}

	// Seed this month's AWS monthly costs from aws_usage_logs (fixes quota bypass after restart)
	thisMonth := time.Now().Format("2006-01")
	if awsMonthlyCosts, err := database.GetUserAWSMonthlyCostByMonth(thisMonth); err != nil {
		logger.Warnf("init aws monthly costs: %v", err)
	} else {
		keyStore.InitAWSMonthlyCosts(thisMonth, awsMonthlyCosts)
		logger.Infof("init aws monthly costs: loaded %d user records for %s", len(awsMonthlyCosts), thisMonth)
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

	// Flush last_used_at and key costs from memory to DB every minute
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			// Flush last_used_at
			keyTimes := keyStore.FlushLastUsed()
			if len(keyTimes) > 0 {
				if err := database.BatchUpdateKeyLastUsedAt(keyTimes); err != nil {
					logger.Errorf("flush last_used_at: %v", err)
				}
			}
			// Flush accumulated key costs (backend + aws)
			costUpdates := keyStore.FlushCosts()
			if len(costUpdates) > 0 {
				dbUpdates := make([]db.KeyCostUpdate, len(costUpdates))
				for i, u := range costUpdates {
					dbUpdates[i] = db.KeyCostUpdate{
						KeyID:          u.KeyID,
						BackendCostAdd: u.BackendCostAdd,
						AWSCostAdd:     u.AWSCostAdd,
					}
				}
				if err := database.BatchUpdateKeyCosts(dbUpdates); err != nil {
					logger.Errorf("flush key costs: %v", err)
				}
			}
		}
	}()

	aggregator := stats.NewAggregator(database, cfg.UsageSync)
	aggregator.Start()

	// Reset daily cost counters at midnight
	go func() {
		for {
			// Calculate duration until next midnight
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 5, 0, now.Location())
			timer := time.NewTimer(time.Until(next))
			<-timer.C
			keyStore.ResetDailyCosts()
			logger.Infof("daily cost counters reset for new day: %s", time.Now().Format("2006-01-02"))
		}
	}()

	lb := proxy.NewLoadBalancer(cfg.Backends)
	proxyH := proxy.NewHandler(lb, collector, keyStore, cfg, cfg.ModelReplacements)
	if cfg.ValidateBackends {
		lb.ValidateBackends()
	}

	// ─── AWS Bedrock initialization ───────────────────────────────────
	var awsProxyH *awsproxy.Handler
	var awsCollector *stats.AWSCollector
	var awsAggregator *stats.AWSAggregator

	if cfg.AWS.Region != "" && cfg.AWS.AccessKeyID != "" {
		bedrockClient, err := awsproxy.NewBedrockClient(
			cfg.AWS.Region, cfg.AWS.AccessKeyID, cfg.AWS.SecretAccessKey, cfg.AWS.Socks5Proxy,
		)
		if err != nil {
			logger.Fatalf("init bedrock client: %v", err)
		}
		awsCollector = stats.NewAWSCollector(database, keyStore, 1024)
		awsProxyH = awsproxy.NewHandler(bedrockClient, awsCollector, keyStore, &cfg.AWS)
		awsProxyH.SetRootConfig(cfg)
		awsAggregator = stats.NewAWSAggregator(database, cfg.UsageSync)
		awsAggregator.Start()
		if cfg.AWS.Socks5Proxy != "" {
			logger.Infof("AWS Bedrock channel initialized (region: %s, proxy: %s)", cfg.AWS.Region, cfg.AWS.Socks5Proxy)
		} else {
			logger.Infof("AWS Bedrock channel initialized (region: %s, proxy: none)", cfg.AWS.Region)
		}
	} else {
		logger.Infof("AWS Bedrock channel not configured, skipping initialization")
	}

	// ─── Public providers initialization ─────────────────────────────
	publicH := publicproxy.NewHandler(collector, keyStore, cfg)
	if len(cfg.PublicProviders) > 0 {
		var names []string
		for _, p := range cfg.PublicProviders {
			if p.Enabled {
				names = append(names, p.Name)
			}
		}
		if len(names) > 0 {
			logger.Infof("Public providers initialized: %s", strings.Join(names, ", "))
		}
	}

	authH := handler.NewAuthHandler(database, codeStore, &cfg.Auth)
	keyH := handler.NewAPIKeyHandler(database, keyStore)
	userH := handler.NewUserHandler(database, keyStore)
	statsH := handler.NewStatsHandler(database, cfg, keyStore)
	appH := handler.NewApplicationHandler(database, keyStore)
	awsStatsH := handler.NewAWSStatsHandler(database, cfg, keyStore)
	dbExplorerH := handler.NewDBExplorerHandler(database, keyStore)
	insightH := handler.NewInsightHandler(database, cfg)
	configH := handler.NewConfigHandler(cfgPath, cfg, func() error {
		newCfg, err := config.Load(cfgPath)
		if err != nil {
			return err
		}
		lb.UpdateBackends(newCfg.Backends)
		proxyH.UpdateModelReplacements(newCfg.ModelReplacements)
		proxyH.UpdateConfig(newCfg)
		publicH.UpdateConfig(newCfg)
		statsH.UpdateConfig(newCfg)
		awsStatsH.UpdateConfig(newCfg)
		insightH.UpdateConfig(newCfg)
		if awsProxyH != nil {
			awsProxyH.UpdateConfig(&newCfg.AWS)
			awsProxyH.SetRootConfig(newCfg)
		}
		*cfg = *newCfg
		return nil
	})

	// ─── Performance test initialization ─────────────────────────────
	var bedrockInvoker perftest.BedrockInvoker
	if awsProxyH != nil {
		bedrockInvoker = awsProxyH.GetBedrockClient()
	}
	perfRunner := perftest.NewRunner(lb, bedrockInvoker, cfg)
	perfTestH := handler.NewPerfTestHandler(database, perfRunner, cfg, lb)

	apiAuth := r.Group("/api/auth")
	{
		apiAuth.POST("/send-code", authH.SendCode)
		apiAuth.POST("/login", authH.Login)
		apiAuth.POST("/logout", authH.Logout)
	}

	// /v1/* — dispatch by channel attribute of the API key
	// Priority: public provider (model match) > aws/backend (channel)
	v1 := r.Group("/v1")
	v1.Use(middleware.AuthMiddleware(keyStore))
	{
		v1.Any("/*path", func(c *gin.Context) {
			ch, _ := c.Get("channel")
			keyPrefix, _ := c.Get("raw_api_key")
			path := "/v1" + c.Param("path")
			logger.Infof("v1 request: path=%s key=%s channel=%v", c.Param("path"), keyPrefix, ch)

			// For /models endpoint, return public models alongside channel models
			if strings.HasSuffix(path, "/models") && c.Request.Method == "GET" {
				publicModels := publicH.Models()
				serveModelsWithPublic(c, ch, awsProxyH, proxyH, publicModels)
				return
			}

			// For mutation requests (POST), peek at the body to detect model
			if c.Request.Method == "POST" {
				body, err := io.ReadAll(c.Request.Body)
				if err != nil {
					c.JSON(400, gin.H{"error": "read request body failed"})
					return
				}

				var reqJSON struct {
					Model string `json:"model"`
				}
				if json.Unmarshal(body, &reqJSON) == nil && reqJSON.Model != "" {
					// Apply the key's locked model BEFORE public-provider routing,
					// so a locked model (e.g. a public Kimi model) can be matched and
					// routed to its provider. Without this, the lock only takes effect
					// inside the backend channel handler — too late to reach the public
					// provider routing below. ReplaceModelInBody is idempotent.
					if ki, ok := c.Get(middleware.CtxKeyInfo); ok {
						if info, ok := ki.(*auth.KeyInfo); ok && info.LockedModel != "" && info.LockedModel != reqJSON.Model {
							logger.Infof("applying locked model %s (was %s) before routing", info.LockedModel, reqJSON.Model)
							body = proxy.ReplaceModelInBody(body, reqJSON.Model, info.LockedModel)
							reqJSON.Model = info.LockedModel
						}
					}

					if provider := publicH.MatchModel(reqJSON.Model); provider != nil {
						logger.Infof("routing to public provider %s for model %s", provider.Name, reqJSON.Model)
						publicH.Forward(c, path, body, reqJSON.Model, provider)
						return
					}
				}

				// Restore body for the downstream channel handler
				c.Request.Body = io.NopCloser(bytes.NewReader(body))
			}

			// Fall through to original channel-based routing
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

	// External API (API key auth)
	apiExternal := r.Group("/api/v1")
	apiExternal.Use(middleware.AuthMiddleware(keyStore))
	{
		apiExternal.GET("/quota", statsH.GetMyQuota)
	}

	// User API routes (session auth)
	apiUser := r.Group("/api")
	apiUser.Use(middleware.SessionAuthMiddleware())
	{
		apiUser.GET("/me", authH.Me)
		apiUser.GET("/dashboard", statsH.GetMyDashboard)
		apiUser.GET("/keys", keyH.ListKeys)
		apiUser.POST("/keys", keyH.CreateKey)
		apiUser.PUT("/keys/:id/disable", keyH.DisableKey)
		apiUser.PUT("/keys/:id/enable", keyH.EnableKey)
		apiUser.PUT("/keys/:id/auto-downgrade", keyH.SetAutoDowngrade)
		apiUser.PUT("/keys/:id/rename", keyH.RenameKey)
		apiUser.PUT("/keys/:id/channel", keyH.SwitchChannel) // channel migration
		apiUser.PUT("/keys/:id/locked-model", keyH.SetLockedModel)
		apiUser.DELETE("/keys/:id", keyH.DeleteKey)
		apiUser.GET("/usage", statsH.GetMyUsage)
		apiUser.GET("/usage/daily", statsH.GetMyDailyStats)
		apiUser.GET("/usage/export", statsH.ExportMyUsage)
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
		adminAPI.GET("/users/search", userH.SearchUsers)
		adminAPI.GET("/users/:id", userH.GetUser)
		adminAPI.POST("/users", userH.CreateUser)
		adminAPI.PUT("/users/:id", userH.UpdateUser)
		adminAPI.PUT("/users/:id/itcode", userH.UpdateItcode)
		adminAPI.GET("/keys", keyH.AdminListKeys)
		adminAPI.POST("/keys", keyH.AdminCreateKey)
		adminAPI.PUT("/keys/:id/rename", keyH.RenameKey)
		adminAPI.PUT("/keys/:id/channel", keyH.AdminSwitchChannel)
		adminAPI.PUT("/keys/:id/transfer", keyH.TransferKey)
		adminAPI.PUT("/keys/:id/locked-model", keyH.AdminSetLockedModel)
		adminAPI.GET("/usage", statsH.GetUsage)
		adminAPI.GET("/usage/export", statsH.ExportUsage)
		adminAPI.GET("/usage/daily", statsH.GetDailyStats)
		adminAPI.GET("/usage/user-daily", statsH.GetUserDailyCostRanking)
		adminAPI.GET("/backends/stats", statsH.GetBackendStats)
		adminAPI.GET("/backends/status", func(c *gin.Context) {
			backends := lb.GetBackends()
			for i := range backends {
				backends[i].DailyLimit = cfg.LookupBackendDailyLimit(backends[i].Name)
			}
			c.JSON(200, backends)
		})
		adminAPI.GET("/groups", statsH.GetGroups)
		adminAPI.GET("/groups/stats", statsH.GetGroupStats)
		adminAPI.GET("/applications", appH.ListAll)
		adminAPI.PUT("/applications/:id/review", appH.Review)
		adminAPI.GET("/config/limits", configH.GetLimits)
		adminAPI.PUT("/config/limits", configH.UpdateLimits)
		adminAPI.POST("/perftest/run", perfTestH.StartRun)
		adminAPI.GET("/perftest/run/:id", perfTestH.GetRun)
		adminAPI.GET("/perftest/run/:id/stream", perfTestH.StreamProgress)
		adminAPI.GET("/perftest/runs", perfTestH.ListRuns)
		adminAPI.GET("/perftest/options", perfTestH.GetOptions)
		adminAPI.DELETE("/perftest/run/:id", perfTestH.CancelRun)

		// Insight: usage ranking, per-user report, org tagging (migrated from sky-insight)
		adminAPI.GET("/insight/ranking", insightH.GetRanking)
		adminAPI.GET("/insight/user/:id", insightH.GetUserInsight)
		adminAPI.GET("/insight/org", insightH.GetOrgList)
		adminAPI.PUT("/insight/org/:id", insightH.UpdateOrgTag)
		adminAPI.POST("/insight/org/batch", insightH.BatchUpdateOrgTags)
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
		adminAWS.GET("/usage/user-monthly", awsStatsH.GetUserMonthlyCostRanking)
		adminAWS.GET("/bedrock/stats", awsStatsH.GetBedrockStats)
	}

	// DB Explorer API (admin API key auth)
	dbAPI := r.Group("/admin/api/db")
	dbAPI.Use(dbExplorerH.AdminAPIKeyAuth())
	{
		dbAPI.GET("/schema", dbExplorerH.GetSchema)
		dbAPI.POST("/query", dbExplorerH.ExecuteQuery)
	}

	// Backend quota sync API (session_secret auth, called by cmd/check)
	r.POST("/admin/api/backends/quota", func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		raw = strings.TrimPrefix(raw, "Bearer ")
		raw = strings.TrimPrefix(raw, "bearer ")
		if raw == "" || raw != cfg.Auth.SessionSecret {
			c.JSON(401, gin.H{"error": "unauthorized"})
			return
		}

		var req struct {
			Backends []struct {
				Name      string  `json:"name"`
				Exhausted bool    `json:"exhausted"`
				Limit     float64 `json:"limit"`
				Usage     float64 `json:"usage"`
			} `json:"backends"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		results := make([]gin.H, 0, len(req.Backends))
		for _, b := range req.Backends {
			found := lb.SetQuotaStatus(b.Name, b.Exhausted, b.Limit, b.Usage)
			results = append(results, gin.H{"name": b.Name, "found": found, "exhausted": b.Exhausted})
		}
		c.JSON(200, gin.H{"updated": results})
	})

	// Backend health sync API (session_secret auth, called by cmd/check --health)
	r.POST("/admin/api/backends/health", func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		raw = strings.TrimPrefix(raw, "Bearer ")
		raw = strings.TrimPrefix(raw, "bearer ")
		if raw == "" || raw != cfg.Auth.SessionSecret {
			c.JSON(401, gin.H{"error": "unauthorized"})
			return
		}

		var req struct {
			Backends []struct {
				Name      string `json:"name"`
				Healthy   bool   `json:"healthy"`
				LatencyMs int64  `json:"latency_ms"`
				Error     string `json:"error"`
			} `json:"backends"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		results := make([]gin.H, 0, len(req.Backends))
		for _, b := range req.Backends {
			found := lb.SetHealthStatus(b.Name, b.Healthy, b.LatencyMs)
			results = append(results, gin.H{"name": b.Name, "found": found, "healthy": b.Healthy})
		}
		c.JSON(200, gin.H{"updated": results})
	})

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
		lb, proxyH, awsProxyH, publicH, statsH, awsStatsH, insightH, database, keyStore, cfg)

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

// serveModelsWithPublic handles GET /v1/models by injecting public models into context
// before delegating to the channel's handler. Both AWS and backend handlers check for
// "extra_models" in the gin context and append them to the response.
func serveModelsWithPublic(c *gin.Context, ch interface{}, awsProxyH *awsproxy.Handler, proxyH *proxy.Handler, publicModels []gin.H) {
	// Inject public models into context for handlers to pick up
	c.Set("extra_models", publicModels)

	if ch == "aws" && awsProxyH != nil {
		awsProxyH.Models(c)
	} else if ch == "aws" {
		c.JSON(503, gin.H{"error": "AWS channel not configured"})
	} else {
		proxyH.Models(c)
	}
}

func handleReload(sigCh <-chan os.Signal, cfgPath string,
	collector *stats.Collector, awsCollector *stats.AWSCollector,
	aggregator *stats.Aggregator, awsAggregator *stats.AWSAggregator,
	lb *proxy.LoadBalancer, proxyH *proxy.Handler, awsProxyH *awsproxy.Handler,
	publicH *publicproxy.Handler,
	statsH *handler.StatsHandler, awsStatsH *handler.AWSStatsHandler,
	insightH *handler.InsightHandler,
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
		publicH.UpdateConfig(newCfg)
		statsH.UpdateConfig(newCfg)
		awsStatsH.UpdateConfig(newCfg)
		insightH.UpdateConfig(newCfg)
		if awsProxyH != nil {
			awsProxyH.UpdateConfig(&newCfg.AWS)
			awsProxyH.SetRootConfig(newCfg)
		}

		// Step 5: reload key store
		if err := loadKeyStore(database, keyStore); err != nil {
			logger.Errorf("reload: failed to reload key store: %v", err)
		}

		*currentCfg = *newCfg
		logger.Infof("reload: completed successfully")
	}
}
