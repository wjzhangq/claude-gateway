package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
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

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	gatewayURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
	client := &http.Client{Timeout: 15 * time.Second}

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
