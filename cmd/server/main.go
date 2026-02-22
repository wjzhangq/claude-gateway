package main

import (
	"fmt"
	"log"
	"os"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"

	"github.com/claude-gateway/claude-gateway/config"
	"github.com/claude-gateway/claude-gateway/internal/auth"
	"github.com/claude-gateway/claude-gateway/internal/db"
	"github.com/claude-gateway/claude-gateway/internal/handler"
	"github.com/claude-gateway/claude-gateway/internal/logger"
	"github.com/claude-gateway/claude-gateway/internal/middleware"
	"github.com/claude-gateway/claude-gateway/internal/model"
	"github.com/claude-gateway/claude-gateway/internal/proxy"
	"github.com/claude-gateway/claude-gateway/internal/stats"
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

	// Ensure data directory exists
	if err := os.MkdirAll("data", 0755); err != nil {
		logger.Fatalf("create data dir: %v", err)
	}

	database, err := db.Init(cfg.Database.Path)
	if err != nil {
		logger.Fatalf("failed to init database: %v", err)
	}
	defer database.Close()

	// Ensure admin user exists
	if cfg.Auth.AdminPhone != "" {
		if err := database.EnsureAdmin(cfg.Auth.AdminPhone); err != nil {
			logger.Warnf("ensure admin: %v", err)
		}
	}

	// Load all active API keys into memory
	keyStore := auth.NewKeyStore()
	if err := loadKeyStore(database, keyStore); err != nil {
		logger.Fatalf("load key store: %v", err)
	}

	codeStore := auth.NewCodeStore(cfg.Auth.CodeExpiry)

	// Gin setup
	if cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.RequestLogger())

	// Session middleware
	store := cookie.NewStore([]byte(cfg.Auth.SessionSecret))
	r.Use(sessions.Sessions("gateway_session", store))

	// Session loader middleware (populate ctx from session)
	r.Use(sessionLoader())

	// Stats collector (async, buffered channel)
	collector := stats.NewCollector(database, 1024)

	// Load balancer + proxy handler
	lb := proxy.NewLoadBalancer(cfg.Backends)
	proxyH := proxy.NewHandler(lb, collector)

	// Handlers
	authH := handler.NewAuthHandler(database, codeStore)
	keyH := handler.NewAPIKeyHandler(database, keyStore)
	userH := handler.NewUserHandler(database)

	// Public auth routes
	apiAuth := r.Group("/api/auth")
	{
		apiAuth.POST("/send-code", authH.SendCode)
		apiAuth.POST("/login", authH.Login)
		apiAuth.POST("/logout", authH.Logout)
	}

	// Proxy routes (API key auth) - OpenAI + Anthropic style
	v1 := r.Group("/v1")
	v1.Use(middleware.AuthMiddleware(keyStore))
	{
		v1.POST("/chat/completions", proxyH.ChatCompletions)
		v1.POST("/messages", proxyH.Messages)
		v1.GET("/models", proxyH.Models)
	}

	// User API routes (API key auth)
	apiUser := r.Group("/api")
	apiUser.Use(middleware.AuthMiddleware(keyStore))
	{
		apiUser.GET("/keys", keyH.ListKeys)
		apiUser.POST("/keys", keyH.CreateKey)
		apiUser.PUT("/keys/:id/disable", keyH.DisableKey)
		apiUser.PUT("/keys/:id/enable", keyH.EnableKey)
		apiUser.DELETE("/keys/:id", keyH.DeleteKey)
	}

	// Admin routes (session auth)
	adminAPI := r.Group("/admin/api")
	adminAPI.Use(middleware.SessionAuthMiddleware())
	adminAPI.Use(middleware.AdminRequired())
	{
		adminAPI.GET("/users", userH.ListUsers)
		adminAPI.GET("/users/:id", userH.GetUser)
		adminAPI.POST("/users", userH.CreateUser)
		adminAPI.PUT("/users/:id", userH.UpdateUser)
	}

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

// sessionLoader reads session values and sets them in gin context.
func sessionLoader() gin.HandlerFunc {
	return func(c *gin.Context) {
		sess := sessions.Default(c)
		if uid := sess.Get("user_id"); uid != nil {
			c.Set("session_user_id", uid)
		}
		if role := sess.Get("user_role"); role != nil {
			c.Set(middleware.CtxUserRole, role)
		}
		c.Next()
	}
}
