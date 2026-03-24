package auth

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/model"
)

// KeyInfo is the in-memory representation of an active API key.
type KeyInfo struct {
	KeyID       int64
	UserID      int64
	Itcode      string
	DailyQuotaTokens int64  // 0 = unlimited
	UserStatus  string // active | disabled
	CreatedAt   time.Time // key creation time
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
		u, ok := users[k.UserID]
		if !ok || u.Status != "active" {
			continue
		}
		m[k.Key] = &KeyInfo{
			KeyID:       k.ID,
			UserID:      k.UserID,
			Itcode:      u.Itcode,
			DailyQuotaTokens: u.DailyQuotaTokens,
			UserStatus:  u.Status,
			CreatedAt:  k.CreatedAt,
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

// Count returns the number of keys in the store.
func (ks *KeyStore) Count() int {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return len(ks.keys)
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

// UpdateUserStatus updates the UserStatus for all keys belonging to a user.
// If the new status is not "active", all keys for this user are removed from the store.
func (ks *KeyStore) UpdateUserStatus(userID int64, status string) {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	if status != "active" {
		// Remove all keys for this user
		for key, info := range ks.keys {
			if info.UserID == userID {
				delete(ks.keys, key)
			}
		}
	} else {
		// Update status for all keys
		for _, info := range ks.keys {
			if info.UserID == userID {
				info.UserStatus = status
			}
		}
	}
}

// unambiguousChars excludes visually confusing characters: 0/O, 1/l/I.
const unambiguousChars = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"

// GenerateKey creates a new API key string with "sk-" prefix using only
// unambiguous alphanumeric characters (no 0, O, 1, l, I).
func GenerateKey() (string, error) {
	const keyLen = 32
	b := make([]byte, keyLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	n := byte(len(unambiguousChars))
	for i := range b {
		b[i] = unambiguousChars[b[i]%n]
	}
	return "sk-" + string(b), nil
}
