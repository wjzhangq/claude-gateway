package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/claude-gateway/claude-gateway/internal/model"
)

// KeyInfo is the in-memory representation of an active API key.
type KeyInfo struct {
	KeyID       int64
	UserID      int64
	QuotaTokens int64  // 0 = unlimited
	UserStatus  string // active | disabled
}

// KeyStore holds all active API keys in memory for O(1) lookup.
type KeyStore struct {
	mu   sync.RWMutex
	keys map[string]*KeyInfo // key string -> KeyInfo
}

// NewKeyStore creates an empty KeyStore.
func NewKeyStore() *KeyStore {
	return &KeyStore{keys: make(map[string]*KeyInfo)}
}

// Load replaces the entire key map (called at startup).
func (ks *KeyStore) Load(keys []model.APIKey, users map[int64]*model.User) {
	m := make(map[string]*KeyInfo, len(keys))
	for _, k := range keys {
		if k.Status != "active" {
			continue
		}
		if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
			continue
		}
		u, ok := users[k.UserID]
		if !ok || u.Status != "active" {
			continue
		}
		m[k.Key] = &KeyInfo{
			KeyID:       k.ID,
			UserID:      k.UserID,
			QuotaTokens: u.QuotaTokens,
			UserStatus:  u.Status,
		}
	}
	ks.mu.Lock()
	ks.keys = m
	ks.mu.Unlock()
}

// Get looks up a key; returns nil if not found or inactive.
func (ks *KeyStore) Get(key string) *KeyInfo {
	ks.mu.RLock()
	info := ks.keys[key]
	ks.mu.RUnlock()
	return info
}

// Add inserts or updates a key in memory.
func (ks *KeyStore) Add(key string, info *KeyInfo) {
	ks.mu.Lock()
	ks.keys[key] = info
	ks.mu.Unlock()
}

// Remove deletes a key from memory.
func (ks *KeyStore) Remove(key string) {
	ks.mu.Lock()
	delete(ks.keys, key)
	ks.mu.Unlock()
}

// GenerateKey creates a new API key string with "sk-" prefix.
func GenerateKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return "sk-" + hex.EncodeToString(b), nil
}
