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
	Processed         int
	RuleOnly          int
	HaikuCalls        int
	Retried           int
	Skipped           int
	HaikuInputTokens  int64 // input tokens billed by the Haiku fallback calls
	HaikuOutputTokens int64 // output tokens billed by the Haiku fallback calls
}

// runAnalyze consumes the pending-analysis queue in batches: pull → classify each
// (rules first, Haiku only when rules cannot decide; continuation/subagent rounds
// inherit their parent turn and never call a model) → write verdicts back. It
// loops until a batch comes back empty, then prints a summary. A single Haiku
// failure never aborts the batch — that record is flagged for retry (FR-010).
//
// When recent is true the analyzer instead makes a single pass over just the
// most recent batch_size records (newest first) and stops, rather than draining
// the whole backlog oldest-first — useful for a quick look at fresh traffic.
func runAnalyze(client *http.Client, cfg *config.Config, gatewayURL string, recent bool) {
	a := cfg.Analyze
	if !a.Enabled {
		log.Println("analyze disabled in config (analyze.enabled=false); nothing to do")
		return
	}
	secret := cfg.Auth.SessionSecret
	clsCfg := classify.FromAnalyzeConfig(a)

	// Decide where the Haiku fallback lands and which key it carries.
	//
	//   - haiku_base_url set  → route through that URL (typically this gateway
	//     itself) with haiku_api_key, tagged with the analyzer UA so the proxy
	//     skips enqueueing the analyzer's own calls (anti-recursion). Empty key
	//     ⇒ pure-rules mode.
	//   - haiku_base_url empty → hit an available backend node directly, using
	//     that backend's own URL + api_key. This bypasses the gateway entirely,
	//     so there is no recursion to guard against and haiku_api_key is unused.
	var hc *classify.HaikuClient
	if a.HaikuBaseURL != "" {
		if a.HaikuAPIKey != "" {
			hc = classify.NewHaikuClient(a.HaikuBaseURL, a.HaikuAPIKey, a.HaikuModel)
			hc.UA = a.AnalyzerUA
		} else {
			log.Println("analyze: no haiku_api_key configured; running in pure-rules mode (no fallback)")
		}
	} else if b := pickAvailableBackend(client, cfg, gatewayURL, secret); b != nil {
		hc = classify.NewHaikuClient(b.URL, b.APIKey, a.HaikuModel)
		hc.UA = a.AnalyzerUA
		log.Printf("analyze: haiku_base_url empty; using backend %q directly", b.Name)
	} else {
		log.Println("analyze: haiku_base_url empty and no available backend found; running in pure-rules mode (no fallback)")
	}

	limit := a.BatchSize
	if limit <= 0 {
		limit = 500
	}

	// order controls which end of the queue we pull. Default drains oldest-first
	// (id ASC) across as many batches as needed; --recent pulls the newest
	// batch_size records (id DESC) in a single pass.
	order := "asc"
	if recent {
		order = "desc"
	}

	var stats analyzeStats
	for {
		records, err := fetchPending(client, gatewayURL, secret, limit, order)
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
				stats.HaikuInputTokens += int64(res.HaikuInTok)
				stats.HaikuOutputTokens += int64(res.HaikuOutTok)
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

		// --recent: only ever process the single newest batch, then stop.
		if recent {
			break
		}
		// A batch smaller than the limit means the queue is drained.
		if len(records) < limit {
			break
		}
	}

	log.Printf("analyze done: processed=%d rule_only=%d haiku_calls=%d retried=%d skipped=%d haiku_input_tokens=%d haiku_output_tokens=%d haiku_total_tokens=%d",
		stats.Processed, stats.RuleOnly, stats.HaikuCalls, stats.Retried, stats.Skipped,
		stats.HaikuInputTokens, stats.HaikuOutputTokens, stats.HaikuInputTokens+stats.HaikuOutputTokens)
}

// pickAvailableBackend returns a backend the analyzer can call directly for its
// Haiku fallback when haiku_base_url is empty. It prefers a backend whose live
// state on the gateway is routable (not isolated / quarantine / disabled), and
// falls back to the first enabled config backend if the gateway's state list is
// unavailable. Returns nil only when config has no enabled backend at all.
func pickAvailableBackend(client *http.Client, cfg *config.Config, gatewayURL, secret string) *config.BackendAPI {
	unroutable := fetchUnroutableBackends(client, gatewayURL, secret)
	var firstEnabled *config.BackendAPI
	for i := range cfg.Backends {
		b := &cfg.Backends[i]
		if !b.Enabled {
			continue
		}
		if firstEnabled == nil {
			firstEnabled = b
		}
		if !unroutable[b.Name] {
			return b // enabled and healthy per the gateway — best choice
		}
	}
	// Every enabled backend is currently unroutable (or the state list was empty);
	// fall back to the first enabled one rather than dropping to pure-rules mode.
	return firstEnabled
}

// fetchUnroutableBackends asks the gateway which backends are not currently
// serving traffic (isolated / quarantine / disabled). A failure returns an empty
// set so the caller degrades to config order rather than aborting.
func fetchUnroutableBackends(client *http.Client, gatewayURL, secret string) map[string]bool {
	out := map[string]bool{}
	req, err := http.NewRequest(http.MethodGet, gatewayURL+"/admin/api/backends", nil)
	if err != nil {
		return out
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("analyze: fetch backend states failed (using config order): %v", err)
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out
	}
	var payload struct {
		Backends []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"backends"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return out
	}
	for _, b := range payload.Backends {
		switch b.State {
		case "healthy", "degraded", "probing":
			// routable — leave out of the unroutable set
		default:
			out[b.Name] = true
		}
	}
	return out
}

// fetchPending GETs one batch of pending records from the gateway. order is
// "desc" to pull the newest records first (--recent) or "asc"/"" for the default
// oldest-first queue order.
func fetchPending(client *http.Client, gatewayURL, secret string, limit int, order string) ([]pendingRecord, error) {
	url := fmt.Sprintf("%s/admin/api/analyze/pending?limit=%d", gatewayURL, limit)
	if order == "desc" {
		url += "&order=recent"
	}
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
