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
	GroupID          int
	Itcode           string
	DailyQuotaUSD    float64   // 0 = unlimited (backend channel daily quota in USD)
	AWSDailyQuotaUSD float64   // 0 = unlimited (aws channel daily quota in USD)
	UserStatus       string    // active | disabled
	CreatedAt        time.Time // key creation time
	AutoDowngrade    bool      // whether auto-downgrade is enabled
	DowngradedUntil  time.Time // if set and in future, skip original model and use GPT directly
	LastUsedAt       time.Time // updated in-memory on every request, flushed to DB periodically
	Channel          string    // "backend" | "aws"
	// Cost accumulators (write-back pattern, flushed to DB periodically)
	backendCostDelta float64 // pending backend cost not yet written to DB
	awsCostDelta     float64 // pending aws cost not yet written to DB
	costDirty        bool    // true if cost needs flush
}

// KeyStore holds all active API keys in memory for O(1) lookup.
type KeyStore struct {
	mu              sync.RWMutex
	keys            map[string]*KeyInfo // key string -> KeyInfo
	dailyCosts      map[int64]float64   // userID -> today's accumulated backend cost (USD)
	dailyCostDate   string              // "YYYY-MM-DD" of the current daily cost window
	awsDailyCosts   map[int64]float64   // userID -> today's accumulated AWS cost (USD)
	awsDailyCostDate string             // "YYYY-MM-DD" of the current AWS daily cost window
}

// NewKeyStore creates an empty KeyStore.
func NewKeyStore() *KeyStore {
	return &KeyStore{
		keys:          make(map[string]*KeyInfo),
		dailyCosts:    make(map[int64]float64),
		awsDailyCosts: make(map[int64]float64),
	}
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
			GroupID:          u.GroupID,
			Itcode:           u.Itcode,
			DailyQuotaUSD:    u.DailyQuotaUSD,
			AWSDailyQuotaUSD: u.AWSDailyQuotaUSD,
			UserStatus:       u.Status,
			CreatedAt:        k.CreatedAt,
			AutoDowngrade:    k.AutoDowngrade,
			Channel:          k.Channel,
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
		ks.mu.Lock()
		if info2, ok := ks.keys[key]; ok {
			info2.LastUsedAt = t
		}
		ks.mu.Unlock()
	}
}

// AddCost accumulates cost for a key in memory (channel-aware). No DB write.
func (ks *KeyStore) AddCost(key string, channel string, costUSD float64) {
	if costUSD <= 0 {
		return
	}
	ks.mu.Lock()
	if info, ok := ks.keys[key]; ok {
		if channel == "aws" {
			info.awsCostDelta += costUSD
		} else {
			info.backendCostDelta += costUSD
		}
		info.costDirty = true
	}
	ks.mu.Unlock()
}

// KeyCostUpdate holds pending cost delta for one key.
type KeyCostUpdate struct {
	KeyID          int64
	BackendCostAdd float64
	AWSCostAdd     float64
}

// FlushCosts returns and resets the pending cost deltas for all dirty keys.
// Callers should persist the returned updates to DB.
func (ks *KeyStore) FlushCosts() []KeyCostUpdate {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	var updates []KeyCostUpdate
	for _, info := range ks.keys {
		if !info.costDirty {
			continue
		}
		updates = append(updates, KeyCostUpdate{
			KeyID:          info.KeyID,
			BackendCostAdd: info.backendCostDelta,
			AWSCostAdd:     info.awsCostDelta,
		})
		info.backendCostDelta = 0
		info.awsCostDelta = 0
		info.costDirty = false
	}
	return updates
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

// ── Daily cost tracking ──────────────────────────────────────────────────────

// todayStr returns the current date as "YYYY-MM-DD" in local time.
func todayStr() string {
	return time.Now().Format("2006-01-02")
}

// checkAndResetDaily resets dailyCosts if the date has rolled over.
// Must be called with ks.mu held (write lock).
func (ks *KeyStore) checkAndResetDaily() {
	today := todayStr()
	if ks.dailyCostDate != today {
		ks.dailyCosts = make(map[int64]float64)
		ks.dailyCostDate = today
	}
}

// AddDailyCost accumulates backend spend for a user today. Thread-safe.
func (ks *KeyStore) AddDailyCost(userID int64, costUSD float64) {
	if costUSD <= 0 {
		return
	}
	ks.mu.Lock()
	ks.checkAndResetDaily()
	ks.dailyCosts[userID] += costUSD
	ks.mu.Unlock()
}

// GetDailyCost returns today's accumulated backend spend for a user. Thread-safe.
func (ks *KeyStore) GetDailyCost(userID int64) float64 {
	ks.mu.Lock()
	ks.checkAndResetDaily()
	cost := ks.dailyCosts[userID]
	ks.mu.Unlock()
	return cost
}

// InitDailyCosts seeds the in-memory daily cost map from a pre-fetched snapshot.
// date must be "YYYY-MM-DD". Costs map is userID -> cost_usd for that date.
// Call this once at startup (after loading daily_stats from DB).
func (ks *KeyStore) InitDailyCosts(date string, costs map[int64]float64) {
	ks.mu.Lock()
	ks.dailyCostDate = date
	ks.dailyCosts = make(map[int64]float64, len(costs))
	for uid, c := range costs {
		ks.dailyCosts[uid] = c
	}
	ks.mu.Unlock()
}

// ResetDailyCosts clears all accumulated daily costs (e.g. called at midnight).
func (ks *KeyStore) ResetDailyCosts() {
	ks.mu.Lock()
	ks.dailyCosts = make(map[int64]float64)
	ks.dailyCostDate = todayStr()
	ks.awsDailyCosts = make(map[int64]float64)
	ks.awsDailyCostDate = todayStr()
	ks.mu.Unlock()
}

// ── AWS Daily cost tracking ───────────────────────────────────────────────────

// checkAndResetAWSDaily resets awsDailyCosts if the date has rolled over.
// Must be called with ks.mu held (write lock).
func (ks *KeyStore) checkAndResetAWSDaily() {
	today := todayStr()
	if ks.awsDailyCostDate != today {
		ks.awsDailyCosts = make(map[int64]float64)
		ks.awsDailyCostDate = today
	}
}

// AddAWSDailyCost accumulates AWS spend for a user today. Thread-safe.
func (ks *KeyStore) AddAWSDailyCost(userID int64, costUSD float64) {
	if costUSD <= 0 {
		return
	}
	ks.mu.Lock()
	ks.checkAndResetAWSDaily()
	ks.awsDailyCosts[userID] += costUSD
	ks.mu.Unlock()
}

// GetAWSDailyCost returns today's accumulated AWS spend for a user. Thread-safe.
func (ks *KeyStore) GetAWSDailyCost(userID int64) float64 {
	ks.mu.Lock()
	ks.checkAndResetAWSDaily()
	cost := ks.awsDailyCosts[userID]
	ks.mu.Unlock()
	return cost
}

// InitAWSDailyCosts seeds the in-memory AWS daily cost map from a pre-fetched snapshot.
func (ks *KeyStore) InitAWSDailyCosts(date string, costs map[int64]float64) {
	ks.mu.Lock()
	ks.awsDailyCostDate = date
	ks.awsDailyCosts = make(map[int64]float64, len(costs))
	for uid, c := range costs {
		ks.awsDailyCosts[uid] = c
	}
	ks.mu.Unlock()
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
