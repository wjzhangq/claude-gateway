package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// billingSyncPeriod matches the former crontab quota job.
const billingSyncPeriod = 15 * time.Minute

// billingSyncLoop pulls dashboard billing for every backend. The first pull
// waits a minute so startup probes finish first; after that it runs on the
// same 15-minute cadence the cron used. A failed read is logged and skipped.
// This loop never calls /v1/models and never writes quotaExhausted.
func (lb *LoadBalancer) billingSyncLoop() {
	client := &http.Client{Timeout: 15 * time.Second}
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		<-timer.C
		lb.syncBilling(client)
		timer.Reset(billingSyncPeriod)
	}
}

func (lb *LoadBalancer) syncBilling(client *http.Client) {
	lb.mu.RLock()
	backends := append([]*Backend(nil), lb.backends...)
	lb.mu.RUnlock()

	for _, b := range backends {
		usage, limit, err := fetchBilling(client, b.URL, b.APIKey)
		if err != nil {
			log.Printf("[backend:%s] billing sync skipped: %v", b.Name, err)
			continue
		}
		lb.RecordBilling(b.Name, limit, usage)

		// The reading already says the account cannot pay for the next request.
		// Take it out here rather than leaving a user to discover the same thing
		// as a 403. An account already resting is left alone: this loop runs
		// every 15 minutes, and re-entering would reset a longer backoff.
		if b.balanceHold.Load() || !billingSpent(usage, limit) {
			continue
		}
		log.Printf("[backend:%s] billing says spent (usage=%.2f limit=%.2f): entering balance rest", b.Name, usage, limit)
		lb.enterBalanceRest(b)
	}
}

// billingSpent reports an account with no headroom left for another request.
// limit <= 0 means no hard limit is configured, which is not an exhausted
// account — the dashboard reports 0 for unmetered keys.
func billingSpent(usage, limit float64) bool {
	return limit > 0 && usage >= limit
}

type billingUsage struct {
	TotalUsage float64 `json:"total_usage"`
}

type billingSubscription struct {
	HardLimitUSD float64 `json:"hard_limit_usd"`
}

func fetchBilling(client *http.Client, baseURL, apiKey string) (usage, limit float64, err error) {
	base := strings.TrimRight(baseURL, "/")
	usageBody, err := billingGet(client, base+"/v1/dashboard/billing/usage", apiKey)
	if err != nil {
		return 0, 0, fmt.Errorf("fetch usage: %w", err)
	}
	var u billingUsage
	if err := json.Unmarshal(usageBody, &u); err != nil {
		return 0, 0, fmt.Errorf("decode usage: %w", err)
	}

	subBody, err := billingGet(client, base+"/v1/dashboard/billing/subscription", apiKey)
	if err != nil {
		return 0, 0, fmt.Errorf("fetch subscription: %w", err)
	}
	var s billingSubscription
	if err := json.Unmarshal(subBody, &s); err != nil {
		return 0, 0, fmt.Errorf("decode subscription: %w", err)
	}
	return u.TotalUsage, s.HardLimitUSD, nil
}

func billingGet(client *http.Client, url, apiKey string) ([]byte, error) {
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}
