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
	BackendDailyUSD float64 `yaml:"backend_daily_usd"` // 0 = not overridden for backend channel
	AWSDailyUSD     float64 `yaml:"aws_daily_usd"`     // 0 = not overridden for AWS channel (daily)
	AWSMonthlyUSD   float64 `yaml:"aws_monthly_usd"`   // 0 = not overridden; >0 switches AWS to monthly billing
}

// BackendDailyLimit specifies a per-backend daily cost cap (display/monitoring only).
// A limit matches a backend either by exact Name or by Prefix (e.g. "lianxiang-"
// applies to lianxiang-sc031, lianxiang-sc032, ...). DailyUSD is the ceiling in USD
// where 0 (or absent/negative) means unlimited, following the project-wide
// "0 = unlimited" convention.
type BackendDailyLimit struct {
	Name     string  `yaml:"name"`              // exact backend name (optional if Prefix is set)
	Prefix   string  `yaml:"prefix"`            // backend name prefix; applies to every backend whose name starts with it
	DailyUSD float64 `yaml:"backend_daily_usd"` // 0 = unlimited
}

// Config is the root configuration structure.
type Config struct {
	Server                  ServerConfig                 `yaml:"server"`
	Database                DatabaseConfig               `yaml:"database"`
	Log                     LogConfig                    `yaml:"log"`
	Auth                    AuthConfig                   `yaml:"auth"`
	Groups                  []Group                      `yaml:"groups"`
	Backends                []BackendAPI                 `yaml:"backends"`
	UsageSync               time.Duration                `yaml:"usage_sync_time"`
	ModelReplacements       map[string]string            `yaml:"model_replacements"`
	DowngradedTTL           time.Duration                `yaml:"downgraded_ttl"`            // how long to skip original model after downgrade
	Fallback                string                       `yaml:"fallback"`                  // fallback model name from public_providers for auto-downgrade
	BackendDailyMax         float64                      `yaml:"backend_daily_max"`         // max backend spend per user per day in USD (0 = unlimited)
	UserDailyLimits         []UserDailyLimit             `yaml:"user_daily_limits"`         // per-itcode daily spending overrides
	BackendDailyLimits      []BackendDailyLimit          `yaml:"backend_daily_limits"`      // per-backend daily cost caps (display only, 0 = unlimited)
	LobsterAutoForward      bool                         `yaml:"lobster_auto_forward"`      // auto-forward lobster (openclaw/hermes) Claude requests to fallback
	LobsterForwardWhitelist []string                     `yaml:"lobster_forward_whitelist"` // itcodes exempt from lobster forwarding
	AWS                     AWSConfig                    `yaml:"aws"`
	PublicProviders         []PublicProvider             `yaml:"public_providers"`       // third-party model providers accessible from all channels
	BackendModelPricing     map[string]ModelPricingEntry `yaml:"backend_model_pricing"`  // glob pattern -> pricing for backend channel
	ValidateBackends        bool                         `yaml:"validate_backends"`      // check backend /v1/models on startup (default: false)
	RankingHiddenItcodes    []string                     `yaml:"ranking_hidden_itcodes"` // itcodes hidden from ranking / user-daily lists (still counted in totals)
	AttributionLabels       map[string]string            `yaml:"attribution_labels"`     // display names for token attribution sides (shen / non)
	AttributionLeaders      []AttributionLeader          `yaml:"attribution_leaders"`    // selectable 负责人 list for the org-management tab
	IPGeo                   IPGeoConfig                  `yaml:"ip_geo"`                 // per-request IP → city / HQ tagging
	Analyze                 AnalyzeConfig                `yaml:"analyze"`                // offline traffic abuse analysis (feature 004)
	WebSearch               WebSearchConfig              `yaml:"websearch"`              // gateway-simulated web_search server tool (backend channel)
}

// WebSearchConfig configures gateway-side simulation of Anthropic's web_search
// server tool for the backend channel. Backend upstreams are third-party
// Anthropic-compatible relays that don't implement the server tool, so the
// gateway rewrites it into a plain client tool, intercepts the model's
// web_search tool_use calls, runs the search against SearXNG, feeds results
// back, and finally emits native server_tool_use + web_search_tool_result
// blocks so Claude Code renders the search natively. Disabled by default.
type WebSearchConfig struct {
	Enabled         bool          `yaml:"enabled"`           // master switch; when false the proxy never intercepts web_search
	Provider        string        `yaml:"provider"`          // search provider identifier (currently only "searxng")
	SearchURL       string        `yaml:"search_url"`        // SearXNG search endpoint, e.g. https://host/search
	Authorization   string        `yaml:"authorization"`     // value placed verbatim into the Authorization header
	Language        string        `yaml:"language"`          // default search language (request may override)
	MaxResults      int           `yaml:"max_results"`       // results returned to the model per search
	SnippetMaxChars int           `yaml:"snippet_max_chars"` // per-result content truncation length
	Timeout         time.Duration `yaml:"timeout"`           // SearXNG request timeout (e.g. 10s)
	DefaultMaxUses  int           `yaml:"default_max_uses"`  // per-request search cap when the tool omits max_uses
}

