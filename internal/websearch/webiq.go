package websearch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wjzhangq/claude-gateway/config"
)

// WebIQClient queries Microsoft AI Web Search API and normalizes results.
type WebIQClient struct {
	cfg  config.WebSearchConfig
	http *http.Client
}

// NewWebIQClient builds a WebIQ client from config.
func NewWebIQClient(cfg config.WebSearchConfig) *WebIQClient {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &WebIQClient{
		cfg:  cfg,
		http: &http.Client{Timeout: timeout},
	}
}

// webIQRequest is the request payload for Microsoft AI Web Search API.
type webIQRequest struct {
	Query         string `json:"query"`
	MaxResults    int    `json:"maxResults,omitempty"`
	Language      string `json:"language,omitempty"`
	Region        string `json:"region,omitempty"`
	MaxLength     int    `json:"maxLength,omitempty"`
	ContentFormat string `json:"contentFormat,omitempty"`
	SafeSearch    string `json:"safeSearch,omitempty"`
}

// webIQResponse is the subset of Microsoft AI Web Search API's JSON format we read.
type webIQResponse struct {
	WebResults []webIQResult `json:"webResults"`
	RiskLevel  string        `json:"riskLevel"`
	TraceID    string        `json:"traceId"`
}

type webIQResult struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	Domain        string `json:"domain"`
	Content       string `json:"content"`
	CrawledAt     string `json:"crawledAt"`
	LastUpdatedAt string `json:"lastUpdatedAt"`
	Language      string `json:"language"`
	IsAdult       bool   `json:"isAdult"`
}

// Search runs a query against Microsoft AI Web Search API and returns normalized results.
func (c *WebIQClient) Search(ctx context.Context, query, lang string, allowed, blocked []string) ([]Result, error) {
	if lang == "" {
		lang = c.cfg.Language
	}

	maxResults := c.cfg.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}

	maxLength := c.cfg.SnippetMaxChars
	if maxLength <= 0 {
		maxLength = 3000
	}

	// Build request payload
	payload := webIQRequest{
		Query:         query,
		MaxResults:    maxResults,
		Language:      lang,
		Region:        "US",
		MaxLength:     maxLength,
		ContentFormat: "passage",
		SafeSearch:    "strict",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("webiq: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.SearchURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}

	req.Header.Set("host", "api.microsoft.ai")
	if c.cfg.Authorization != "" {
		req.Header.Set("x-apikey", c.cfg.Authorization)
	}
	req.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("webiq: status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var wr webIQResponse
	if err := json.Unmarshal(bodyBytes, &wr); err != nil {
		return nil, fmt.Errorf("webiq: decode: %w", err)
	}

	snippetMax := c.cfg.SnippetMaxChars
	if snippetMax <= 0 {
		snippetMax = 3000
	}

	out := make([]Result, 0, len(wr.WebResults))
	for _, r := range wr.WebResults {
		// Filter adult content
		if r.IsAdult {
			continue
		}

		// Apply domain filters using the domain field (since url may be a redirect)
		urlToFilter := r.URL
		if r.Domain != "" {
			// Use domain field for filtering when available (more accurate than redirect URL)
			urlToFilter = "https://" + r.Domain
		}
		if !hostAllowed(urlToFilter, allowed, blocked) {
			continue
		}

		content := r.Content
		if len(content) > snippetMax {
			content = content[:snippetMax]
		}

		// Use CrawledAt as published date, fallback to LastUpdatedAt
		publishedAt := r.CrawledAt
		if publishedAt == "" {
			publishedAt = r.LastUpdatedAt
		}

		// Decode redirect URL to get the real URL
		realURL := decodeRedirectURL(r.URL)

		out = append(out, Result{
			Title:       r.Title,
			URL:         realURL,
			Content:     content,
			PublishedAt: publishedAt,
		})
	}

	return out, nil
}

// decodeRedirectURL attempts to extract the real URL from a Bing redirect link.
// The redirect format is: https://api.microsoftol.com/ck/a?...&u=a1<base64>
// where <base64> is the base64-encoded original URL (after removing "a1" prefix).
// Returns the original URL if it's not a redirect, or the decoded URL if successful.
func decodeRedirectURL(rawURL string) string {
	if !strings.Contains(rawURL, "api.microsoftol.com/ck/a") {
		return rawURL // Not a redirect link
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	// Extract the "u" parameter
	uParam := parsed.Query().Get("u")
	if uParam == "" || len(uParam) < 2 {
		return rawURL
	}

	// Remove "a1" prefix (Bing's encoding marker)
	if !strings.HasPrefix(uParam, "a1") {
		return rawURL
	}
	encoded := uParam[2:]
	if encoded == "" {
		return rawURL
	}

	// Base64 decode (use RawStdEncoding for unpadded base64)
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		// Try standard encoding as fallback
		decoded, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return rawURL
		}
	}

	// Sanity check: the decoded value must be an absolute http(s) URL with a
	// host. Anything else means we misread the parameter, so keep the original
	// rather than handing the model a broken or empty link.
	decodedURL := string(decoded)
	parsedDecoded, err := url.Parse(decodedURL)
	if err != nil || parsedDecoded.Host == "" {
		return rawURL
	}
	if parsedDecoded.Scheme != "http" && parsedDecoded.Scheme != "https" {
		return rawURL
	}

	return decodedURL
}
