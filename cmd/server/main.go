package main

import (
	"fmt"
	"log"

	"github.com/claude-gateway/claude-gateway/config"
	"github.com/claude-gateway/claude-gateway/internal/db"
)

func main() {
	cfg, err := config.Load("config/config.yaml")
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	database, err := db.Init(cfg.Database.Path)
	if err != nil {
		log.Fatalf("failed to init database: %v", err)
	}
	defer database.Close()

	fmt.Printf("Claude Gateway starting on :%d\n", cfg.Server.Port)
	// TODO: start HTTP server
}
