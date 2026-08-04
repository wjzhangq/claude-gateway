package model

import "time"

// User represents a gateway user.
type User struct {
	ID               int64     `db:"id"                json:"id"`
	Itcode           string    `db:"itcode"            json:"itcode"`
	Name             string    `db:"name"              json:"name"`
	Role             string    `db:"role"              json:"role"`
	Status           string    `db:"status"            json:"status"`
	GroupID          int       `db:"group_id"          json:"group_id"`
	DailyQuotaUSD    float64   `db:"daily_quota_usd"   json:"daily_quota_usd"`
	AWSDailyQuotaUSD float64   `db:"aws_daily_quota_usd" json:"aws_daily_quota_usd"`
	AWSEnabled       bool      `db:"aws_enabled"       json:"aws_enabled"`
	Department       string    `db:"department"        json:"department"`
	RoleTag          string    `db:"role_tag"          json:"role_tag"` // 研发 | 非研发 | 未分类
	OrgNote          string    `db:"org_note"          json:"org_note"`
	Mgr1Name         string    `db:"mgr1_name"         json:"mgr1_name"`   // 直接主管
	Mgr2Name         string    `db:"mgr2_name"         json:"mgr2_name"`   // 二级主管
	AttrSide         string    `db:"attr_side"         json:"attr_side"`   // 归口团队: ""|"shen"|"non"
	AttrGroup        string    `db:"attr_group"        json:"attr_group"`  // 归口负责人组名
	IsDeparted       bool      `db:"is_departed"       json:"is_departed"` // 离职标记
	CreatedAt        time.Time `db:"created_at"        json:"created_at"`
	UpdatedAt        time.Time `db:"updated_at"        json:"updated_at"`
}

// APIKey represents a user's API key.
type APIKey struct {
	ID             int64      `db:"id"               json:"id"`
	UserID         int64      `db:"user_id"          json:"user_id"`
	Key            string     `db:"key"              json:"key"`
	Name           string     `db:"name"             json:"name"`
	Status         string     `db:"status"           json:"status"`
	Channel        string     `db:"channel"          json:"channel"` // "backend" | "aws"
	AutoDowngrade  bool       `db:"auto_downgrade"   json:"auto_downgrade"`
	CreatedAt      time.Time  `db:"created_at"       json:"created_at"`
	UpdatedAt      time.Time  `db:"updated_at"       json:"updated_at"`
	LastUsedAt     *time.Time `db:"last_used_at"     json:"last_used_at"`
	TotalCostUSD   float64    `db:"total_cost_usd"   json:"total_cost_usd"`
	BackendCostUSD float64    `db:"backend_cost_usd" json:"backend_cost_usd"`
	AWSCostUSD     float64    `db:"aws_cost_usd"     json:"aws_cost_usd"`
	LockedModel    string     `db:"locked_model"     json:"locked_model"`
}

// UsageLog records a single API call.
type UsageLog struct {
	ID           int64     `db:"id"            json:"id"`
	UserID       int64     `db:"user_id"       json:"user_id"`
	GroupID      int       `db:"group_id"      json:"group_id"`
	Itcode       string    `db:"-"             json:"itcode"`
	APIKeyID     int64     `db:"api_key_id"    json:"api_key_id"`
	Model        string    `db:"model"         json:"model"`
	Backend      string    `db:"backend"       json:"backend"`
	InputTokens      int       `db:"input_tokens"       json:"input_tokens"`
	OutputTokens     int       `db:"output_tokens"      json:"output_tokens"`
	TotalTokens      int       `db:"total_tokens"       json:"total_tokens"`
	CacheReadTokens  int       `db:"cache_read_tokens"  json:"cache_read_tokens"`  // prompt-cache read (discount price)
	CacheWriteTokens int       `db:"cache_write_tokens" json:"cache_write_tokens"` // prompt-cache creation (premium price)
	CostUSD          float64   `db:"cost_usd"           json:"cost_usd"`
	StatusCode   int       `db:"status_code"   json:"status_code"`
	Latency      int64     `db:"latency_ms"    json:"latency_ms"`
	IsOpenClaw   bool      `db:"is_openclaw"   json:"is_openclaw"`
	IsDowngraded bool      `db:"is_downgraded" json:"is_downgraded"`
	UA           string    `db:"ua"            json:"ua"`
	ErrorReason  string    `db:"error_reason"  json:"error_reason"`
	IP           string    `db:"ip"            json:"ip"`
	City         string    `db:"city"          json:"city"`
	IsHQ         bool      `db:"is_hq"         json:"is_hq"`
	// Feature 004 abuse-analysis verdict (written back offline by cmd/check --analyze).
	TaskType      string    `db:"task_type"      json:"task_type"`      // code|doc|other|'' (unanalyzed)
	WorkRelated   int       `db:"work_related"   json:"work_related"`   // 1=yes 0=no -1=undetermined
	CodeDirection string    `db:"code_direction" json:"code_direction"` // 前端/后端/... (code only)
	CreatedAt     time.Time `db:"created_at"     json:"created_at"`

	// PendingSignal / PendingUserInitiated are transient (db:"-"): they carry the
	// compressed classify.Signal JSON and the "should enqueue" decision from the
	// proxy down to the batch insert, which — inside the same transaction — writes a
	// pending_analysis row for a successful user_initiated request (feature 004).
	// Never persisted as columns on usage_logs.
	PendingSignal        string `db:"-" json:"-"` // JSON of classify.Signal; empty ⇒ do not enqueue
	PendingUserInitiated bool   `db:"-" json:"-"` // true only for a user_initiated round
}

