package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// probeTimeout bounds a single probe request. Probes now run on the backend's
// own client, whose timeout is sized for streaming (300s), so the deadline has
// to come from the request context instead of the client.
const probeTimeout = 15 * time.Second

// probeContext returns a context carrying the probe deadline.
func probeContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), probeTimeout)
}

// healthCheckModel is a real model used to verify a backend can actually serve
// inference when the lightweight /v1/models probe fails. Kept in sync with
// cmd/check, which calls ProbeReachable via this shared routine.
const healthCheckModel = "claude-sonnet-4-6"

// ProbeMessageCostUSD is a flat estimate of one max_tokens=1 probe. Callers
// attribute it only when that messages request was actually sent.
const ProbeMessageCostUSD = 0.0002

// ProbeReachable determines whether a backend is usable, matching the runtime
// active-probe logic so startup validation and cmd/check agree (FR-011). It
// first tries GET /v1/models; if that fails it falls back to a minimal
// /v1/messages inference call (max_tokens=1) with a real model, because some
// backends can serve inference but do not support the model-listing endpoint.
//
// Returns the observed HTTP status code (0 on transport failure), the raw
// Retry-After header if the upstream sent one, and a non-nil error when the
// backend is judged unreachable. When the models probe fails but the message
// probe succeeds, err is nil and observedCode is the message probe's status.
// The observedCode + retryAfter let callers feed the SAME computeTTL policy the
// passive request path uses (FR-005..FR-007).
func ProbeReachable(client *http.Client, baseURL, apiKey string) (observedCode int, retryAfter string, err error) {
	ctx, cancel := probeContext()
	defer cancel()
	code, ra, _, err := ProbeReachableDetailedContext(ctx, client, baseURL, apiKey)
	return code, ra, err
}

// ProbeReachableDetailed is ProbeReachable that also reports whether the
// (billable) inference fallback was used, so callers can attribute probe cost
// only when a real /v1/messages request was actually sent (FR-010).
func ProbeReachableDetailed(client *http.Client, baseURL, apiKey string) (observedCode int, retryAfter string, usedInference bool, err error) {
	ctx, cancel := probeContext()
	defer cancel()
	return ProbeReachableDetailedContext(ctx, client, baseURL, apiKey)
}

// ProbeReachableDetailedContext is ProbeReachableDetailed with a caller-supplied
// context, so a probe running on a long-timeout client still has its own
// deadline.
func ProbeReachableDetailedContext(ctx context.Context, client *http.Client, baseURL, apiKey string) (observedCode int, retryAfter string, usedInference bool, err error) {
	baseURL = strings.TrimRight(baseURL, "/")

	code, ra, mErr := probeModels(ctx, client, baseURL, apiKey)
	if mErr == nil {
		return code, ra, false, nil
	}

	// 401/403 is an account-level verdict, not a missing endpoint: the key is
	// revoked, suspended, or out of balance, so the inference fallback would
	// fail on the same key and cost a billable request to learn nothing. Judge
	// it unreachable on the cheap probe alone. The verdict is not permanent —
	// the isolation TTL expires, releaseExpired promotes the backend to Probing
	// and real traffic re-decides. ProbeBalance is a separate path and is
	// unaffected, so a negative balance still gets its own self-probe.
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return code, ra, false, mErr
	}

	// Models listing failed or is unsupported — confirm with a real message.
	msgCode, msgRA, msgErr := probeMessages(ctx, client, baseURL, apiKey)
	if msgErr != nil {
		// Prefer a real HTTP status if the message probe produced one.
		if msgCode != 0 {
			return msgCode, msgRA, true, msgErr
		}
		return code, ra, true, msgErr
	}
	return msgCode, msgRA, true, nil
}

// ProbeBalance sends one max_tokens=1 /v1/messages request. It does not call
// GET /v1/models: that endpoint often returns 200 while the account balance is
// still negative, which would look like a recovery.
func ProbeBalance(client *http.Client, baseURL, apiKey string) (int, error) {
	ctx, cancel := probeContext()
	defer cancel()
	code, _, err := probeMessages(ctx, client, strings.TrimRight(baseURL, "/"), apiKey)
	return code, err
}

// probeModels performs GET /v1/models. Returns the status code (0 on transport
// error), any Retry-After header, and an error for transport failure or non-200.
func probeModels(ctx context.Context, client *http.Client, baseURL, apiKey string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-api-key", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	ra := resp.Header.Get("Retry-After")
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, ra, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, ra, nil
}

// probeMessages sends a minimal /v1/messages request (max_tokens=1) with a real
// model and verifies a well-formed message response. This is the strongest
// signal that a backend can route and serve inference. The tiny token budget
// keeps the probe's cost negligible (FR-009).
func probeMessages(ctx context.Context, client *http.Client, baseURL, apiKey string) (int, string, error) {
	reqBody, err := json.Marshal(map[string]any{
		"model":      healthCheckModel,
		"max_tokens": 1,
		"messages": []map[string]any{
			{"role": "user", "content": "ping"},
		},
	})
	if err != nil {
		return 0, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	ra := resp.Header.Get("Retry-After")

	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, ra, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(buf.String()))
	}

	var result struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		return resp.StatusCode, ra, fmt.Errorf("decode response: %w", err)
	}
	if result.Type != "message" {
		return resp.StatusCode, ra, fmt.Errorf("unexpected response shape (type=%q, content=%d)", result.Type, len(result.Content))
	}
	return resp.StatusCode, ra, nil
}
