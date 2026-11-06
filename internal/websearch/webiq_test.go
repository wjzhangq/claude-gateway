package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/wjzhangq/claude-gateway/config"
)

func TestWebIQClient_Search(t *testing.T) {
	// Mock response from Microsoft AI Web Search API
	mockResponse := map[string]interface{}{
		"webResults": []map[string]interface{}{
			{
				"title":     "Claude by Anthropic",
				"url":       "https://claude.com/",
				"domain":    "claude.com",
				"content":   "Claude is an artificial intelligence assistant...",
				"crawledAt": "2026-11-06T07:25:00.0000000Z",
				"language":  "en",
				"isAdult":   false,
			},
			{
				"title":     "Home \\ Anthropic",
				"url":       "https://www.anthropic.com/",
				"domain":    "www.anthropic.com",
				"content":   "AI research and products that put safety at the frontier...",
				"crawledAt": "2026-11-06T08:34:00.0000000Z",
				"language":  "en",
				"isAdult":   false,
			},
		},
		"riskLevel": "low",
		"traceId":   "test-trace-id",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request headers
		if r.Header.Get("x-apikey") != "test-key" {
			t.Errorf("expected x-apikey=test-key, got %s", r.Header.Get("x-apikey"))
		}
		if r.Header.Get("content-type") != "application/json" {
			t.Errorf("expected content-type=application/json, got %s", r.Header.Get("content-type"))
		}

		// Verify request body
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if reqBody["query"] != "test query" {
			t.Errorf("expected query='test query', got %v", reqBody["query"])
		}

		// Send mock response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	cfg := config.WebSearchConfig{
		Provider:        "webiq",
		SearchURL:       server.URL,
		Authorization:   "test-key",
		Language:        "en",
		MaxResults:      10,
		SnippetMaxChars: 500,
	}

	client := NewWebIQClient(cfg)
	results, err := client.Search(context.Background(), "test query", "", nil, nil)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if results[0].Title != "Claude by Anthropic" {
		t.Errorf("expected first title='Claude by Anthropic', got %q", results[0].Title)
	}
	if results[0].URL != "https://claude.com/" {
		t.Errorf("expected first url='https://claude.com/', got %q", results[0].URL)
	}
}

func TestWebIQClient_FilterAdult(t *testing.T) {
	mockResponse := map[string]interface{}{
		"webResults": []map[string]interface{}{
			{
				"title":   "Safe Content",
				"url":     "https://example.com/safe",
				"content": "Safe content",
				"isAdult": false,
			},
			{
				"title":   "Adult Content",
				"url":     "https://example.com/adult",
				"content": "Adult content",
				"isAdult": true,
			},
		},
		"riskLevel": "low",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	cfg := config.WebSearchConfig{
		Provider:  "webiq",
		SearchURL: server.URL,
	}

	client := NewWebIQClient(cfg)
	results, err := client.Search(context.Background(), "test", "", nil, nil)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// Adult content should be filtered out
	if len(results) != 1 {
		t.Fatalf("expected 1 result after filtering, got %d", len(results))
	}
	if results[0].Title != "Safe Content" {
		t.Errorf("expected safe content, got %q", results[0].Title)
	}
}

func TestWebIQClient_DomainFiltering(t *testing.T) {
	mockResponse := map[string]interface{}{
		"webResults": []map[string]interface{}{
			{"title": "Example 1", "url": "https://example.com/1", "content": "c1", "isAdult": false},
			{"title": "Test 1", "url": "https://test.com/1", "content": "c2", "isAdult": false},
			{"title": "Bad 1", "url": "https://bad.com/1", "content": "c3", "isAdult": false},
		},
		"riskLevel": "low",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	cfg := config.WebSearchConfig{
		Provider:  "webiq",
		SearchURL: server.URL,
	}

	client := NewWebIQClient(cfg)

	// Test blocked domains
	results, _ := client.Search(context.Background(), "q", "", nil, []string{"bad.com"})
	if len(results) != 2 {
		t.Errorf("expected 2 results after blocking bad.com, got %d", len(results))
	}
	for _, r := range results {
		if r.URL == "https://bad.com/1" {
			t.Errorf("bad.com should have been filtered")
		}
	}

	// Test allowed domains
	results, _ = client.Search(context.Background(), "q", "", []string{"example.com"}, nil)
	if len(results) != 1 {
		t.Errorf("expected 1 result when allowing only example.com, got %d", len(results))
	}
	if results[0].URL != "https://example.com/1" {
		t.Errorf("expected example.com result, got %s", results[0].URL)
	}
}

// TestWebIQClient_RealAPI is an opt-in integration test against the live
// Microsoft AI endpoint. It is skipped unless WEBIQ_API_KEY is set, so the
// default test run stays offline and no key is committed:
//
//	WEBIQ_API_KEY=<key> go test -v -run TestWebIQClient_RealAPI ./internal/websearch
func TestWebIQClient_RealAPI(t *testing.T) {
	apiKey := os.Getenv("WEBIQ_API_KEY")
	if apiKey == "" {
		t.Skip("WEBIQ_API_KEY not set; skipping live API test")
	}

	cfg := config.WebSearchConfig{
		Provider:        "webiq",
		SearchURL:       "https://api.microsoft.ai/v3/search/web",
		Authorization:   apiKey,
		Language:        "en",
		MaxResults:      3,
		SnippetMaxChars: 200,
	}

	client := NewWebIQClient(cfg)
	results, err := client.Search(context.Background(), "Anthropic Claude", "", nil, nil)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected at least 1 result, got 0")
	}

	for i, r := range results {
		t.Logf("%d. %s", i+1, r.Title)
		t.Logf("   URL: %s", r.URL)
		
		// Verify URL was decoded (should not contain api.microsoftol.com)
		if strings.Contains(r.URL, "api.microsoftol.com") {
			t.Errorf("result %d: URL was not decoded: %s", i+1, r.URL)
		}
		
		// Verify it's a proper http(s) URL
		if !strings.HasPrefix(r.URL, "http://") && !strings.HasPrefix(r.URL, "https://") {
			t.Errorf("result %d: URL missing http(s) scheme: %s", i+1, r.URL)
		}
	}
}
