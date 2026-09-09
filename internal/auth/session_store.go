package auth

import (
	"sync"
	"time"
)

// SessionData holds user session information
type SessionData struct {
	UserID    int64
	UserRole  string
	ExpiresAt time.Time
}

// SessionStore manages session ID to user data mapping in memory
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*SessionData
}

// NewSessionStore creates a new session store
func NewSessionStore() *SessionStore {
	store := &SessionStore{
		sessions: make(map[string]*SessionData),
	}
	// Start background cleaner to remove expired sessions
	go store.cleanExpiredSessions()
	return store
}

// Set stores session data with expiration (minimum 1 day)
func (s *SessionStore) Set(sessionID string, userID int64, userRole string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Enforce minimum TTL of 1 day
	minTTL := 24 * time.Hour
	if ttl < minTTL {
		ttl = minTTL
	}

	s.sessions[sessionID] = &SessionData{
		UserID:    userID,
		UserRole:  userRole,
		ExpiresAt: time.Now().Add(ttl),
	}
}

// Get retrieves session data if it exists and is not expired
func (s *SessionStore) Get(sessionID string) (*SessionData, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, exists := s.sessions[sessionID]
	if !exists {
		return nil, false
	}
	if time.Now().After(data.ExpiresAt) {
		return nil, false
	}
	return data, true
}

// Delete removes a session
func (s *SessionStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

// cleanExpiredSessions periodically removes expired sessions
func (s *SessionStore) cleanExpiredSessions() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		s.mu.Lock()
		for id, data := range s.sessions {
			if now.After(data.ExpiresAt) {
				delete(s.sessions, id)
			}
		}
		s.mu.Unlock()
	}
}
