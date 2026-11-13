package proxy

import (
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Connection-pool sizing for the shared transports. The gateway funnels many
// backends through very few upstream hosts (each backend is a distinct API key
// on the same host), and the key travels as a per-request header rather than
// as connection state, so every backend on a host can draw from one pool.
const (
	transportMaxIdleConns        = 200
	transportMaxIdleConnsPerHost = 50
	transportIdleConnTimeout     = 90 * time.Second
)

var (
	transportsMu sync.RWMutex
	transports   = make(map[string]*http.Transport)
)

// GetSharedTransport returns the transport for backendURL's host, creating it
// on first use. Backends that point at the same host get the same transport and
// therefore share its connection pool; a URL that fails to parse falls back to
// a private transport so a malformed entry cannot poison the shared map.
func GetSharedTransport(backendURL string) *http.Transport {
	host := transportKey(backendURL)
	if host == "" {
		return newTransport()
	}

	transportsMu.RLock()
	t := transports[host]
	transportsMu.RUnlock()
	if t != nil {
		return t
	}

	transportsMu.Lock()
	defer transportsMu.Unlock()
	// Re-check: another goroutine may have created it between the two locks.
	if t := transports[host]; t != nil {
		return t
	}
	t = newTransport()
	transports[host] = t
	return t
}

// GetDedicatedClient returns a client with its own timeout over the shared
// transport for backendURL's host. Each backend keeps a distinct client so
// per-backend timeout policy stays independent, while the underlying
// connections are pooled per host.
func GetDedicatedClient(backendURL string, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: GetSharedTransport(backendURL),
		Timeout:   timeout,
	}
}

// transportKey reduces a backend URL to the host that keys its transport.
// Scheme is included so an http and an https endpoint on the same host do not
// share a pool. Returns "" when the URL carries no usable host.
func transportKey(backendURL string) string {
	u, err := url.Parse(backendURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func newTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConns:        transportMaxIdleConns,
		MaxIdleConnsPerHost: transportMaxIdleConnsPerHost,
		MaxConnsPerHost:     0, // unlimited: never queue a request behind the pool
		IdleConnTimeout:     transportIdleConnTimeout,
		ForceAttemptHTTP2:   true,
		DisableCompression:  true, // prevent gzip on SSE streams (e.g. MiniMax returns compressed binary)
	}
}

// SharedTransportHosts reports the hosts that currently hold a shared
// transport, for diagnostics.
func SharedTransportHosts() []string {
	transportsMu.RLock()
	defer transportsMu.RUnlock()
	hosts := make([]string, 0, len(transports))
	for h := range transports {
		hosts = append(hosts, h)
	}
	return hosts
}

var (
	multiHostOnce      sync.Once
	multiHostTransport *http.Transport
)

// GetMultiHostTransport returns one transport for callers whose destination
// varies per request, such as the downgrade path to public providers. An
// http.Transport keys its idle pool by host internally, so a single instance
// still pools each provider's connections separately — unlike the per-request
// clients this replaces, which started from an empty pool every time.
func GetMultiHostTransport() *http.Transport {
	multiHostOnce.Do(func() {
		multiHostTransport = newTransport()
	})
	return multiHostTransport
}
