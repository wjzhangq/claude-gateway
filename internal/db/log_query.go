package db

import (
	"fmt"
)

// RecentLogEntry combines fields from both backend and AWS logs for unified display.
type RecentLogEntry struct {
	ID           int64   `json:"id"`
	Channel      string  `json:"channel"` // "backend" or "aws"
	UserID       int64   `json:"user_id"`
	Itcode       string  `json:"itcode"`
	APIKeyID     int64   `json:"api_key_id"`
	Model        string  `json:"model"`
	Backend      string  `json:"backend,omitempty"`      // backend only
	BedrockModel string  `json:"bedrock_model,omitempty"` // aws only
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	StatusCode   int     `json:"status_code"`
	LatencyMs    int64   `json:"latency_ms"`
	CreatedAt    string  `json:"created_at"`
}

// GetRecentErrors returns recent error logs (status_code != 200) from both channels.
func (d *DB) GetRecentErrors(offset, limit int, channel string, hours int) ([]*RecentLogEntry, int, error) {
	return d.getRecentLogs(offset, limit, channel, hours, true)
}

// GetRecentLogs returns recent access logs from both channels (all status codes).
func (d *DB) GetRecentLogs(offset, limit int, channel string, hours int) ([]*RecentLogEntry, int, error) {
	return d.getRecentLogs(offset, limit, channel, hours, false)
}

// getRecentLogs queries usage_logs and/or aws_usage_logs with unified output.
func (d *DB) getRecentLogs(offset, limit int, channel string, hours int, errorsOnly bool) ([]*RecentLogEntry, int, error) {
	var logs []*RecentLogEntry
	var total int

	timeFilter := fmt.Sprintf("datetime('now', '+8 hours', '-%d hours')", hours)
	statusFilter := ""
	if errorsOnly {
		statusFilter = "AND status_code != 200"
	}

	// Query backend logs if channel is "" or "backend"
	if channel == "" || channel == "backend" {
		backendSQL := fmt.Sprintf(`
			SELECT l.id, l.user_id, COALESCE(u.itcode, ''), l.api_key_id, l.model, l.backend,
			       l.input_tokens, l.output_tokens, l.total_tokens, l.cost_usd, l.status_code, l.latency_ms, l.created_at
			FROM usage_logs l
			LEFT JOIN users u ON u.id = l.user_id
			WHERE l.created_at >= %s %s
		`, timeFilter, statusFilter)

		rows, err := d.readonlyDB.Query(backendSQL)
		if err != nil {
			return nil, 0, fmt.Errorf("query backend logs: %w", err)
		}
		for rows.Next() {
			e := &RecentLogEntry{Channel: "backend"}
			if err := rows.Scan(&e.ID, &e.UserID, &e.Itcode, &e.APIKeyID, &e.Model, &e.Backend,
				&e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.CostUSD, &e.StatusCode, &e.LatencyMs, &e.CreatedAt); err != nil {
				rows.Close()
				return nil, 0, fmt.Errorf("scan backend log: %w", err)
			}
			logs = append(logs, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, 0, fmt.Errorf("backend rows: %w", err)
		}
	}

	// Query AWS logs if channel is "" or "aws"
	if channel == "" || channel == "aws" {
		awsSQL := fmt.Sprintf(`
			SELECT l.id, l.user_id, COALESCE(u.itcode, ''), l.api_key_id, l.model, l.bedrock_model,
			       l.input_tokens, l.output_tokens, l.total_tokens, l.cost_usd, l.status_code, l.latency_ms, l.created_at
			FROM aws_usage_logs l
			LEFT JOIN users u ON u.id = l.user_id
			WHERE l.created_at >= %s %s
		`, timeFilter, statusFilter)

		rows, err := d.readonlyDB.Query(awsSQL)
		if err != nil {
			return nil, 0, fmt.Errorf("query aws logs: %w", err)
		}
		for rows.Next() {
			e := &RecentLogEntry{Channel: "aws"}
			if err := rows.Scan(&e.ID, &e.UserID, &e.Itcode, &e.APIKeyID, &e.Model, &e.BedrockModel,
				&e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.CostUSD, &e.StatusCode, &e.LatencyMs, &e.CreatedAt); err != nil {
				rows.Close()
				return nil, 0, fmt.Errorf("scan aws log: %w", err)
			}
			logs = append(logs, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, 0, fmt.Errorf("aws rows: %w", err)
		}
	}

	total = len(logs)

	// Sort by created_at DESC (newest first)
	// Simple bubble sort since logs are likely already mostly sorted
	for i := 0; i < len(logs)-1; i++ {
		for j := i + 1; j < len(logs); j++ {
			if logs[i].CreatedAt < logs[j].CreatedAt {
				logs[i], logs[j] = logs[j], logs[i]
			}
		}
	}

	// Apply pagination
	if offset >= len(logs) {
		return []*RecentLogEntry{}, total, nil
	}
	end := offset + limit
	if end > len(logs) {
		end = len(logs)
	}

	return logs[offset:end], total, nil
}
