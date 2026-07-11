package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// healthCheckModel is a real model used to verify a backend can actually serve
// inference when the lightweight /v1/models probe fails. Kept in sync with
// cmd/check, which calls ProbeReachable via this shared routine.
const healthCheckModel = "claude-sonnet-4-6"

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
	code, ra, _, err := ProbeReachableDetailed(client, baseURL, apiKey)
	return code, ra, err
}

// ProbeReachableDetailed is ProbeReachable that also reports whether the
// (billable) inference fallback was used, so callers can attribute probe cost
// only when a real /v1/messages request was actually sent (FR-010).
func ProbeReachableDetailed(client *http.Client, baseURL, apiKey string) (observedCode int, retryAfter string, usedInference bool, err error) {
	baseURL = strings.TrimRight(baseURL, "/")

	code, ra, mErr := probeModels(client, baseURL, apiKey)
	if mErr == nil {
		return code, ra, false, nil
	}

	// Models listing failed or is unsupported — confirm with a real message.
	msgCode, msgRA, msgErr := probeMessages(client, baseURL, apiKey)
	if msgErr != nil {
		// Prefer a real HTTP status if the message probe produced one.
		if msgCode != 0 {
			return msgCode, msgRA, true, msgErr
		}
		return code, ra, true, msgErr
	}
	return msgCode, msgRA, true, nil
}

// probeModels performs GET /v1/models. Returns the status code (0 on transport
// error), any Retry-After header, and an error for transport failure or non-200.
func probeModels(client *http.Client, baseURL, apiKey string) (int, string, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/models", nil)
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
func probeMessages(client *http.Client, baseURL, apiKey string) (int, string, error) {
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

	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/messages", bytes.NewReader(reqBody))
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
