package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// UserDailyLimit specifies a per-itcode daily spending override.
// These entries take precedence over both the global cap and the per-user DB value,
// allowing individual users to have a higher (or lower) ceiling than the global default.
type UserDailyLimit struct {
	Itcode          string  `yaml:"itcode"`
	BackendDailyUSD float64 `yaml:"backend_daily_usd"`  // 0 = not overridden for backend channel
	AWSDailyUSD     float64 `yaml:"aws_daily_usd"`      // 0 = not overridden for AWS channel (daily)
	AWSMonthlyUSD   float64 `yaml:"aws_monthly_usd"`    // 0 = not overridden; >0 switches AWS to monthly billing
}

// BackendDailyLimit specifies a per-backend daily cost cap (display/monitoring only).
// A limit matches a backend either by exact Name or by Prefix (e.g. "lianxiang-"
// applies to lianxiang-sc031, lianxiang-sc032, ...). DailyUSD is the ceiling in USD
// where 0 (or absent/negative) means unlimited, following the project-wide
// "0 = unlimited" convention.
type BackendDailyLimit struct {
	Name     string  `yaml:"name"`   // exact backend name (optional if Prefix is set)
	Prefix   string  `yaml:"prefix"` // backend name prefix; applies to every backend whose name starts with it
	DailyUSD float64 `yaml:"backend_daily_usd"` // 0 = unlimited
}

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
	DowngradedTTL     time.Duration     `yaml:"downgraded_ttl"`    // how long to skip original model after downgrade
	Fallback          string            `yaml:"fallback"`          // fallback model name from public_providers for auto-downgrade
	BackendDailyMax         float64           `yaml:"backend_daily_max"`          // max backend spend per user per day in USD (0 = unlimited)
	UserDailyLimits         []UserDailyLimit  `yaml:"user_daily_limits"`          // per-itcode daily spending overrides
	BackendDailyLimits      []BackendDailyLimit `yaml:"backend_daily_limits"`     // per-backend daily cost caps (display only, 0 = unlimited)
	LobsterAutoForward      bool                         `yaml:"lobster_auto_forward"`       // auto-forward lobster (openclaw/hermes) Claude requests to fallback
	LobsterForwardWhitelist []string                     `yaml:"lobster_forward_whitelist"` // itcodes exempt from lobster forwarding
	AWS                     AWSConfig                    `yaml:"aws"`
	PublicProviders         []PublicProvider             `yaml:"public_providers"`           // third-party model providers accessible from all channels
	BackendModelPricing     map[string]ModelPricingEntry `yaml:"backend_model_pricing"`      // glob pattern -> pricing for backend channel
	ValidateBackends        bool                         `yaml:"validate_backends"`          // check backend /v1/models on startup (default: false)
	RankingHiddenItcodes    []string                     `yaml:"ranking_hidden_itcodes"`     // itcodes hidden from ranking / user-daily lists (still counted in totals)
}

// PublicProvider represents a third-party model provider (e.g. Kimi, MiniMax)
// that is accessible from both backend and AWS channels.
// It exposes two URLs for transparent forwarding:
//   - OpenAIURL:     for /v1/chat/completions requests
//   - AnthropicURL:  for /v1/messages requests
type PublicProvider struct {
	Name         string                       `yaml:"name"`
	OpenAIURL    string                       `yaml:"openai_url"`    // base URL for OpenAI-compatible API
	AnthropicURL string                       `yaml:"anthropic_url"` // base URL for Anthropic-compatible API
	APIKey       string                       `yaml:"api_key"`
	Enabled      bool                         `yaml:"enabled"`
	Models       []string                     `yaml:"models"`        // supported model names (exact match)
	ModelPricing map[string]ModelPricingEntry  `yaml:"model_pricing"` // model name -> pricing per 1M tokens
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
	Dir    string `yaml:"dir"`    // directory for error log files (daily rotation)
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
	Socks5Proxy     string                       `yaml:"socks5"`        // optional socks5 proxy, e.g. socks5://user:pass@host:port or user:pass@host:port
	AWSDailyMax     float64                      `yaml:"aws_daily_max"`   // max AWS spend per user per day in USD (0 = unlimited)
	AWSMonthlyMax   float64                      `yaml:"aws_monthly_max"` // max AWS spend per user per natural month in USD (0 = use daily limit)
	ModelReplace    map[string]string            `yaml:"model_replace"`   // exact: upstream name -> Bedrock ARN
	ModelDefault    map[string]string            `yaml:"model_default"` // glob pattern -> upstream name
	ModelPricing    map[string]ModelPricingEntry `yaml:"model_pricing"` // glob pattern -> pricing
	// ModelCapabilities is an ordered list of per-model-family capability rules.
	// The first entry whose Match substring appears (case-insensitive) in either
	// the resolved Bedrock model or the requested model name wins. It controls how
	// the request body is adapted (thinking mode, output_config support) so that new
	// model families can be onboarded by editing config instead of changing code.
	ModelCapabilities []ModelCapability `yaml:"model_capabilities"`
	// AllowedBodyFields is the whitelist of top-level fields that may be forwarded
	// to Bedrock. Any field not listed is stripped before the request is sent,
	// which prevents Claude-Code-injected fields (e.g. "diagnostics", "stream",
	// "context_management") from triggering Bedrock "Extra inputs are not permitted"
	// ValidationExceptions. When empty, a safe built-in default is used.
	AllowedBodyFields []string `yaml:"allowed_body_fields"`
}

