package model

import "time"

// User represents a gateway user.
type User struct {
	ID          int64     `db:"id"`
	Itcode      string    `db:"itcode"`
	Name        string    `db:"name"`
	Role        string    `db:"role"`   // admin | user
	Status      string    `db:"status"` // active | disabled
	QuotaTokens int64     `db:"quota_tokens"` // monthly token quota, 0 = unlimited
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
}

// APIKey represents a user's API key.
type APIKey struct {
	ID        int64     `db:"id"`
	UserID    int64     `db:"user_id"`
	Key       string    `db:"key"`
	Name      string    `db:"name"`
	Status    string    `db:"status"` // active | disabled
	ExpiresAt *time.Time `db:"expires_at"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// UsageLog records a single API call.
type UsageLog struct {
	ID           int64     `db:"id"`
	UserID       int64     `db:"user_id"`
	APIKeyID     int64     `db:"api_key_id"`
	Model        string    `db:"model"`
	Backend      string    `db:"backend"`
	InputTokens  int       `db:"input_tokens"`
	OutputTokens int       `db:"output_tokens"`
	TotalTokens  int       `db:"total_tokens"`
	CostUSD      float64   `db:"cost_usd"`
	StatusCode   int       `db:"status_code"`
	Latency      int64     `db:"latency_ms"`
	CreatedAt    time.Time `db:"created_at"`
}

// DailyStats aggregates usage per user per model per day.
type DailyStats struct {
	ID           int64   `db:"id"`
	Date         string  `db:"date"` // YYYY-MM-DD
	UserID       int64   `db:"user_id"`
	Model        string  `db:"model"`
	Requests     int     `db:"requests"`
	InputTokens  int64   `db:"input_tokens"`
	OutputTokens int64   `db:"output_tokens"`
	TotalTokens  int64   `db:"total_tokens"`
	CostUSD      float64 `db:"cost_usd"`
}

// Application is a user's request to access a model.
type Application struct {
	ID          int64     `db:"id"`
	UserID      int64     `db:"user_id"`
	Model       string    `db:"model"`
	Reason      string    `db:"reason"`
	Status      string    `db:"status"` // pending | approved | rejected
	ReviewerID  *int64    `db:"reviewer_id"`
	ReviewNote  string    `db:"review_note"`
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
}
