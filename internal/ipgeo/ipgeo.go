// Package ipgeo provides an in-process IP → city cache with headquarters (HQ)
// classification by CIDR. It is owned exclusively by the server process, which
// records one entry per observed client IP (city, request count, HQ flag) and
// periodically persists the map to a single JSON cache file.
//
// Cities are resolved out-of-band by `check --ip2region`: the server exposes
// unresolved (public, city-less) IPs and accepts city updates, so unknown IPs
// start with an empty city and get filled in on the next resolve pass.
package ipgeo

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry is the per-IP record held in memory and persisted to the cache file.
type Entry struct {
	City      string    `json:"city"`       // resolved city; empty until check --ip2region fills it
	Count     int64     `json:"count"`      // number of requests observed from this IP
	HQ        bool      `json:"hq"`         // whether the IP falls in a configured HQ/VPN CIDR
	UpdatedAt time.Time `json:"updated_at"` // last time this entry was observed or resolved
}

// fileFormat is the on-disk JSON layout of the cache file.
type fileFormat struct {
	UpdatedAt time.Time         `json:"updated_at"`
	IPs       map[string]*Entry `json:"ips"`
}

// Store is a thread-safe IP → city cache with HQ classification.
type Store struct {
	mu        sync.RWMutex
	entries   map[string]*Entry
	hqNets    []*net.IPNet
	cacheFile string
	dirty     bool
}

// New builds a Store from the given cache file path and HQ CIDR list, loading
// any existing cache from disk. Malformed CIDRs are skipped. A nil/empty
// cacheFile disables persistence but the Store still classifies HQ and counts.
func New(cacheFile string, hqCIDRs []string) *Store {
	s := &Store{
		entries:   make(map[string]*Entry),
		cacheFile: cacheFile,
	}
	for _, c := range hqCIDRs {
		_, n, err := net.ParseCIDR(c)
		if err != nil || n == nil {
			continue
		}
		s.hqNets = append(s.hqNets, n)
	}
	s.load()
	return s
}

// isHQ reports whether ip falls within any configured HQ/VPN CIDR.
func (s *Store) isHQ(ip net.IP) bool {
	for _, n := range s.hqNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// skip reports whether an IP should not be tracked (loopback, private,
// link-local, unspecified, or unparseable). Such IPs never enter the cache.
func skip(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

// Observe records a request from ipStr, incrementing its count and returning
// the currently known city (possibly empty) and HQ flag. Loopback/private/
// reserved addresses are ignored: they return ("", false) and are not cached.
func (s *Store) Observe(ipStr string) (city string, isHQ bool) {
	ip := net.ParseIP(ipStr)
	if skip(ip) {
		return "", false
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[ipStr]
	if !ok {
		e = &Entry{HQ: s.isHQ(ip)}
		s.entries[ipStr] = e
	}
	e.Count++
	e.UpdatedAt = now
	s.dirty = true
	return e.City, e.HQ
}

// Unresolved returns the sorted list of public IPs that have no city yet.
func (s *Store) Unresolved() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for ip, e := range s.entries {
		if e.City == "" {
			out = append(out, ip)
		}
	}
	sort.Strings(out)
	return out
}

// Resolve sets the city for ip. Unknown IPs are created so a city discovered
// out-of-band is retained even if the IP has since been evicted. Empty city is
// ignored so a failed lookup does not clobber an existing value.
func (s *Store) Resolve(ip, city string) {
	if city == "" {
		return
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[ip]
	if !ok {
		e = &Entry{HQ: s.isHQ(parsed)}
		s.entries[ip] = e
	}
	e.City = city
	e.UpdatedAt = now
	s.dirty = true
}

// load reads the cache file into memory. A missing or malformed file is treated
// as an empty cache (best effort — the cache is a rebuildable side store).
func (s *Store) load() {
	if s.cacheFile == "" {
		return
	}
	data, err := os.ReadFile(s.cacheFile)
	if err != nil {
		return
	}
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return
	}
	if f.IPs != nil {
		s.entries = f.IPs
	}
}

// Flush persists the current cache to disk if it has changed since the last
// flush. It writes atomically via a temp file + rename. Safe to call on nil.
func (s *Store) Flush() error {
	if s == nil || s.cacheFile == "" {
		return nil
	}
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	f := fileFormat{UpdatedAt: time.Now(), IPs: s.entries}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.dirty = false
	s.mu.Unlock()

	if dir := filepath.Dir(s.cacheFile); dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}
	tmp := s.cacheFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.cacheFile)
}

// StartFlusher runs a background ticker that periodically flushes the cache
// until stop is closed. Returns immediately; run as a goroutine's owner.
func (s *Store) StartFlusher(interval time.Duration, stop <-chan struct{}) {
	if s == nil || s.cacheFile == "" {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := s.Flush(); err != nil {
					// best effort; the next tick retries
					_ = err
				}
			case <-stop:
				_ = s.Flush()
				return
			}
		}
	}()
}