// ModelCapability describes how the request body should be adapted for a family
// of models, matched by a case-insensitive substring of the model name.
type ModelCapability struct {
	Match        string `yaml:"match"`         // case-insensitive substring, e.g. "opus-4"
	Thinking     string `yaml:"thinking"`      // "adaptive" | "legacy"
	OutputConfig bool   `yaml:"output_config"` // whether output_config may be forwarded
}

// ModelPricingEntry holds per-model pricing in USD per 1M tokens.
type ModelPricingEntry struct {
	Input      float64 `yaml:"input"`
	Output     float64 `yaml:"output"`
	CacheRead  float64 `yaml:"cache_read"`
	CacheWrite float64 `yaml:"cache_write"`
}

// defaultBodyFieldAllowlist is the built-in set of top-level request fields that
// are safe to forward to Bedrock's Anthropic Messages API. Used when
// AWSConfig.AllowedBodyFields is empty. Note: "metadata", "stream", "model",
// "diagnostics" and "context_management" are intentionally excluded — Bedrock
// either rejects them or they must not be forwarded.
var defaultBodyFieldAllowlist = []string{
	"anthropic_version",
	"messages",
	"system",
	"max_tokens",
	"temperature",
	"top_p",
	"top_k",
	"stop_sequences",
	"tools",
	"tool_choice",
	"thinking",
	"output_config",
}

// CapsFor returns the capability rule for the given model. Both the resolved
// Bedrock model identifier and the original requested model name are consulted,
// because bedrockModel may be an inference-profile ARN that does not contain the
// model family. The first rule whose Match substring (case-insensitive) appears
// in either name wins. If no rule matches, a safe default is returned: legacy
// thinking with output_config disabled (equivalent to the pre-table behaviour for
// non-adaptive models).
func (c AWSConfig) CapsFor(bedrockModel, requestModel string) ModelCapability {
	b := strings.ToLower(bedrockModel)
	r := strings.ToLower(requestModel)
	for _, mc := range c.ModelCapabilities {
		if mc.Match == "" {
			continue
		}
		m := strings.ToLower(mc.Match)
		if strings.Contains(b, m) || strings.Contains(r, m) {
			return mc
		}
	}
	return ModelCapability{Thinking: "legacy", OutputConfig: false}
}

// BodyFieldAllowlist returns the configured top-level field whitelist, or the
// built-in default when none is configured. The returned slice must not be
// mutated by callers.
func (c AWSConfig) BodyFieldAllowlist() []string {
	if len(c.AllowedBodyFields) > 0 {
		return c.AllowedBodyFields
	}
	return defaultBodyFieldAllowlist
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

// LookupUserDailyLimit returns the UserDailyLimit entry for the given itcode, or nil if not found.
func (c *Config) LookupUserDailyLimit(itcode string) *UserDailyLimit {
	for i := range c.UserDailyLimits {
		if c.UserDailyLimits[i].Itcode == itcode {
			return &c.UserDailyLimits[i]
		}
	}
	return nil
}

// LookupBackendDailyLimit returns the configured daily cap (USD) for the given
// backend name, or 0 when there is no matching entry or the cap is not positive.
// A return of 0 means unlimited (FR-002), and callers can safely treat it as a
// divide-by-zero guard for percentage math (FR-010).
//
// An exact Name match always wins over a Prefix match. Among prefix entries the
// longest matching prefix wins, so a more specific prefix can override a broader one.
func (c *Config) LookupBackendDailyLimit(name string) float64 {
	bestPrefixLen := -1
	var bestPrefixUSD float64
	for i := range c.BackendDailyLimits {
		l := &c.BackendDailyLimits[i]
		if l.Name != "" && l.Name == name {
			if l.DailyUSD > 0 {
				return l.DailyUSD
			}
			return 0
		}
		if l.Prefix != "" && strings.HasPrefix(name, l.Prefix) && len(l.Prefix) > bestPrefixLen {
			bestPrefixLen = len(l.Prefix)
			bestPrefixUSD = l.DailyUSD
		}
	}
	if bestPrefixLen >= 0 && bestPrefixUSD > 0 {
		return bestPrefixUSD
	}
	return 0
}

// IsLobsterWhitelisted returns true if the itcode is exempt from lobster auto-forwarding.
func (c *Config) IsLobsterWhitelisted(itcode string) bool {
	for _, w := range c.LobsterForwardWhitelist {
		if w == itcode {
			return true
		}
	}
	return false
}

// LookupPublicProvider returns the PublicProvider that serves the given model, or nil.
func (c *Config) LookupPublicProvider(model string) *PublicProvider {
	for i := range c.PublicProviders {
		p := &c.PublicProviders[i]
		if !p.Enabled {
			continue
		}
		for _, m := range p.Models {
			if m == model {
				return p
			}
		}
	}
	return nil
}

// PublicModelList returns all model names from enabled public providers.
func (c *Config) PublicModelList() []string {
	var models []string
	for _, p := range c.PublicProviders {
		if !p.Enabled {
			continue
		}
		models = append(models, p.Models...)
	}
	return models
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
