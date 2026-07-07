package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/wjzhangq/claude-gateway/config"
)

// backendsForTest builds N single-backend configs for white-box tests.
func backendsForTest(weights ...int) []config.BackendAPI {
	cfgs := make([]config.BackendAPI, len(weights))
	for i, w := range weights {
		cfgs[i] = config.BackendAPI{
			Name:    "backend",
			URL:     "http://localhost",
			APIKey:  "test",
			Weight:  w,
			Enabled: true,
		}
	}
	return cfgs
}

// singleBackend builds a one-backend config for the white-box tests in this
// file (makeBackends lives in the external proxy_test package and isn't visible
// here).
func singleBackend(weight int) []config.BackendAPI {
	return []config.BackendAPI{
		{Name: "backend", URL: "http://localhost", APIKey: "test", Weight: weight, Enabled: true},
	}
}

// --- fix #2: thinking-variant normalization ---

func TestStripThinkingSuffix(t *testing.T) {
	cases := []struct {
		in       string
		wantBase string
		wantOK   bool
	}{
		{"claude-sonnet-4-5-20250929-thinking", "claude-sonnet-4-5-20250929", true},
		{"claude-opus-4-8-thinking", "claude-opus-4-8", true},
		{"claude-sonnet-4-5-20250929", "", false},
		{"kimi-k2.5", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			base, ok := stripThinkingSuffix(tc.in)
			if ok != tc.wantOK || base != tc.wantBase {
				t.Fatalf("stripThinkingSuffix(%q) = (%q,%v), want (%q,%v)", tc.in, base, ok, tc.wantBase, tc.wantOK)
			}
		})
	}
}

// --- fix #3: client-cancel classification & health ---

func TestClassifyError_ClientCanceled(t *testing.T) {
	if got := ClassifyError(0, context.Canceled); got != ErrCanceled {
		t.Fatalf("context.Canceled = %v, want ErrCanceled", got)
	}
	if got := ClassifyError(0, context.DeadlineExceeded); got != ErrCanceled {
		t.Fatalf("context.DeadlineExceeded = %v, want ErrCanceled", got)
	}
	// A wrapped url.Error carrying the canceled sentinel is still a cancel.
	wrapped := fmt.Errorf(`Post "https://x/v1/messages": %w`, context.Canceled)
	if got := ClassifyError(0, wrapped); got != ErrCanceled {
		t.Fatalf("wrapped cancel = %v, want ErrCanceled", got)
	}
	// A genuine transport error is NOT a cancel.
	if got := ClassifyError(0, errors.New("connection refused")); got != ErrTransport {
		t.Fatalf("connection refused = %v, want ErrTransport", got)
	}
}

func TestRecordResult_CanceledDoesNotAffectHealth(t *testing.T) {
	lb := NewLoadBalancer(singleBackend(10))
	b := lb.Pick()
	// Far more cancellations than the disable threshold — health must be untouched.
	for i := 0; i < 20; i++ {
		b.RecordResult(ErrCanceled)
	}
	if b.State() != stateHealthy {
		t.Fatalf("client cancellations must not change state, got %d", b.State())
	}
	if b.consecErr.Load() != 0 {
		t.Fatalf("client cancellations must not accrue consecErr, got %d", b.consecErr.Load())
	}
}

// --- fix #4: quota failover primitives ---

func TestMarkQuotaExhausted_MakesUnselectable(t *testing.T) {
	lb := NewLoadBalancer(singleBackend(10))
	b := lb.Pick()
	if b == nil {
		t.Fatal("expected a backend")
	}
	if !lb.MarkQuotaExhausted(b.Name) {
		t.Fatal("MarkQuotaExhausted should find the backend")
	}
	if lb.Pick() != nil {
		t.Fatal("quota-exhausted backend must not be selectable")
	}
}

func TestPickExcluding_SkipsExcludedBackend(t *testing.T) {
	// Two distinct backends so failover has somewhere to go.
	cfgs := []config.BackendAPI{
		{Name: "a", URL: "http://a", APIKey: "k", Weight: 10, Enabled: true},
		{Name: "b", URL: "http://b", APIKey: "k", Weight: 10, Enabled: true},
	}
	lb := NewLoadBalancer(cfgs)

	got := lb.PickExcluding(map[string]bool{"a": true})
	if got == nil || got.Name != "b" {
		t.Fatalf("PickExcluding should return b, got %v", got)
	}
	// Excluding every backend yields nil.
	if lb.PickExcluding(map[string]bool{"a": true, "b": true}) != nil {
		t.Fatal("excluding all backends must return nil")
	}
}

func TestPeekQuotaExhausted(t *testing.T) {
	quota := `{"error":{"message":"Your account org-x is suspended due to insufficient balance","type":"exceeded_current_quota_error"}}`
	other := `{"error":{"message":"model not found","type":"invalid_request_error"}}`

	mk := func(s string) *http.Response {
		return &http.Response{Body: io.NopCloser(strings.NewReader(s))}
	}

	body, isQuota := peekQuotaExhausted(mk(quota))
	if !isQuota {
		t.Fatal("expected quota-exhaustion detection")
	}
	if string(body) != quota {
		t.Fatal("peek must return the consumed bytes so the body can be restored")
	}

	if _, isQuota := peekQuotaExhausted(mk(other)); isQuota {
		t.Fatal("non-quota error must not be detected as quota exhaustion")
	}
}
