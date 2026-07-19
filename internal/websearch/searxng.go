package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/wjzhangq/claude-gateway/config"
)

// Client queries a SearXNG instance and normalizes its JSON results. It mirrors
// the lightweight http.Client pattern used by internal/classify/haiku.go.
type Client struct {
	cfg  config.WebSearchConfig
	http *http.Client
}

// NewClient builds a SearXNG client from config. Timeout falls back to 10s when
// unset so a missing value can't hang a request forever.
func NewClient(cfg config.WebSearchConfig) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: timeout},
	}
}

// searxResponse is the subset of SearXNG's JSON format we read.
type searxResponse struct {
	Results []searxResult `json:"results"`
}

type searxResult struct {
	Title         string  `json:"title"`
	URL           string  `json:"url"`
	Content       string  `json:"content"`
	PublishedDate string  `json:"publishedDate"`
	Score         float64 `json:"score"`
}

// Search runs a query against SearXNG and returns up to MaxResults normalized
// results, sorted by score descending, with content truncated to
// SnippetMaxChars. lang overrides the configured default when non-empty.
// allowed/blocked filter result URLs by host suffix (SearXNG has no native
// support, so the gateway filters). On transport/decoding failure it returns an
// error the caller maps to a web_search_tool_result_error.
func (c *Client) Search(ctx context.Context, query, lang string, allowed, blocked []string) ([]Result, error) {
	if lang == "" {
		lang = c.cfg.Language
	}
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "json")
	if lang != "" {
		q.Set("language", lang)
	}
	endpoint := c.cfg.SearchURL + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if c.cfg.Authorization != "" {
		req.Header.Set("Authorization", c.cfg.Authorization)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("searxng: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var sr searxResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, fmt.Errorf("searxng: decode: %w", err)
	}

	results := sr.Results
	// Sort by score descending; SearXNG usually returns pre-sorted, but be safe.
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	max := c.cfg.MaxResults
	if max <= 0 {
		max = 8
	}
	snippetMax := c.cfg.SnippetMaxChars
	if snippetMax <= 0 {
		snippetMax = 800
	}

	out := make([]Result, 0, max)
	for _, r := range results {
		if !hostAllowed(r.URL, allowed, blocked) {
			continue
		}
		content := r.Content
		if len(content) > snippetMax {
			content = content[:snippetMax]
		}
		out = append(out, Result{
			Title:       r.Title,
			URL:         r.URL,
			Content:     content,
			PublishedAt: r.PublishedDate,
		})
		if len(out) >= max {
			break
		}
	}
	return out, nil
}

// hostAllowed reports whether a result URL passes the allowed/blocked domain
// filters. blocked takes precedence. A match is a case-insensitive suffix match
// on the URL host (so "example.com" matches "www.example.com"). A malformed URL
// is rejected only when an allowed list is set (fail-closed for allow-listing).
func hostAllowed(rawURL string, allowed, blocked []string) bool {
	u, err := url.Parse(rawURL)
	host := ""
	if err == nil {
		host = strings.ToLower(u.Hostname())
	}
	for _, d := range blocked {
		if hostMatches(host, d) {
			return false
		}
	}
	if len(allowed) == 0 {
		return true
	}
	if host == "" {
		return false
	}
	for _, d := range allowed {
		if hostMatches(host, d) {
			return true
		}
	}
	return false
}

// hostMatches reports whether host equals domain or is a subdomain of it.
func hostMatches(host, domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" || host == "" {
		return false
	}
	return host == domain || strings.HasSuffix(host, "."+domain)
}
