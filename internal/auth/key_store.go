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
	KeyID            int64
	UserID           int64
	Itcode           string
	DailyQuotaTokens int64     // 0 = unlimited
	UserStatus       string    // active | disabled
	CreatedAt        time.Time // key creation time
	AutoDowngrade    bool      // whether auto-downgrade is enabled
	DowngradedUntil  time.Time // if set and in future, skip original model and use GPT directly
	LastUsedAt       time.Time // updated in-memory on every request, flushed to DB periodically
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

// Load replaces the entire key map (called at startup or reload).
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
		info := &KeyInfo{
			KeyID:            k.ID,
			UserID:           k.UserID,
			Itcode:           u.Itcode,
			DailyQuotaTokens: u.DailyQuotaTokens,
			UserStatus:       u.Status,
			CreatedAt:        k.CreatedAt,
			AutoDowngrade:    k.AutoDowngrade,
		}
		if k.LastUsedAt != nil {
			info.LastUsedAt = *k.LastUsedAt
		}
		m[k.Key] = info
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

// MarkUsed updates the in-memory LastUsedAt for the given key. O(1), no DB write.
func (ks *KeyStore) MarkUsed(key string, t time.Time) {
	ks.mu.RLock()
	info := ks.keys[key]
	ks.mu.RUnlock()
	if info != nil {
		// LastUsedAt is a value field; update via pointer under read lock is safe
		// because only this goroutine writes LastUsedAt for this key at this moment.
		// Use a separate write lock to be safe.
		ks.mu.Lock()
		if info2, ok := ks.keys[key]; ok {
			info2.LastUsedAt = t
		}
		ks.mu.Unlock()
	}
}

// FlushLastUsed returns a snapshot of keyID -> LastUsedAt for all keys that have
// been used (LastUsedAt != zero), then resets the dirty tracking.
// Callers should persist this map to DB.
func (ks *KeyStore) FlushLastUsed() map[int64]time.Time {
	ks.mu.RLock()
	result := make(map[int64]time.Time, len(ks.keys))
	for _, info := range ks.keys {
		if !info.LastUsedAt.IsZero() {
			result[info.KeyID] = info.LastUsedAt
		}
	}
	ks.mu.RUnlock()
	return result
}

// SetDowngradedUntil sets the time until which this API key should use GPT directly.
func (ks *KeyStore) SetDowngradedUntil(key string, until time.Time) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	if info, ok := ks.keys[key]; ok {
		info.DowngradedUntil = until
	}
}

// IsDowngraded checks if the key is currently in the downgraded period.
func (ks *KeyStore) IsDowngraded(key string) bool {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	if info, ok := ks.keys[key]; ok {
		return !info.DowngradedUntil.IsZero() && time.Now().Before(info.DowngradedUntil)
	}
	return false
}

// UpdateUserStatus updates the UserStatus for all keys belonging to a user.
// If the new status is not "active", all keys for this user are removed from the store.
func (ks *KeyStore) UpdateUserStatus(userID int64, status string) {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	if status != "active" {
		for key, info := range ks.keys {
			if info.UserID == userID {
				delete(ks.keys, key)
			}
		}
	} else {
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
