package main

import (
	"fmt"
	"log"
	"time"
	"os"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/auth"
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

	collector := stats.NewCollector(database, 1024)

	aggregator := stats.NewAggregator(database, cfg.UsageSync)
	aggregator.Start()

	lb := proxy.NewLoadBalancer(cfg.Backends)
	proxyH := proxy.NewHandler(lb, collector)
	lb.ValidateBackends()

	authH := handler.NewAuthHandler(database, codeStore, &cfg.Auth)
	keyH := handler.NewAPIKeyHandler(database, keyStore)
	userH := handler.NewUserHandler(database)
	statsH := handler.NewStatsHandler(database)
	appH := handler.NewApplicationHandler(database)

	apiAuth := r.Group("/api/auth")
	apiAuth.Use(middleware.RateLimit(10, time.Minute))
	{
		apiAuth.POST("/send-code", authH.SendCode)
		apiAuth.POST("/login", authH.Login)
		apiAuth.POST("/logout", authH.Logout)
	}

	v1 := r.Group("/v1")
	v1.Use(middleware.AuthMiddleware(keyStore))
	{
		v1.POST("/chat/completions", proxyH.ChatCompletions)
		v1.POST("/messages", proxyH.Messages)
		v1.GET("/models", proxyH.Models)
	}

	// User API routes (session auth for web console)
	apiUser := r.Group("/api")
	apiUser.Use(middleware.SessionAuthMiddleware())
	{
		apiUser.GET("/keys", keyH.ListKeys)
		apiUser.POST("/keys", keyH.CreateKey)
		apiUser.PUT("/keys/:id/disable", keyH.DisableKey)
		apiUser.PUT("/keys/:id/enable", keyH.EnableKey)
		apiUser.DELETE("/keys/:id", keyH.DeleteKey)
		apiUser.GET("/usage", statsH.GetMyUsage)
		apiUser.GET("/usage/daily", statsH.GetMyDailyStats)
		apiUser.POST("/applications", appH.Submit)
		apiUser.GET("/applications", appH.ListMine)
	}

	adminAPI := r.Group("/admin/api")
	adminAPI.Use(middleware.SessionAuthMiddleware())
	adminAPI.Use(middleware.AdminRequired())
	{
		adminAPI.GET("/users", userH.ListUsers)
		adminAPI.GET("/users/:id", userH.GetUser)
		adminAPI.POST("/users", userH.CreateUser)
		adminAPI.PUT("/users/:id", userH.UpdateUser)
		adminAPI.GET("/usage", statsH.GetUsage)
		adminAPI.GET("/usage/daily", statsH.GetDailyStats)
		adminAPI.GET("/backends/stats", statsH.GetBackendStats)
		adminAPI.GET("/applications", appH.ListAll)
		adminAPI.PUT("/applications/:id/review", appH.Review)
	}

	// Serve frontend static files
	r.Static("/assets", "web/dist/assets")
	r.StaticFile("/favicon.ico", "web/dist/favicon.ico")
	r.NoRoute(func(c *gin.Context) {
		c.File("web/dist/index.html")
	})

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
