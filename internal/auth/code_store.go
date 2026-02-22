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

// Set stores a code for the given phone number.
func (cs *CodeStore) Set(phone, code string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.codes[phone] = codeEntry{
		code:      code,
		expiresAt: time.Now().Add(cs.expiry),
	}
}

// Verify checks the code and removes it on success.
func (cs *CodeStore) Verify(phone, code string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	entry, ok := cs.codes[phone]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(cs.codes, phone)
		return false
	}
	if entry.code != code {
		return false
	}
	delete(cs.codes, phone)
	return true
}

func (cs *CodeStore) cleanupLoop() {
	ticker := time.NewTicker(cs.expiry)
	defer ticker.Stop()
	for range ticker.C {
		cs.mu.Lock()
		now := time.Now()
		for phone, entry := range cs.codes {
			if now.After(entry.expiresAt) {
				delete(cs.codes, phone)
			}
		}
		cs.mu.Unlock()
	}
}
