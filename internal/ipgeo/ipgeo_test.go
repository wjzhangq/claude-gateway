package ipgeo

import (
	"path/filepath"
	"testing"
)

var testCIDRs = []string{
	"111.205.43.224/27",
	"106.38.1.112/28",
	"111.198.161.0/24", // VPN — also HQ
}

func TestObserveHQClassification(t *testing.T) {
	s := New("", testCIDRs)

	cases := []struct {
		ip     string
		wantHQ bool
	}{
		{"111.205.43.230", true},  // inside /27
		{"111.205.43.255", false}, // outside /27 (only .224-.255? .224/27 = .224-.255) -> actually in range
		{"106.38.1.120", true},    // inside /28
		{"111.198.161.5", true},   // VPN
		{"8.8.8.8", false},        // public, not HQ
	}
	// Correct expectation: 111.205.43.224/27 covers .224–.255, so .255 IS in range.
	cases[1].wantHQ = true

	for _, c := range cases {
		_, hq := s.Observe(c.ip)
		if hq != c.wantHQ {
			t.Errorf("Observe(%s) HQ = %v, want %v", c.ip, hq, c.wantHQ)
		}
	}
}

func TestObserveSkipsPrivateAndCounts(t *testing.T) {
	s := New("", testCIDRs)

	// Private/loopback are skipped: not counted, not cached.
	for _, ip := range []string{"127.0.0.1", "192.168.1.1", "10.0.0.1", "not-an-ip"} {
		city, hq := s.Observe(ip)
		if city != "" || hq {
			t.Errorf("Observe(%s) = (%q,%v), want empty/false", ip, city, hq)
		}
	}
	if got := len(s.Unresolved()); got != 0 {
		t.Errorf("private IPs should not be cached, got %d unresolved", got)
	}

	// A public IP is counted and shows up as unresolved (no city yet).
	s.Observe("8.8.8.8")
	s.Observe("8.8.8.8")
	s.mu.RLock()
	e := s.entries["8.8.8.8"]
	s.mu.RUnlock()
	if e == nil || e.Count != 2 {
		t.Fatalf("expected count 2 for 8.8.8.8, got %+v", e)
	}
	un := s.Unresolved()
	if len(un) != 1 || un[0] != "8.8.8.8" {
		t.Errorf("Unresolved() = %v, want [8.8.8.8]", un)
	}
}

func TestResolveFillsCity(t *testing.T) {
	s := New("", testCIDRs)
	s.Observe("1.2.3.4")

	s.Resolve("1.2.3.4", "北京")
	city, _ := s.Observe("1.2.3.4")
	if city != "北京" {
		t.Errorf("after Resolve, city = %q, want 北京", city)
	}
	if len(s.Unresolved()) != 0 {
		t.Errorf("resolved IP should not be unresolved")
	}

	// Empty city must not clobber an existing value.
	s.Resolve("1.2.3.4", "")
	city, _ = s.Observe("1.2.3.4")
	if city != "北京" {
		t.Errorf("empty Resolve clobbered city: %q", city)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	s := New(path, testCIDRs)
	s.Observe("111.205.43.230") // HQ
	s.Observe("8.8.8.8")
	s.Resolve("8.8.8.8", "山景城")
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Reload from disk into a fresh store.
	s2 := New(path, testCIDRs)
	city, hq := s2.Observe("8.8.8.8")
	if city != "山景城" {
		t.Errorf("reloaded city = %q, want 山景城", city)
	}
	_, hq = s2.Observe("111.205.43.230")
	if !hq {
		t.Errorf("reloaded HQ flag lost")
	}
}
