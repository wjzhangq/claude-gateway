package auth

import (
	"sync"
	"time"
)

// CodeStore holds in-memory verification codes with expiry.
type CodeStore struct {
	mu      sync.Mutex
	codes   map[string]codeEntry
	expiry  time.Duration
}

type codeEntry struct {
	code      string
	expiresAt time.Time
}

// NewCodeStore creates a CodeStore with the given TTL.
func NewCodeStore(expiry time.Duration) *CodeStore {
	cs := &CodeStore{
		codes:  make(map[string]codeEntry),
		expiry: expiry,
	}
	go cs.cleanupLoop()
	return cs
}

// Set stores a code for the given itcode.
func (cs *CodeStore) Set(itcode, code string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.codes[itcode] = codeEntry{
		code:      code,
		expiresAt: time.Now().Add(cs.expiry),
	}
}

// Verify checks the code and removes it on success.
func (cs *CodeStore) Verify(itcode, code string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	entry, ok := cs.codes[itcode]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(cs.codes, itcode)
		return false
	}
	if entry.code != code {
		return false
	}
	delete(cs.codes, itcode)
	return true
}

func (cs *CodeStore) cleanupLoop() {
	ticker := time.NewTicker(cs.expiry)
	defer ticker.Stop()
	for range ticker.C {
		cs.mu.Lock()
		now := time.Now()
		for itcode, entry := range cs.codes {
			if now.After(entry.expiresAt) {
				delete(cs.codes, itcode)
			}
		}
		cs.mu.Unlock()
	}
}
