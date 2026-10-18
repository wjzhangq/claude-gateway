package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"testing"
)

func TestIsClaudeCodeUA(t *testing.T) {
	if !isClaudeCodeUA("claude-cli/2.1.0 (external, cli)") {
		t.Fatal("claude-cli UA should match")
	}
	if !isClaudeCodeUA("Claude-Code/1.0") {
		t.Fatal("Claude-Code UA should match case-insensitively")
	}
	if isClaudeCodeUA("") {
		t.Fatal("empty UA should not match")
	}
	if isClaudeCodeUA("OpenAI/JS 4.0") {
		t.Fatal("OpenAI/JS should not match")
	}
}

func TestMaybeRewriteClaudeLoginQuota(t *testing.T) {
	const claudeUA = "claude-cli/2.1.0 (external, cli)"
	loginBody := []byte(`{"type":"error","error":{"message":"Please run /login · API Error: 403"}}`)

	resp := loginQuotaResponse(loginBody)
	if !maybeRewriteClaudeLoginQuota(claudeUA, resp) {
		t.Fatal("claude login-quota 403 should be rewritten")
	}
	if resp.StatusCode != statusOverloaded {
		t.Fatalf("status = %d, want %d", resp.StatusCode, statusOverloaded)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, overloadedErrorBody) {
		t.Fatalf("body = %s, want overloaded JSON", got)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" {
		t.Fatalf("Content-Encoding = %q, want empty", enc)
	}
	if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(overloadedErrorBody)) {
		t.Fatalf("Content-Length = %q, want %d", cl, len(overloadedErrorBody))
	}

	unchanged := loginQuotaResponse(loginBody)
	if maybeRewriteClaudeLoginQuota("OpenAI/JS 4.0", unchanged) {
		t.Fatal("non-claude UA should not be rewritten")
	}
	if unchanged.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", unchanged.StatusCode)
	}
	got, err = io.ReadAll(unchanged.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, loginBody) {
		t.Fatalf("non-claude body changed: %s", got)
	}

	other := []byte(`{"type":"error","error":{"type":"permission_error","message":"forbidden"}}`)
	genuine := loginQuotaResponse(other)
	if maybeRewriteClaudeLoginQuota(claudeUA, genuine) {
		t.Fatal("unrelated 403 should not be rewritten")
	}
	if genuine.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", genuine.StatusCode)
	}
	got, err = io.ReadAll(genuine.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, other) {
		t.Fatalf("unrelated 403 body changed: %s", got)
	}
}

func TestMaybeRewriteClaudeLoginQuota_LeavesSuccessBody(t *testing.T) {
	body := []byte(`{"ok":true}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	if maybeRewriteClaudeLoginQuota("claude-cli/2.1.0", resp) {
		t.Fatal("success response should not be rewritten")
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("success body consumed or changed: %s", got)
	}
}

func TestRewriteClaudeLoginQuotaBody(t *testing.T) {
	const claudeUA = "claude-cli/2.1.0 (external, cli)"
	loginBody := []byte(`Please run /login · API Error: 403`)

	status, body, ok := rewriteClaudeLoginQuotaBody(claudeUA, http.StatusForbidden, loginBody)
	if !ok || status != statusOverloaded || !bytes.Equal(body, overloadedErrorBody) {
		t.Fatalf("rewrite = (%d, %s, %v), want 529 overloaded", status, body, ok)
	}

	status, body, ok = rewriteClaudeLoginQuotaBody("OpenAI/JS", http.StatusForbidden, loginBody)
	if ok || status != http.StatusForbidden || !bytes.Equal(body, loginBody) {
		t.Fatalf("non-claude rewrite = (%d, %s, %v)", status, body, ok)
	}

	other := []byte(`{"error":"invalid api key"}`)
	status, body, ok = rewriteClaudeLoginQuotaBody(claudeUA, http.StatusForbidden, other)
	if ok || status != http.StatusForbidden || !bytes.Equal(body, other) {
		t.Fatalf("unrelated rewrite = (%d, %s, %v)", status, body, ok)
	}
}

func loginQuotaResponse(body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Header: http.Header{
			"Content-Type":     []string{"application/json"},
			"Content-Encoding": []string{"gzip"},
			"Content-Length":   []string{"99"},
		},
		Body: io.NopCloser(bytes.NewReader(body)),
	}
}