// DailyStats aggregates usage per user per model per day.
type DailyStats struct {
	ID           int64   `db:"id"            json:"id"`
	Date         string  `db:"date"          json:"date"`
	UserID       int64   `db:"user_id"       json:"user_id"`
	Model        string  `db:"model"         json:"model"`
	Requests     int     `db:"requests"      json:"requests"`
	InputTokens  int64   `db:"input_tokens"  json:"input_tokens"`
	OutputTokens int64   `db:"output_tokens" json:"output_tokens"`
	TotalTokens  int64   `db:"total_tokens"  json:"total_tokens"`
	CostUSD      float64 `db:"cost_usd"      json:"cost_usd"`
}

// Application is a user's request to activate their account.
type Application struct {
	ID         int64     `db:"id"          json:"id"`
	UserID     int64     `db:"user_id"     json:"user_id"`
	UserItcode string    `db:"-"           json:"user_itcode"`
	UserName   string    `db:"-"           json:"user_name"`
	UserStatus string    `db:"-"           json:"user_status"`
	GroupID    int       `db:"-"           json:"group_id"`
	Reason     string    `db:"reason"      json:"reason"`
	Status     string    `db:"status"      json:"status"`
	ReviewerID *int64    `db:"reviewer_id" json:"reviewer_id"`
	ReviewNote string    `db:"review_note" json:"review_note"`
	CreatedAt  time.Time `db:"created_at"  json:"created_at"`
	UpdatedAt  time.Time `db:"updated_at"  json:"updated_at"`
}

// GroupStats aggregates usage per group.
type GroupStats struct {
	GroupID      int     `json:"group_id"`
	GroupName    string  `json:"group_name"`
	Requests     int     `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// AWSUsageLog records a single AWS Bedrock API call.
type AWSUsageLog struct {
	ID               int64     `db:"id"                json:"id"`
	UserID           int64     `db:"user_id"           json:"user_id"`
	GroupID          int       `db:"group_id"          json:"group_id"`
	Itcode           string    `db:"-"                 json:"itcode"`
	APIKeyID         int64     `db:"api_key_id"        json:"api_key_id"`
	Model            string    `db:"model"             json:"model"`
	BedrockModel     string    `db:"bedrock_model"     json:"bedrock_model"`
	InputTokens      int       `db:"input_tokens"      json:"input_tokens"`
	OutputTokens     int       `db:"output_tokens"     json:"output_tokens"`
	TotalTokens      int       `db:"total_tokens"      json:"total_tokens"`
	CacheReadTokens  int       `db:"cache_read_tokens"  json:"cache_read_tokens"`
	CacheWriteTokens int       `db:"cache_write_tokens" json:"cache_write_tokens"`
	CostUSD          float64   `db:"cost_usd"          json:"cost_usd"`
	StatusCode       int       `db:"status_code"       json:"status_code"`
	Latency          int64     `db:"latency_ms"        json:"latency_ms"`
	UA               string    `db:"ua"                json:"ua"`
	CreatedAt        time.Time `db:"created_at"        json:"created_at"`
}

// UserQuotaOverride stores a per-user backend daily spend cap that takes priority
// over both the global BackendDailyMax and users.daily_quota_usd.
type UserQuotaOverride struct {
	ID          int64   `db:"id"           json:"id"`
	UserID      int64   `db:"user_id"      json:"user_id"`
	Itcode      string  `db:"-"            json:"itcode"` // from JOIN with users
	Name        string  `db:"-"            json:"name"`   // from JOIN with users
	QuotaUSD    float64 `db:"quota_usd"    json:"quota_usd"`
	IsTemporary bool    `db:"is_temporary" json:"is_temporary"`
	ExpiresAt   *string `db:"expires_at"   json:"expires_at"` // "YYYY-MM-DD" or nil
	Note        string  `db:"note"         json:"note"`
	IsExpired   bool    `db:"-"            json:"is_expired"` // computed
	CreatedAt   string  `db:"created_at"   json:"created_at"`
	UpdatedAt   string  `db:"updated_at"   json:"updated_at"`
}

// AWSDailyStats aggregates AWS usage per user per model per day.
type AWSDailyStats struct {
	ID               int64   `db:"id"                json:"id"`
	Date             string  `db:"date"              json:"date"`
	UserID           int64   `db:"user_id"           json:"user_id"`
	Model            string  `db:"model"             json:"model"`
	Requests         int     `db:"requests"          json:"requests"`
	InputTokens      int64   `db:"input_tokens"      json:"input_tokens"`
	OutputTokens     int64   `db:"output_tokens"     json:"output_tokens"`
	TotalTokens      int64   `db:"total_tokens"      json:"total_tokens"`
	CacheReadTokens  int64   `db:"cache_read_tokens"  json:"cache_read_tokens"`
	CacheWriteTokens int64   `db:"cache_write_tokens" json:"cache_write_tokens"`
	CostUSD          float64 `db:"cost_usd"          json:"cost_usd"`
}
