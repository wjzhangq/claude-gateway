package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wjzhangq/claude-gateway/config"
)

func newTestClient(url string, maxResults, snippetMax int) *Client {
	return NewClient(config.WebSearchConfig{
		SearchURL:       url,
		MaxResults:      maxResults,
		SnippetMaxChars: snippetMax,
		Timeout:         2 * time.Second,
		Language:        "zh-CN",
	})
}

func TestSearchSortAndTruncate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			t.Errorf("missing format=json: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"title":"low","url":"https://a.com","content":"aaaaaaaaaa","score":0.1},
			{"title":"high","url":"https://b.com","content":"bbbbbbbbbb","score":0.9},
			{"title":"mid","url":"https://c.com","content":"cccccccccc","score":0.5}
		]}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, 2, 4)
	res, err := c.Search(context.Background(), "q", "", nil, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("want 2 results (max), got %d", len(res))
	}
	if res[0].Title != "high" || res[1].Title != "mid" {
		t.Fatalf("wrong sort order: %s, %s", res[0].Title, res[1].Title)
	}
	if len(res[0].Content) != 4 {
		t.Fatalf("content not truncated to 4: %q", res[0].Content)
	}
}

func TestSearchDomainFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[
			{"title":"keep","url":"https://good.com/x","content":"c","score":0.9},
			{"title":"drop","url":"https://bad.com/y","content":"c","score":0.8},
			{"title":"sub","url":"https://www.good.com/z","content":"c","score":0.7}
		]}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, 10, 100)

	// blocked wins
	res, _ := c.Search(context.Background(), "q", "", nil, []string{"bad.com"})
	for _, r := range res {
		if r.Title == "drop" {
			t.Fatal("blocked domain not filtered")
		}
	}
	if len(res) != 2 {
		t.Fatalf("want 2 after block, got %d", len(res))
	}

	// allow-list: only good.com and its subdomains
	res, _ = c.Search(context.Background(), "q", "", []string{"good.com"}, nil)
	if len(res) != 2 {
		t.Fatalf("want 2 allowed (good.com + www.good.com), got %d", len(res))
	}
}

func TestSearchUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, 5, 100)
	_, err := c.Search(context.Background(), "q", "", nil, nil)
	if err == nil {
		t.Fatal("expected error on upstream 502")
	}
}

func TestSearchAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	c := NewClient(config.WebSearchConfig{
		SearchURL:     srv.URL,
		Authorization: "Bearer secret-token",
		Timeout:       2 * time.Second,
	})
	if _, err := c.Search(context.Background(), "q", "", nil, nil); err != nil {
		t.Fatalf("search: %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("Authorization header = %q", gotAuth)
	}
}

func TestHostMatches(t *testing.T) {
	cases := []struct {
		host, domain string
		want         bool
	}{
		{"good.com", "good.com", true},
		{"www.good.com", "good.com", true},
		{"evilgood.com", "good.com", false},
		{"good.com", "", false},
		{"", "good.com", false},
	}
	for _, tc := range cases {
		if got := hostMatches(tc.host, tc.domain); got != tc.want {
			t.Errorf("hostMatches(%q,%q) = %v, want %v", tc.host, tc.domain, got, tc.want)
		}
	}
}
