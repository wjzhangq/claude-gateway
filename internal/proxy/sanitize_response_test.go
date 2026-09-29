package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wjzhangq/claude-gateway/internal/sanitize"
)

// TestUpstreamDomainSanitizationInResponse verifies that sensitive upstream
// domains are removed from error responses at the streaming and buffering level.
func TestUpstreamDomainSanitizationInResponse(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		responseBody string
		wantBody     string
		wantSanitize bool
	}{
		{
			name:         "200 OK - no sanitization",
			statusCode:   http.StatusOK,
			responseBody: `{"content":"text from https://zjz-ai.webtrn.cn"}`,
			wantBody:     `{"content":"text from https://zjz-ai.webtrn.cn"}`,
			wantSanitize: false,
		},
		{
			name:         "400 error with zjz-ai domain",
			statusCode:   http.StatusBadRequest,
			responseBody: `{"error":"Request failed at https://zjz-ai.webtrn.cn/v1/messages"}`,
			wantBody:     `{"error":"Request failed at [upstream]"}`,
			wantSanitize: true,
		},
		{
			name:         "500 error with dasheng domain",
			statusCode:   http.StatusInternalServerError,
			responseBody: `{"error":"Timeout at https://dasheng.aibeke.com/api"}`,
			wantBody:     `{"error":"Timeout at [upstream]"}`,
			wantSanitize: true,
		},
		{
			name:         "502 with both domains",
			statusCode:   http.StatusBadGateway,
			responseBody: `{"error":"Failed: zjz-ai.webtrn.cn and dasheng.aibeke.com"}`,
			wantBody:     `{"error":"Failed: [upstream] and [upstream]"}`,
			wantSanitize: true,
		},
		{
			name:         "404 without sensitive domains",
			statusCode:   http.StatusNotFound,
			responseBody: `{"error":"Model not found"}`,
			wantBody:     `{"error":"Model not found"}`,
			wantSanitize: false,
		},
		{
			name:         "429 rate limit with domain in message",
			statusCode:   http.StatusTooManyRequests,
			responseBody: `{"type":"rate_limit_error","message":"Rate limit exceeded at https://zjz-ai.webtrn.cn"}`,
			wantBody:     `{"type":"rate_limit_error","message":"Rate limit exceeded at [upstream]"}`,
			wantSanitize: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the sanitization that happens in streamResponse/bufferResponse
			body := []byte(tt.responseBody)
			if tt.statusCode >= 400 {
				body = sanitize.RemoveUpstreamDomains(body)
			}

			gotBody := string(body)
			if gotBody != tt.wantBody {
				t.Errorf("sanitized body mismatch:\ngot:  %q\nwant: %q", gotBody, tt.wantBody)
			}

			// Verify no sensitive domains remain when they should be sanitized
			if tt.wantSanitize {
				if strings.Contains(gotBody, "zjz-ai.webtrn.cn") || strings.Contains(gotBody, "dasheng.aibeke.com") {
					t.Errorf("response still contains sensitive domains: %s", gotBody)
				}
			}
		})
	}
}

// TestStreamingSanitization verifies sanitization works on chunked streaming responses.
func TestStreamingSanitization(t *testing.T) {
	// Simulate an upstream that returns an error message split across chunks
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusInternalServerError)
		// Write error message in chunks (simulating streaming)
		chunks := []string{
			"data: {\"error\":\"Connection failed to https://",
			"zjz-ai.webtrn.cn",
			"/v1/messages\"}\n\n",
		}
		flusher, _ := w.(http.Flusher)
		for _, chunk := range chunks {
			w.Write([]byte(chunk))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer upstream.Close()

	// Make request and collect response
	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Simulate what streamResponse does: read chunks and sanitize when status >= 400
	var buf bytes.Buffer
	chunk := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(chunk)
		if n > 0 {
			data := chunk[:n]
			// This is what our modified streamResponse does
			if resp.StatusCode >= 400 {
				data = sanitize.RemoveUpstreamDomains(data)
			}
			buf.Write(data)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read error: %v", err)
		}
	}

	got := buf.String()
	if strings.Contains(got, "zjz-ai.webtrn.cn") {
		t.Errorf("streaming response still contains sensitive domain: %s", got)
	}
	if !strings.Contains(got, "[upstream]") {
		t.Errorf("streaming response missing [upstream] replacement: %s", got)
	}
}
