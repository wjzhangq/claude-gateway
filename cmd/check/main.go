package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/wjzhangq/claude-gateway/config"
)

type billingUsage struct {
	TotalUsage float64 `json:"total_usage"`
}

type billingSubscription struct {
	HardLimitUSD float64 `json:"hard_limit_usd"`
}

type quotaBackend struct {
	Name      string  `json:"name"`
	Exhausted bool    `json:"exhausted"`
	Limit     float64 `json:"limit"`
	Usage     float64 `json:"usage"`
}

type healthBackend struct {
	Name      string `json:"name"`
	Healthy   bool   `json:"healthy"`
	LatencyMs int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	enableGlob := flag.String("enable", "", "glob pattern to mark matching backends as available (e.g. 'aws-*')")
	disableGlob := flag.String("disable", "", "glob pattern to mark matching backends as exhausted (e.g. 'aws-*')")
	health := flag.Bool("health", false, "run active health probe (GET /v1/models) and sync state to gateway")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	gatewayURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
	client := &http.Client{Timeout: 15 * time.Second}

	// Active health probe mode: check each backend's reachability and sync.
	if *health {
		runHealthProbe(client, cfg, gatewayURL)
		return
	}

	// Handle --enable / --disable glob overrides without running quota checks.
	if *enableGlob != "" || *disableGlob != "" {
		var results []quotaBackend
		for _, b := range cfg.Backends {
			if *enableGlob != "" {
				if matched, _ := filepath.Match(*enableGlob, b.Name); matched {
					log.Printf("[%s] marking as available (--enable %s)", b.Name, *enableGlob)
					results = append(results, quotaBackend{Name: b.Name, Exhausted: false})
				}
			}
			if *disableGlob != "" {
				if matched, _ := filepath.Match(*disableGlob, b.Name); matched {
					log.Printf("[%s] marking as exhausted (--disable %s)", b.Name, *disableGlob)
					results = append(results, quotaBackend{Name: b.Name, Exhausted: true})
				}
			}
		}
		if len(results) == 0 {
			log.Println("no backends matched the glob pattern")
			return
		}
		if err := syncToServer(client, gatewayURL, cfg.Auth.SessionSecret, results); err != nil {
			log.Fatalf("sync to server: %v", err)
		}
		log.Println("done")
		return
	}

	var results []quotaBackend
	for _, b := range cfg.Backends {
		if !b.Enabled {
			continue
		}

		baseURL := strings.TrimRight(b.URL, "/")
		usage, limit, err := fetchBilling(client, baseURL, b.APIKey)
		if err != nil {
			log.Printf("[%s] billing query failed: %v, falling back to /v1/models", b.Name, err)
			if checkModels(client, baseURL, b.APIKey) {
				log.Printf("[%s] /v1/models OK, marking as available", b.Name)
				results = append(results, quotaBackend{Name: b.Name, Exhausted: false})
			} else {
				log.Printf("[%s] /v1/models also failed, marking as exhausted", b.Name)
				results = append(results, quotaBackend{Name: b.Name, Exhausted: true})
			}
			continue
		}

		exhausted := limit > 0 && usage >= limit
		if exhausted {
			log.Printf("[%s] EXHAUSTED: usage=%.2f limit=%.2f", b.Name, usage, limit)
		} else {
			log.Printf("[%s] OK: usage=%.2f limit=%.2f", b.Name, usage, limit)
		}
		results = append(results, quotaBackend{Name: b.Name, Exhausted: exhausted, Limit: limit, Usage: usage})
	}

	if len(results) == 0 {
		log.Println("no backends to check")
		return
	}

	if err := syncToServer(client, gatewayURL, cfg.Auth.SessionSecret, results); err != nil {
		log.Fatalf("sync to server: %v", err)
	}
	log.Println("quota sync complete")
}

// runHealthProbe actively probes each enabled backend with GET /v1/models,
// measures latency, and syncs the result to the gateway.
func runHealthProbe(client *http.Client, cfg *config.Config, gatewayURL string) {
	var results []healthBackend
	for _, b := range cfg.Backends {
		if !b.Enabled {
			continue
		}
		baseURL := strings.TrimRight(b.URL, "/")
		start := time.Now()
		err := probeModels(client, baseURL, b.APIKey)
		latency := time.Since(start).Milliseconds()
		if err != nil {
			log.Printf("[%s] health FAIL (%dms): %v", b.Name, latency, err)
			results = append(results, healthBackend{Name: b.Name, Healthy: false, LatencyMs: latency, Error: err.Error()})
		} else {
			log.Printf("[%s] health OK (%dms)", b.Name, latency)
			results = append(results, healthBackend{Name: b.Name, Healthy: true, LatencyMs: latency})
		}
	}

	if len(results) == 0 {
		log.Println("no backends to probe")
		return
	}

	if err := syncHealthToServer(client, gatewayURL, cfg.Auth.SessionSecret, results); err != nil {
		log.Fatalf("sync health to server: %v", err)
	}
	log.Println("health sync complete")
}

// probeModels performs a GET /v1/models and returns an error if unreachable/non-200.
func probeModels(client *http.Client, baseURL, apiKey string) error {
	_, err := doGet(client, baseURL+"/v1/models", apiKey)
	return err
}

// syncHealthToServer posts health probe results to the gateway.
func syncHealthToServer(client *http.Client, gatewayURL, secret string, backends []healthBackend) error {
	body, err := json.Marshal(map[string]any{"backends": backends})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, gatewayURL+"/admin/api/backends/health", bytes.NewReader(body))
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
	log.Printf("server response: %v", result)
	return nil
}

func fetchBilling(client *http.Client, baseURL, apiKey string) (usage, limit float64, err error) {
	usageResp, err := doGet(client, baseURL+"/v1/dashboard/billing/usage", apiKey)
	if err != nil {
		return 0, 0, fmt.Errorf("fetch usage: %w", err)
	}
	var u billingUsage
	if err := json.Unmarshal(usageResp, &u); err != nil {
		return 0, 0, fmt.Errorf("decode usage: %w", err)
	}

	subResp, err := doGet(client, baseURL+"/v1/dashboard/billing/subscription", apiKey)
	if err != nil {
		return 0, 0, fmt.Errorf("fetch subscription: %w", err)
	}
	var s billingSubscription
	if err := json.Unmarshal(subResp, &s); err != nil {
		return 0, 0, fmt.Errorf("decode subscription: %w", err)
	}

	return u.TotalUsage, s.HardLimitUSD, nil
}

func checkModels(client *http.Client, baseURL, apiKey string) bool {
	_, err := doGet(client, baseURL+"/v1/models", apiKey)
	return err == nil
}

func doGet(client *http.Client, url, apiKey string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func syncToServer(client *http.Client, gatewayURL, secret string, backends []quotaBackend) error {
	body, err := json.Marshal(map[string]any{"backends": backends})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, gatewayURL+"/admin/api/backends/quota", bytes.NewReader(body))
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
	log.Printf("server response: %v", result)
	return nil
}
