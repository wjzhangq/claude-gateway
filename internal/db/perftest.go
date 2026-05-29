package db

import (
	"encoding/json"
	"time"
)

type PerfTestRun struct {
	ID             int64     `json:"id"`
	CreatedAt      time.Time `json:"created_at"`
	InitiatedBy    string    `json:"initiated_by"`
	Status         string    `json:"status"`
	Channels       string    `json:"channels"`
	InputSizes     string    `json:"input_sizes"`
	OutputSizes    string    `json:"output_sizes"`
	TotalCells     int       `json:"total_cells"`
	CompletedCells int       `json:"completed_cells"`
	ErrorMsg       string    `json:"error_msg,omitempty"`
}

type PerfTestResult struct {
	ID                 int64   `json:"id"`
	RunID              int64   `json:"run_id"`
	Channel            string  `json:"channel"`
	Model              string  `json:"model"`
	InputTokens        int     `json:"input_tokens"`
	MaxTokens          int     `json:"max_tokens"`
	TTFT_ms            float64 `json:"ttft_ms"`
	TPOT_ms            float64 `json:"tpot_ms"`
	TokensPerSecond    float64 `json:"tokens_per_second"`
	ActualOutputTokens int     `json:"actual_output_tokens"`
	TotalDuration_ms   float64 `json:"total_duration_ms"`
	Status             string  `json:"status"`
	ErrorMsg           string  `json:"error_msg,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

func (d *DB) CreatePerfTestRun(initiatedBy string, channels json.RawMessage, inputSizes, outputSizes json.RawMessage, totalCells int) (int64, error) {
	result, err := d.Exec(
		`INSERT INTO perf_test_runs (initiated_by, channels, input_sizes, output_sizes, total_cells) VALUES (?, ?, ?, ?, ?)`,
		initiatedBy, string(channels), string(inputSizes), string(outputSizes), totalCells,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (d *DB) UpdatePerfTestRunStatus(id int64, status string, errorMsg string) error {
	_, err := d.Exec(`UPDATE perf_test_runs SET status = ?, error_msg = ? WHERE id = ?`, status, errorMsg, id)
	return err
}

func (d *DB) IncrementPerfTestRunCompleted(id int64) error {
	_, err := d.Exec(`UPDATE perf_test_runs SET completed_cells = completed_cells + 1 WHERE id = ?`, id)
	return err
}

func (d *DB) GetPerfTestRun(id int64) (*PerfTestRun, error) {
	row := d.QueryRow(`SELECT id, created_at, initiated_by, status, channels, input_sizes, output_sizes, total_cells, completed_cells, COALESCE(error_msg,'') FROM perf_test_runs WHERE id = ?`, id)
	var r PerfTestRun
	if err := row.Scan(&r.ID, &r.CreatedAt, &r.InitiatedBy, &r.Status, &r.Channels, &r.InputSizes, &r.OutputSizes, &r.TotalCells, &r.CompletedCells, &r.ErrorMsg); err != nil {
		return nil, err
	}
	return &r, nil
}

func (d *DB) ListPerfTestRuns(limit int) ([]PerfTestRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.Query(`SELECT id, created_at, initiated_by, status, channels, input_sizes, output_sizes, total_cells, completed_cells, COALESCE(error_msg,'') FROM perf_test_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []PerfTestRun
	for rows.Next() {
		var r PerfTestRun
		if err := rows.Scan(&r.ID, &r.CreatedAt, &r.InitiatedBy, &r.Status, &r.Channels, &r.InputSizes, &r.OutputSizes, &r.TotalCells, &r.CompletedCells, &r.ErrorMsg); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, nil
}

func (d *DB) InsertPerfTestResult(r *PerfTestResult) (int64, error) {
	result, err := d.Exec(
		`INSERT INTO perf_test_results (run_id, channel, model, input_tokens, max_tokens, ttft_ms, tpot_ms, tokens_per_second, actual_output_tokens, total_duration_ms, status, error_msg) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.RunID, r.Channel, r.Model, r.InputTokens, r.MaxTokens, r.TTFT_ms, r.TPOT_ms, r.TokensPerSecond, r.ActualOutputTokens, r.TotalDuration_ms, r.Status, r.ErrorMsg,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (d *DB) GetPerfTestResults(runID int64) ([]PerfTestResult, error) {
	rows, err := d.Query(`SELECT id, run_id, channel, model, input_tokens, max_tokens, ttft_ms, tpot_ms, tokens_per_second, actual_output_tokens, total_duration_ms, status, COALESCE(error_msg,''), created_at FROM perf_test_results WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []PerfTestResult
	for rows.Next() {
		var r PerfTestResult
		if err := rows.Scan(&r.ID, &r.RunID, &r.Channel, &r.Model, &r.InputTokens, &r.MaxTokens, &r.TTFT_ms, &r.TPOT_ms, &r.TokensPerSecond, &r.ActualOutputTokens, &r.TotalDuration_ms, &r.Status, &r.ErrorMsg, &r.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}
