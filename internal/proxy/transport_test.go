package proxy

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/wjzhangq/claude-gateway/config"
)

func TestSharedTransportSameHost(t *testing.T) {
	// Two backends differing only by API key and path must land on one pool.
	a := GetSharedTransport("https://dasheng.aibeke.com")
	b := GetSharedTransport("https://dasheng.aibeke.com/v1/messages")
	if a != b {
		t.Fatal("same host returned different transports; connections would not be shared")
	}
}

func TestSharedTransportDifferentHosts(t *testing.T) {
	a := GetSharedTransport("https://dasheng.aibeke.com")
	b := GetSharedTransport("https://zjz-ai.webtrn.cn")
	if a == b {
		t.Fatal("different hosts share a transport; per-host idle limits would collide")
	}
}

func TestSharedTransportSchemeSeparates(t *testing.T) {
	a := GetSharedTransport("https://example.invalid")
	b := GetSharedTransport("http://example.invalid")
	if a == b {
		t.Fatal("http and https on one host share a transport")
	}
}

func TestSharedTransportSettings(t *testing.T) {
	tr := GetSharedTransport("https://settings.example.invalid")
	if got := tr.MaxIdleConnsPerHost; got != transportMaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want %d", got, transportMaxIdleConnsPerHost)
	}
	if got := tr.MaxIdleConns; got != transportMaxIdleConns {
		t.Errorf("MaxIdleConns = %d, want %d", got, transportMaxIdleConns)
	}
	if tr.MaxConnsPerHost != 0 {
		t.Errorf("MaxConnsPerHost = %d, want 0 (unlimited)", tr.MaxConnsPerHost)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 = false, want true")
	}
	// SSE streams must not be gzipped: the stream parser reads raw bytes.
	if !tr.DisableCompression {
		t.Error("DisableCompression = false, want true (SSE streams must stay uncompressed)")
	}
}

// A malformed URL must not be cached under a shared key, or one bad config
// entry would hand its transport to every other unparseable backend.
func TestSharedTransportUnparseableURLIsolated(t *testing.T) {
	a := GetSharedTransport("://nonsense")
	b := GetSharedTransport("://nonsense")
	if a == b {
		t.Fatal("unparseable URLs share a cached transport; expected a private one each")
	}
	if a == nil || b == nil {
		t.Fatal("unparseable URL produced a nil transport")
	}
}

func TestSharedTransportEmptyURL(t *testing.T) {
	// A backend with no URL still must not panic or poison the cache.
	if got := GetSharedTransport(""); got == nil {
		t.Fatal("empty URL produced a nil transport")
	}
}

func TestDedicatedClientsShareTransport(t *testing.T) {
	const url = "https://shared.example.invalid"
	c1 := GetDedicatedClient(url, 10*time.Second)
	c2 := GetDedicatedClient(url, 300*time.Second)

	if c1 == c2 {
		t.Fatal("GetDedicatedClient returned the same client; per-backend timeouts would collide")
	}
	if c1.Transport != c2.Transport {
		t.Fatal("clients on one host do not share a transport")
	}
	if c1.Timeout == c2.Timeout {
		t.Fatalf("both clients carry timeout %v; each backend must keep its own", c1.Timeout)
	}
}

func TestMultiHostTransportSingleton(t *testing.T) {
	if GetMultiHostTransport() != GetMultiHostTransport() {
		t.Fatal("GetMultiHostTransport is not a singleton")
	}
}

// The transport cache is read on every backend construction and written on
// config reload, so concurrent access must be safe.
func TestSharedTransportConcurrent(t *testing.T) {
	hosts := []string{
		"https://a.example.invalid",
		"https://b.example.invalid",
		"https://c.example.invalid",
	}

	var wg sync.WaitGroup
	got := make([]*http.Transport, 60)
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = GetSharedTransport(hosts[i%len(hosts)])
		}(i)
	}
	wg.Wait()

	// Every request for the same host must have returned the identical pointer.
	for i := range got {
		if got[i] == nil {
			t.Fatalf("index %d got a nil transport", i)
		}
		if got[i] != got[i%len(hosts)] {
			t.Fatalf("index %d (host %s) differs from the first transport for that host",
				i, hosts[i%len(hosts)])
		}
	}
}

// The whole point of the change: many backends, few hosts, one pool each.
func TestManyBackendsFewTransports(t *testing.T) {
	lb := &LoadBalancer{}
	before := len(SharedTransportHosts())

	// Mirror the real config shape: 40 keys on one host, 6 on another.
	for i := 0; i < 40; i++ {
		cfg := config.BackendAPI{
			Name:    "dasheng-" + string(rune('a'+i)),
			URL:     "https://pool-a.example.invalid",
			APIKey:  "sk-test",
			Weight:  10,
			Enabled: true,
		}
		lb.newBackend(cfg)
	}
	for i := 0; i < 6; i++ {
		cfg := config.BackendAPI{
			Name:    "lianxiang-" + string(rune('a'+i)),
			URL:     "https://pool-b.example.invalid",
			APIKey:  "sk-test",
			Weight:  10,
			Enabled: true,
		}
		lb.newBackend(cfg)
	}

	if added := len(SharedTransportHosts()) - before; added != 2 {
		t.Errorf("46 backends created %d transports, want 2", added)
	}
}
