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
	"github.com/wjzhangq/claude-gateway/internal/ipgeo"
	"github.com/wjzhangq/claude-gateway/internal/proxy"
)

// probeReachable delegates to the shared reachability routine so cmd/check and
// the server's startup validation apply identical health logic (FR-011).
func probeReachable(client *http.Client, baseURL, apiKey string) (int, string, bool, error) {
	return proxy.ProbeReachableDetailed(client, baseURL, apiKey)
}

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
	Name         string  `json:"name"`
	Healthy      bool    `json:"healthy"`
	LatencyMs    int64   `json:"latency_ms"`
	Error        string  `json:"error,omitempty"`
	ObservedCode int     `json:"observed_code,omitempty"`  // HTTP status the probe saw (0 = transport)
	RetryAfter   string  `json:"retry_after,omitempty"`    // raw Retry-After header, if any
	ProbeCostUSD float64 `json:"probe_cost_usd,omitempty"` // estimated cost of an inference probe
}

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	enableGlob := flag.String("enable", "", "glob pattern to mark matching backends as available (e.g. 'aws-*')")
	disableGlob := flag.String("disable", "", "glob pattern to mark matching backends as exhausted (e.g. 'aws-*')")
	health := flag.Bool("health", false, "run active health probe (GET /v1/models) and sync state to gateway")
	ip2region := flag.Bool("ip2region", false, "resolve city for usage-log IPs that have none yet (via ip9.com.cn) and update the gateway cache")
	analyze := flag.Bool("analyze", false, "consume the pending-analysis queue: classify each successful request (rules → Haiku fallback) and write verdicts back to the gateway")
	recent := flag.Bool("recent", false, "with --analyze: process only the most recent batch_size pending records (newest first) in a single pass, instead of draining the whole queue oldest-first")
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

	// IP → city resolution mode: pull unresolved IPs from the gateway, look each
	// up via ip9.com.cn, and push the discovered cities back so future request
	// records carry a city.
	if *ip2region {
		runIP2Region(client, cfg, gatewayURL)
		return
	}

	// Offline abuse-analysis mode: pull the pending queue, classify each record
	// (rules first, Haiku only when unsure), and write verdicts back (feature 004).
	if *analyze {
		runAnalyze(client, cfg, gatewayURL, *recent)
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

// probeMessageCostUSD is a conservative flat estimate of a single max_tokens=1
// health probe's cost, attributed to the probed backend (FR-010). The probe is
// intentionally tiny; this is an upper-bound placeholder pending per-backend rates.
const probeMessageCostUSD = 0.0002

// runHealthProbe actively probes each enabled backend via the shared reachability
// routine (GET /v1/models, falling back to a minimal /v1/messages inference call),
// measures latency, and syncs the result to the gateway. Backends the gateway
// reports as quota-isolated are skipped entirely so probing never spends real
// budget on — or worsens the quota of — an already-exhausted backend (FR-008).
func runHealthProbe(client *http.Client, cfg *config.Config, gatewayURL string) {
	quotaIsolated := fetchQuotaIsolated(client, gatewayURL, cfg.Auth.SessionSecret)

	var results []healthBackend
	for _, b := range cfg.Backends {
		if !b.Enabled {
			continue
		}
		if quotaIsolated[b.Name] {
			// Quota-isolated: recovers via daily-reset / TTL expiry, never a probe.
			log.Printf("[%s] quota-isolated: skipping billable probe (recovers via reset/TTL)", b.Name)
			results = append(results, healthBackend{Name: b.Name, Healthy: false, Error: "quota-isolated (probe skipped)"})
			continue
		}

		baseURL := strings.TrimRight(b.URL, "/")
		start := time.Now()
		code, retryAfter, usedInference, err := probeReachable(client, baseURL, b.APIKey)
		latency := time.Since(start).Milliseconds()
		// Only a real /v1/messages inference probe costs money; a pure
		// /v1/models 200 is free (FR-010).
		var probeCost float64
		if usedInference {
			probeCost = probeMessageCostUSD
		}
		if err != nil {
			log.Printf("[%s] health FAIL (%dms) code=%d: %v", b.Name, latency, code, err)
			results = append(results, healthBackend{
				Name: b.Name, Healthy: false, LatencyMs: latency,
				Error: err.Error(), ObservedCode: code, RetryAfter: retryAfter,
				ProbeCostUSD: probeCost,
			})
		} else {
			log.Printf("[%s] health OK (%dms) code=%d", b.Name, latency, code)
			results = append(results, healthBackend{
				Name: b.Name, Healthy: true, LatencyMs: latency, ObservedCode: code,
				ProbeCostUSD: probeCost,
			})
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

// fetchQuotaIsolated asks the gateway which backends are currently quota-isolated
// (state=isolated AND (quota_exhausted OR last_http_code==403)) so they can be
// excluded from billable probing. Returns an empty set on any error (fail-open:
// probing is still safe, just not skipped).
func fetchQuotaIsolated(client *http.Client, gatewayURL, secret string) map[string]bool {
	out := map[string]bool{}
	req, err := http.NewRequest(http.MethodGet, gatewayURL+"/admin/api/backends", nil)
	if err != nil {
		return out
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("fetch backend list failed (probing all): %v", err)
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out
	}
	var payload struct {
		Backends []struct {
			Name           string `json:"name"`
			State          string `json:"state"`
			QuotaExhausted bool   `json:"quota_exhausted"`
			LastHTTPCode   int    `json:"last_http_code"`
		} `json:"backends"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return out
	}
	for _, b := range payload.Backends {
		if b.State == "isolated" && (b.QuotaExhausted || b.LastHTTPCode == 403) {
			out[b.Name] = true
		}
	}
	return out
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

// runIP2Region pulls the gateway's list of city-less public IPs, resolves each
// via ip9.com.cn, and posts the discovered cities back so future request records
// carry a city. The gateway owns the cache file; check only talks HTTP to it.
func runIP2Region(client *http.Client, cfg *config.Config, gatewayURL string) {
	secret := cfg.Auth.SessionSecret

	ips, err := fetchUnresolvedIPs(client, gatewayURL, secret)
	if err != nil {
		log.Fatalf("fetch unresolved IPs: %v", err)
	}
	if len(ips) == 0 {
		log.Println("no unresolved IPs")
		return
	}
	log.Printf("resolving %d unresolved IP(s) via ip9.com.cn", len(ips))

	// ip9.com.cn has no published key but is rate-sensitive; space out lookups.
	lookupClient := &http.Client{Timeout: 10 * time.Second}
	resolved := make([]ipCity, 0, len(ips))
	for _, ip := range ips {
		city, err := ipgeo.LookupCity(lookupClient, ip)
		if err != nil {
			log.Printf("[%s] lookup failed: %v", ip, err)
			continue
		}
		if city == "" {
			log.Printf("[%s] no city (reserved/invalid/unknown)", ip)
			continue
		}
		log.Printf("[%s] -> %s", ip, city)
		resolved = append(resolved, ipCity{IP: ip, City: city})
		time.Sleep(300 * time.Millisecond)
	}

	if len(resolved) == 0 {
		log.Println("no cities resolved")
		return
	}
	if err := postResolvedIPs(client, gatewayURL, secret, resolved); err != nil {
		log.Fatalf("post resolved IPs: %v", err)
	}
	log.Printf("done: updated %d IP(s)", len(resolved))
}

// ipCity pairs an IP with its resolved city for the resolve request body.
type ipCity struct {
	IP   string `json:"ip"`
	City string `json:"city"`
}

// fetchUnresolvedIPs GETs the gateway's list of public IPs that have no city yet.
func fetchUnresolvedIPs(client *http.Client, gatewayURL, secret string) ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, gatewayURL+"/admin/api/ipgeo/unresolved", nil)
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
		IPs []string `json:"ips"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.IPs, nil
}

// postResolvedIPs POSTs discovered ip->city pairs back to the gateway.
func postResolvedIPs(client *http.Client, gatewayURL, secret string, resolved []ipCity) error {
	body, err := json.Marshal(map[string]any{"resolved": resolved})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, gatewayURL+"/admin/api/ipgeo/resolve", bytes.NewReader(body))
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
