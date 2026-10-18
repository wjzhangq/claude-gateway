package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/internal/logger"
)

// maxFailovers caps how many alternate backends a single request will try after
// the first one fails, bounding worst-case latency together with
// failoverBudgetSecs.
const maxFailovers = 2

// failoverKind classifies why a backend attempt should be abandoned in favour of
// another backend.
type failoverKind int

const (
	failoverNone       failoverKind = iota
	failoverTransport               // connection refused/reset, TLS, timeout...
	failoverQuota                   // 429 insufficient balance / account suspended
	failoverLoginQuota              // 403 pre-charge failure ("预扣费" / "Please run /login")
	failoverStatus                  // any other upstream 401/403/429/5xx
)

func (k failoverKind) String() string {
	switch k {
	case failoverTransport:
		return "transport error"
	case failoverQuota:
		return "quota exhausted (429)"
	case failoverLoginQuota:
		return "pre-charge quota failure (403)"
	case failoverStatus:
		return "upstream error status"
	}
	return "none"
}

// isFailoverStatus reports whether an upstream status is a per-backend failure
// that a different backend (a different upstream account/key) may not share.
// Request-shaped errors (400/404/413/422...) are excluded: every backend would
// reject them the same way. 401/403 are always about the gateway's own backend
// credential — the end user was already authenticated by the gateway — and
// Claude Code renders both as an unfixable "Please run /login" prompt.
func isFailoverStatus(code int) bool {
	switch {
	case code == http.StatusUnauthorized, code == http.StatusForbidden, code == http.StatusTooManyRequests:
		return true
	case code == http.StatusInternalServerError, code == http.StatusBadGateway,
		code == http.StatusServiceUnavailable, code == http.StatusGatewayTimeout:
		return true
	case code >= 520 && code <= 529: // Cloudflare origin errors + Anthropic 529 overloaded
		return true
	}
	return false
}

// peekedBody replays already-read bytes before the rest of the original body
// while still closing the original, so the connection is released.
type peekedBody struct {
	io.Reader
	io.Closer
}

// peekErrorBody reads up to maxPeek bytes of an error response, restores
// resp.Body so it can still be forwarded unchanged, and returns the bytes in a
// matchable form (gunzipped when the upstream compressed them — the client's
// Accept-Encoding is forwarded, so error bodies may arrive gzip-encoded).
func peekErrorBody(resp *http.Response) []byte {
	const maxPeek = 8192
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxPeek))
	resp.Body = peekedBody{Reader: io.MultiReader(bytes.NewReader(raw), resp.Body), Closer: resp.Body}
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		if r, err := gzip.NewReader(bytes.NewReader(raw)); err == nil {
			if dec, err := io.ReadAll(r); err == nil || len(dec) > 0 {
				return dec
			}
		}
	}
	return raw
}

// classifyFailover decides whether an attempt should be abandoned. err must not
// be a client cancellation (the caller handles that first).
func classifyFailover(resp *http.Response, err error) failoverKind {
	if err != nil {
		return failoverTransport
	}
	if resp == nil || !isFailoverStatus(resp.StatusCode) {
		return failoverNone
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		if isQuotaExhaustedBody(peekErrorBody(resp)) {
			return failoverQuota
		}
	case http.StatusForbidden:
		if isLoginQuotaBody(peekErrorBody(resp)) {
			return failoverLoginQuota
		}
	}
	return failoverStatus
}

// doRequestWithFailover sends body to backend and, when the attempt fails in a
// way another backend may not (see classifyFailover), retries the same request
// on the next routable backend. The chain is bounded by maxFailovers, by the
// failoverBudgetSecs wall-clock budget measured from start, and — for quota
// failures only — fast-fails while a quota cascade is active (retrying then just
// adds load at the worst moment).
//
// Abandoned backends have their failure recorded here. The returned response
// (with its body intact) and backend are those of the last attempt; recording
// that attempt's outcome is the caller's job, exactly as for a single request.
// A client cancellation is recorded as ErrCanceled and returned as err.
func (h *Handler) doRequestWithFailover(c *gin.Context, backend *Backend, upstreamPath string, body []byte, reqModel string, start time.Time) (*http.Response, *Backend, error) {
	tried := map[string]bool{backend.Name: true}
	failovers := 0
	chainDeadline := start.Add(failoverBudgetSecs * time.Second)
	for {
		targetURL := strings.TrimRight(backend.URL, "/") + upstreamPath
		resp, err := h.doRequest(c, backend, upstreamPath, targetURL, body)
		if err != nil && IsClientCanceled(err) {
			backend.RecordResult(ErrCanceled)
			return nil, backend, err
		}

		kind := classifyFailover(resp, err)
		if kind == failoverNone {
			return resp, backend, nil
		}

		quota := kind == failoverQuota || kind == failoverLoginQuota
		if quota {
			h.lb.MarkQuotaExhausted(backend.Name)
		}
		next := h.lb.PickExcluding(tried)
		cascade := quota && h.lb.QuotaCascadeActive()
		if next == nil || failovers >= maxFailovers || time.Now().After(chainDeadline) || cascade {
			logger.Warnf("failover stopped (backend=%s reason=%s failovers=%d alternate=%v cascade=%v)",
				backend.Name, kind, failovers, next != nil, cascade)
			return resp, backend, err
		}

		h.recordAbandoned(backend, kind, resp, err, reqModel, start)
		logger.Warnf("backend %s failed (%s): failing over to %s", backend.Name, kind, next.Name)
		failovers++
		backend = next
		tried[backend.Name] = true
	}
}

// recordAbandoned records the health outcome of an attempt that is being
// abandoned for failover and releases its connection.
func (h *Handler) recordAbandoned(backend *Backend, kind failoverKind, resp *http.Response, err error, reqModel string, start time.Time) {
	if resp != nil {
		resp.Body.Close()
		backend.RecordRequest(resp.StatusCode, time.Since(start).Milliseconds())
	}
	switch kind {
	case failoverTransport:
		if isValidHealthModel(reqModel) {
			backend.RecordResult(ClassifyError(0, err))
		}
	case failoverQuota:
		backend.RecordResult(ClassifyError(resp.StatusCode, nil))
	case failoverLoginQuota:
		// Counts against health without the 403 auth/forbidden path.
		backend.RecordResult(ErrRateLimit)
	default:
		if isValidHealthModel(reqModel) {
			var retryAfterUnix int64
			if resp.StatusCode == http.StatusTooManyRequests {
				retryAfterUnix = ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
			}
			backend.RecordResultDetailed(ClassifyError(resp.StatusCode, nil), retryAfterUnix, false)
		}
	}
}
