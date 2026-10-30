package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// probeServer counts hits per path and answers with the per-path codes given.
// A zero code for /v1/messages answers a well-formed message (probe success).
func probeServer(t *testing.T, modelsCode, messagesCode int, hits map[string]int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/v1/models":
			if modelsCode != http.StatusOK {
				w.WriteHeader(modelsCode)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[]}`)
		case "/v1/messages":
			if messagesCode != 0 && messagesCode != http.StatusOK {
				w.WriteHeader(messagesCode)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"type":"message","content":[{"type":"text","text":"p"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// An auth failure on the models probe is account-level: the key cannot serve
// inference either, so the billable messages fallback must not be sent.
func TestProbeReachable_AuthFailureSkipsBillableFallback(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		hits := map[string]int{}
		srv := probeServer(t, code, http.StatusOK, hits)
		client := &http.Client{Timeout: 5 * time.Second}

		observed, _, usedInference, err := ProbeReachableDetailed(client, srv.URL, "k")
		if err == nil {
			t.Fatalf("code=%d: expected the backend to be judged unreachable", code)
		}
		if observed != code {
			t.Fatalf("code=%d: observed = %d, want %d", code, observed, code)
		}
		if usedInference {
			t.Fatalf("code=%d: usedInference must stay false so no probe cost is attributed", code)
		}
		if hits["/v1/messages"] != 0 {
			t.Fatalf("code=%d: sent %d billable messages probe(s) after an auth failure",
				code, hits["/v1/messages"])
		}
	}
}

// A models endpoint that is merely absent or broken says nothing about the
// account, so the messages fallback still decides.
func TestProbeReachable_NonAuthFailureStillFallsBack(t *testing.T) {
	hits := map[string]int{}
	srv := probeServer(t, http.StatusNotFound, http.StatusOK, hits)
	client := &http.Client{Timeout: 5 * time.Second}

	observed, _, usedInference, err := ProbeReachableDetailed(client, srv.URL, "k")
	if err != nil {
		t.Fatalf("messages probe succeeded, so the backend is reachable: %v", err)
	}
	if !usedInference {
		t.Fatal("usedInference must be true so the probe cost is attributed")
	}
	if observed != http.StatusOK {
		t.Fatalf("observed = %d, want 200 from the messages probe", observed)
	}
	if hits["/v1/messages"] != 1 {
		t.Fatalf("messages probes = %d, want 1", hits["/v1/messages"])
	}
}
