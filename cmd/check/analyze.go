package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/classify"
)

// pendingRecord mirrors db.PendingRecord as returned by the gateway.
type pendingRecord struct {
	ID         int64     `json:"id"`
	UsageLogID int64     `json:"usage_log_id"`
	UserID     int64     `json:"user_id"`
	Signal     string    `json:"signal"`
	RetryCount int       `json:"retry_count"`
	CreatedAt  time.Time `json:"created_at"`
}

// analysisResult mirrors db.AnalysisResult expected by the gateway.
type analysisResult struct {
	PendingID     int64  `json:"pending_id"`
	UsageLogID    int64  `json:"usage_log_id"`
	TaskType      string `json:"task_type"`
	WorkRelated   *bool  `json:"work_related"`
	CodeDirection string `json:"code_direction"`
	WorkReason    string `json:"work_reason"`
	DocActivity   string `json:"doc_activity"`
	FromHaiku     bool   `json:"from_haiku"`
	Retry         bool   `json:"retry"`
}

// analyzeStats tallies one --analyze run for SC-002/003/006 verification.
type analyzeStats struct {
	Processed  int
	RuleOnly   int
	HaikuCalls int
	Retried    int
	Skipped    int
}

// runAnalyze consumes the pending-analysis queue in batches: pull → classify each
// (rules first, Haiku only when rules cannot decide; continuation/subagent rounds
// inherit their parent turn and never call a model) → write verdicts back. It
// loops until a batch comes back empty, then prints a summary. A single Haiku
// failure never aborts the batch — that record is flagged for retry (FR-010).
func runAnalyze(client *http.Client, cfg *config.Config, gatewayURL string) {
	a := cfg.Analyze
	if !a.Enabled {
		log.Println("analyze disabled in config (analyze.enabled=false); nothing to do")
		return
	}
	secret := cfg.Auth.SessionSecret
	clsCfg := classify.FromAnalyzeConfig(a)

	// The Haiku fallback goes through the gateway itself, tagged with the analyzer
	// UA so the proxy skips enqueueing the analyzer's own calls (anti-recursion).
	base := a.HaikuBaseURL
	if base == "" {
		base = gatewayURL
	}
	var hc *classify.HaikuClient
	if a.HaikuAPIKey != "" {
		hc = classify.NewHaikuClient(base, a.HaikuAPIKey, a.HaikuModel)
		hc.UA = a.AnalyzerUA
	} else {
		log.Println("analyze: no haiku_api_key configured; running in pure-rules mode (no fallback)")
	}

	limit := a.BatchSize
	if limit <= 0 {
		limit = 500
	}

	var stats analyzeStats
	for {
		records, err := fetchPending(client, gatewayURL, secret, limit)
		if err != nil {
			log.Fatalf("fetch pending: %v", err)
		}
		if len(records) == 0 {
			break
		}

		results := make([]analysisResult, 0, len(records))
		for _, rec := range records {
			stats.Processed++
			var sig classify.Signal
			if err := json.Unmarshal([]byte(rec.Signal), &sig); err != nil {
				// Corrupt signal will never parse — retry is pointless, but let the
				// retry ceiling age it out rather than deleting silently.
				log.Printf("[pending %d] bad signal JSON: %v", rec.ID, err)
				results = append(results, analysisResult{PendingID: rec.ID, UsageLogID: rec.UsageLogID, Retry: true})
				stats.Retried++
				continue
			}

			// Records in the queue are user_initiated by construction.
			res, err := classify.AnalyzeSignal(context.Background(), sig, classify.RoleUserInitiated, clsCfg, hc)
			if err != nil {
				// Haiku failed: keep the rule-only verdict queued for retry, do not
				// abort the batch (FR-010).
				results = append(results, analysisResult{PendingID: rec.ID, UsageLogID: rec.UsageLogID, Retry: true})
				stats.Retried++
				continue
			}
			if res.FromHaiku {
				stats.HaikuCalls++
			} else {
				stats.RuleOnly++
			}
			results = append(results, analysisResult{
				PendingID:     rec.ID,
				UsageLogID:    rec.UsageLogID,
				TaskType:      res.TaskType,
				WorkRelated:   res.WorkRelated,
				CodeDirection: res.CodeDirection,
				WorkReason:    res.WorkReason,
				DocActivity:   res.DocActivity,
				FromHaiku:     res.FromHaiku,
			})
		}

		if err := postResults(client, gatewayURL, secret, results); err != nil {
			log.Fatalf("post results: %v", err)
		}

		// A batch smaller than the limit means the queue is drained.
		if len(records) < limit {
			break
		}
	}

	log.Printf("analyze done: processed=%d rule_only=%d haiku_calls=%d retried=%d skipped=%d",
		stats.Processed, stats.RuleOnly, stats.HaikuCalls, stats.Retried, stats.Skipped)
}

// fetchPending GETs one batch of pending records from the gateway.
func fetchPending(client *http.Client, gatewayURL, secret string, limit int) ([]pendingRecord, error) {
	url := fmt.Sprintf("%s/admin/api/analyze/pending?limit=%d", gatewayURL, limit)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+secret)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		return nil, fmt.Errorf("server returned HTTP %d: %s", resp.StatusCode, buf.String())
	}
	var out struct {
		Records []pendingRecord `json:"records"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Records, nil
}

// postResults POSTs verdicts back to the gateway for write-back + queue cleanup.
func postResults(client *http.Client, gatewayURL, secret string, results []analysisResult) error {
	if len(results) == 0 {
		return nil
	}
	body, err := json.Marshal(map[string]any{"results": results})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, gatewayURL+"/admin/api/analyze/results", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		return fmt.Errorf("server returned HTTP %d: %s", resp.StatusCode, buf.String())
	}
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	log.Printf("write-back: %v", result)
	return nil
}