// AnalyzeConfig configures the offline traffic abuse analysis pipeline (feature 004).
// The analyzer runs as a side-channel batch job (cmd/check --analyze): it pulls
// pending signals from the gateway, classifies each with rules first and Haiku only
// as a fallback, then writes the verdict back. Haiku is reached through HaikuBaseURL
// when set (typically this gateway itself, so the call is load-balanced, billed, and
// logged); when empty the analyzer picks an available backend node and calls it
// directly with that backend's own credentials.
type AnalyzeConfig struct {
	Enabled      bool        `yaml:"enabled"`        // master switch; when false the proxy skips signal extraction/enqueue
	HaikuBaseURL string      `yaml:"haiku_base_url"` // base URL for the Haiku fallback (empty = hit an available backend node directly)
	HaikuAPIKey  string      `yaml:"haiku_api_key"`  // gateway key used by the analyzer for its Haiku calls (ignored when haiku_base_url is empty; the backend's own key is used)
	HaikuModel   string      `yaml:"haiku_model"`    // model name for the fallback classification
	AnalyzerUA   string      `yaml:"analyzer_ua"`    // UA the analyzer sends; the proxy skips enqueue for requests carrying it (anti-recursion)
	BatchSize    int         `yaml:"batch_size"`     // pending records pulled per batch
	MaxRetry     int         `yaml:"max_retry"`      // Haiku fallback retry ceiling before a record is skipped
	Score        ScoreConfig `yaml:"score"`          // abuse-score weights and thresholds
}

// ScoreConfig holds the abuse-score weights and thresholds. score is read-time
// computed, so changing these takes effect on the next report without a rewrite.
type ScoreConfig struct {
	NonWork       float64 `yaml:"non_work"`       // weight of the non-work-task ratio
	Volume        float64 `yaml:"volume"`         // weight of the over-baseline volume term
	BaselineTasks int     `yaml:"baseline_tasks"` // logical-task baseline; only volume above it counts
	Threshold     float64 `yaml:"threshold"`      // score at/above which a user enters the review queue
}

// IPGeoConfig configures per-request IP geolocation tagging.
// CacheFile is where the in-memory IP → city cache is persisted (JSON).
// HQCIDRs lists the company IP ranges (including VPN) that count as headquarters;
// any request from an IP inside one of these ranges is tagged is_hq=true.
type IPGeoConfig struct {
	CacheFile string   `yaml:"cache_file"` // path to the IP → city cache file (empty = disabled persistence)
	HQCIDRs   []string `yaml:"hq_cidrs"`   // CIDR ranges considered headquarters (incl. VPN)
}

// PublicProvider represents a third-party model provider (e.g. Kimi, MiniMax)
// that is accessible from both backend and AWS channels.
// It exposes two URLs for transparent forwarding:
//   - OpenAIURL:     for /v1/chat/completions requests
//   - AnthropicURL:  for /v1/messages requests
type PublicProvider struct {
	Name           string                       `yaml:"name"`
	OpenAIURL      string                       `yaml:"openai_url"`      // base URL for OpenAI-compatible API
	AnthropicURL   string                       `yaml:"anthropic_url"`   // base URL for Anthropic-compatible API
	APIKey         string                       `yaml:"api_key"`
	Enabled        bool                         `yaml:"enabled"`
	Models         []string                     `yaml:"models"`          // supported model names (exact match)
	AllowedItcodes []string                     `yaml:"allowed_itcodes"` // if non-empty, restrict access to these itcodes
	AllowedModels  []string                     `yaml:"allowed_models"`  // if non-empty, only these models are restricted; others remain open
	ModelPricing   map[string]ModelPricingEntry `yaml:"model_pricing"`   // model name -> pricing per 1M tokens
}

// AttributionLeader is one selectable 负责人 (attribution group leader) for the
// org-management UI. Name is the leader/group name written to users.attr_group;
// Side is the business line ("shen" | "non") written to users.attr_side so the
// picked user immediately rolls up under that team in the Token 归口 report.
type AttributionLeader struct {
	Name string `yaml:"name" json:"name"`
	Side string `yaml:"side" json:"side"` // shen | non
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
	SessionSecret string        `yaml:"session_secret"`
	SessionMaxAge int           `yaml:"session_max_age"` // seconds
	CodeExpiry    time.Duration `yaml:"code_expiry"`     // verification code TTL
	AdminItcode   string        `yaml:"admin_itcode"`
	SendCodeURL   string        `yaml:"send_code_url"`
	InviteCode    string        `yaml:"invite_code"`
}

// BackendAPI represents a single upstream Claude API endpoint.
type BackendAPI struct {
	Name    string `yaml:"name"`
	URL     string `yaml:"url"`
	APIKey  string `yaml:"api_key"`
	Weight  int    `yaml:"weight"`
	Enabled bool   `yaml:"enabled"`
	// QuotaReset controls how quota isolation recovers (feature 003, FR-014).
	// Empty (default) => treat quota isolation as a jittered long TTL and let the
	// probe/releaseExpired path discover recovery. "cst-midnight" / "utc-midnight"
	// => release at that wall-clock reset instant instead. Avoids assuming every
	// backend shares one reset clock.
	QuotaReset string `yaml:"quota_reset"`
}

