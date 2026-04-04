package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration structure.
type Config struct {
	Server            ServerConfig      `yaml:"server"`
	Database          DatabaseConfig    `yaml:"database"`
	Log               LogConfig         `yaml:"log"`
	Auth              AuthConfig        `yaml:"auth"`
	Groups            []Group           `yaml:"groups"`
	Backends          []BackendAPI      `yaml:"backends"`
	UsageSync         time.Duration     `yaml:"usage_sync_time"`
	ModelReplacements map[string]string `yaml:"model_replacements"`
	DowngradedTTL     time.Duration     `yaml:"downgraded_ttl"` // how long to skip original model after downgrade
	AWS               AWSConfig         `yaml:"aws"`
}

// Group represents a user group for organizing users and tracking usage.
type Group struct {
	ID   int    `yaml:"id" json:"id"`
	Name string `yaml:"name" json:"name"`
}

type ServerConfig struct {
	Port int    `yaml:"port"`
	Mode string `yaml:"mode"` // debug | release
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type LogConfig struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // json | text
}

type AuthConfig struct {
	SessionSecret  string        `yaml:"session_secret"`
	SessionMaxAge  int           `yaml:"session_max_age"` // seconds
	CodeExpiry     time.Duration `yaml:"code_expiry"`     // verification code TTL
	AdminItcode    string        `yaml:"admin_itcode"`
	SendCodeURL    string        `yaml:"send_code_url"`
	InviteCode     string        `yaml:"invite_code"`
}

// BackendAPI represents a single upstream Claude API endpoint.
type BackendAPI struct {
	Name    string `yaml:"name"`
	URL     string `yaml:"url"`
	APIKey  string `yaml:"api_key"`
	Weight  int    `yaml:"weight"`
	Enabled bool   `yaml:"enabled"`
}

// AWSConfig holds all AWS Bedrock channel configuration.
type AWSConfig struct {
	Region          string                       `yaml:"region"`
	AccessKeyID     string                       `yaml:"access_key_id"`
	SecretAccessKey string                       `yaml:"secret_access_key"`
	CacheEnabled    int                          `yaml:"cache_enabled"`
	CacheTTL        time.Duration                `yaml:"cache_ttl"`
	Socks5Proxy     string                       `yaml:"socks5"` // optional socks5 proxy, e.g. socks5://user:pass@host:port or user:pass@host:port
	ModelReplace    map[string]string            `yaml:"model_replace"` // exact: upstream name -> Bedrock ARN
	ModelDefault    map[string]string            `yaml:"model_default"` // glob pattern -> upstream name
	ModelPricing    map[string]ModelPricingEntry `yaml:"model_pricing"` // glob pattern -> pricing
}

// ModelPricingEntry holds per-model pricing in USD per 1M tokens.
type ModelPricingEntry struct {
	Input      float64 `yaml:"input"`
	Output     float64 `yaml:"output"`
	CacheRead  float64 `yaml:"cache_read"`
	CacheWrite float64 `yaml:"cache_write"`
}

// Load reads and parses the YAML config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := defaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

func defaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port: 8080,
			Mode: "release",
		},
		Database: DatabaseConfig{
			Path: "data/gateway.db",
		},
		Log: LogConfig{
			Level:  "info",
			Format: "json",
		},
		Auth: AuthConfig{
			SessionMaxAge: 86400,
			CodeExpiry:    5 * time.Minute,
		},
		UsageSync:     5 * time.Minute,
		DowngradedTTL: 60 * time.Second,
	}
}

func validate(cfg *Config) error {
	if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 1 and 65535")
	}
	if cfg.Auth.SessionSecret == "" {
		return fmt.Errorf("auth.session_secret is required")
	}
	if len(cfg.Backends) == 0 {
		return fmt.Errorf("at least one backend is required")
	}
	for i, b := range cfg.Backends {
		if b.URL == "" {
			return fmt.Errorf("backends[%d].url is required", i)
		}
		if b.APIKey == "" {
			return fmt.Errorf("backends[%d].api_key is required", i)
		}
		if b.Weight <= 0 {
			cfg.Backends[i].Weight = 1
		}
	}
	return nil
}
