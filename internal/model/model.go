package model

import "time"

// User represents a gateway user.
type User struct {
	ID          int64     `db:"id"           json:"id"`
	Itcode      string    `db:"itcode"        json:"itcode"`
	Name        string    `db:"name"          json:"name"`
	Role        string    `db:"role"          json:"role"`
	Status      string    `db:"status"        json:"status"`
	QuotaTokens int64     `db:"quota_tokens"  json:"quota_tokens"`
	CreatedAt   time.Time `db:"created_at"    json:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"    json:"updated_at"`
}

// APIKey represents a user's API key.
type APIKey struct {
	ID         int64      `db:"id"          json:"id"`
	UserID     int64      `db:"user_id"     json:"user_id"`
	Key        string     `db:"key"         json:"key"`
	Name       string     `db:"name"        json:"name"`
	Status     string     `db:"status"      json:"status"`
	ExpiresAt  *time.Time `db:"expires_at"  json:"expires_at"`
	CreatedAt  time.Time  `db:"created_at"  json:"created_at"`
	UpdatedAt  time.Time  `db:"updated_at"  json:"updated_at"`
	LastUsedAt *time.Time `db:"last_used_at" json:"last_used_at"`
}

// UsageLog records a single API call.
type UsageLog struct {
	ID           int64     `db:"id"            json:"id"`
	UserID       int64     `db:"user_id"       json:"user_id"`
	Itcode       string    `db:"-"             json:"itcode"`
	APIKeyID     int64     `db:"api_key_id"    json:"api_key_id"`
	Model        string    `db:"model"         json:"model"`
	Backend      string    `db:"backend"       json:"backend"`
	InputTokens  int       `db:"input_tokens"  json:"input_tokens"`
	OutputTokens int       `db:"output_tokens" json:"output_tokens"`
	TotalTokens  int       `db:"total_tokens"  json:"total_tokens"`
	CostUSD      float64   `db:"cost_usd"      json:"cost_usd"`
	StatusCode   int       `db:"status_code"   json:"status_code"`
	Latency      int64     `db:"latency_ms"    json:"latency_ms"`
	CreatedAt    time.Time `db:"created_at"    json:"created_at"`
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

// Application is a user's request to access a model.
type Application struct {
	ID          int64     `db:"id"          json:"id"`
	UserID      int64     `db:"user_id"     json:"user_id"`
	Model       string    `db:"model"       json:"model"`
	Reason      string    `db:"reason"      json:"reason"`
	Status      string    `db:"status"      json:"status"`
	ReviewerID  *int64    `db:"reviewer_id" json:"reviewer_id"`
	ReviewNote  string    `db:"review_note" json:"review_note"`
	CreatedAt   time.Time `db:"created_at"  json:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"  json:"updated_at"`
}