// AWSConfig holds all AWS Bedrock channel configuration.
type AWSConfig struct {
	Region          string                       `yaml:"region"`
	AccessKeyID     string                       `yaml:"access_key_id"`
	SecretAccessKey string                       `yaml:"secret_access_key"`
	CacheEnabled    int                          `yaml:"cache_enabled"`
	CacheTTL        time.Duration                `yaml:"cache_ttl"`
	Socks5Proxy     string                       `yaml:"socks5"`          // optional socks5 proxy, e.g. socks5://user:pass@host:port or user:pass@host:port
	AWSDailyMax     float64                      `yaml:"aws_daily_max"`   // max AWS spend per user per day in USD (0 = unlimited)
	AWSMonthlyMax   float64                      `yaml:"aws_monthly_max"` // max AWS spend per user per natural month in USD (0 = use daily limit)
	ModelReplace    map[string]string            `yaml:"model_replace"`   // exact: upstream name -> Bedrock ARN
	ModelDefault    map[string]string            `yaml:"model_default"`   // glob pattern -> upstream name
	ModelPricing    map[string]ModelPricingEntry `yaml:"model_pricing"`   // glob pattern -> pricing
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
		Analyze: AnalyzeConfig{
			// HaikuBaseURL empty by default: the analyzer picks an available backend
			// node and calls it directly with that backend's own key.
			HaikuModel: "claude-haiku-4-5-20251001",
			AnalyzerUA: "claude-gateway-analyzer",
			BatchSize:  500,
			MaxRetry:   3,
			Score: ScoreConfig{
				NonWork:       0.7,
				Volume:        0.3,
				BaselineTasks: 60,
				Threshold:     0.5,
			},
		},
		WebSearch: WebSearchConfig{
			// Disabled by default; a websearch: block in the config enables it.
			Provider:        "searxng",
			Language:        "zh-CN",
			MaxResults:      8,
			SnippetMaxChars: 800,
			Timeout:         10 * time.Second,
			DefaultMaxUses:  5,
		},
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

// LookupAttributionLeader returns the configured AttributionLeader with the given
// name, or nil when no entry matches. Used to resolve the business-line side when a
// leader is picked in the org-management tab.
func (c *Config) LookupAttributionLeader(name string) *AttributionLeader {
	for i := range c.AttributionLeaders {
		if c.AttributionLeaders[i].Name == name {
			return &c.AttributionLeaders[i]
		}
	}
	return nil
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

// LookupPublicProvider returns the PublicProvider that serves the given model for the given user, or nil.
// Pass an empty itcode to bypass access restrictions (e.g. for system-level fallback routing).
func (c *Config) LookupPublicProvider(model, itcode string) *PublicProvider {
	for i := range c.PublicProviders {
		p := &c.PublicProviders[i]
		if !p.Enabled {
			continue
		}
		for _, m := range p.Models {
			if m != model {
				continue
			}
			// No itcode restrictions configured — open to all.
			if len(p.AllowedItcodes) == 0 {
				return p
			}
			// Determine if this specific model is subject to the restriction.
			modelRestricted := len(p.AllowedModels) == 0
			if !modelRestricted {
				for _, am := range p.AllowedModels {
					if am == model {
						modelRestricted = true
						break
					}
				}
			}
			if !modelRestricted {
				return p
			}
			// Model is restricted — check itcode. Empty itcode (system call) bypasses.
			if itcode == "" {
				return p
			}
			for _, allowed := range p.AllowedItcodes {
				if allowed == itcode {
					return p
				}
			}
			return nil // model found but user not allowed
		}
	}
	return nil
}

// IsPublicModel reports whether model is served by any enabled public provider,
// regardless of user restrictions. Used to distinguish "access denied" (403) from
// "model not found" (fall through to backend routing).
func (c *Config) IsPublicModel(model string) bool {
	for i := range c.PublicProviders {
		p := &c.PublicProviders[i]
		if !p.Enabled {
			continue
		}
		for _, m := range p.Models {
			if m == model {
				return true
			}
		}
	}
	return false
}

// IsModelRestrictedForItcode reports whether model from the given providerName is
// access-restricted and itcode is NOT in the allowlist. Returns true when the
// model should be hidden from this user's /v1/models response.
func (c *Config) IsModelRestrictedForItcode(providerName, model, itcode string) bool {
	for i := range c.PublicProviders {
		p := &c.PublicProviders[i]
		if p.Name != providerName || !p.Enabled {
			continue
		}
		if len(p.AllowedItcodes) == 0 {
			return false
		}
		modelRestricted := len(p.AllowedModels) == 0
		if !modelRestricted {
			for _, am := range p.AllowedModels {
				if am == model {
					modelRestricted = true
					break
				}
			}
		}
		if !modelRestricted {
			return false
		}
		for _, allowed := range p.AllowedItcodes {
			if strings.TrimSpace(allowed) == itcode {
				return false
			}
		}
		return true
	}
	return false
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
