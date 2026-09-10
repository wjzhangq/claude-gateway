package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/logger"
)

// SessionData holds user session information
type SessionData struct {
	UserID    int64     `json:"user_id"`
	UserRole  string    `json:"user_role"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SessionStore manages session ID to user data mapping with file persistence
type SessionStore struct {
	mu         sync.RWMutex
	sessions   map[string]*SessionData
	storePath  string
	stopCh     chan struct{}
	autoSaveCh chan struct{}
}

// NewSessionStore creates a new session store with file persistence
func NewSessionStore(storePath string) *SessionStore {
	if storePath == "" {
		storePath = filepath.Join(os.TempDir(), "claude-gateway-sessions.json")
	}

	store := &SessionStore{
		sessions:   make(map[string]*SessionData),
		storePath:  storePath,
		stopCh:     make(chan struct{}),
		autoSaveCh: make(chan struct{}, 1),
	}

	// Load sessions from file if exists
	if err := store.load(); err != nil {
		logger.Warnf("failed to load sessions from %s: %v", storePath, err)
	} else {
		logger.Infof("loaded %d sessions from %s", len(store.sessions), storePath)
	}

	// Start background worker for cleanup and auto-save
	go store.backgroundWorker()

	return store
}

// Set stores session data with expiration (minimum 1 day) and triggers auto-save
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

	// Trigger auto-save (non-blocking)
	select {
	case s.autoSaveCh <- struct{}{}:
	default:
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

// Delete removes a session and triggers auto-save
func (s *SessionStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)

	// Trigger auto-save
	select {
	case s.autoSaveCh <- struct{}{}:
	default:
	}
}

// Close stops background worker and saves current state
func (s *SessionStore) Close() error {
	close(s.stopCh)
	return s.save()
}

// backgroundWorker handles periodic cleanup and auto-save
func (s *SessionStore) backgroundWorker() {
	cleanupTicker := time.NewTicker(5 * time.Minute)
	saveTicker := time.NewTicker(30 * time.Second)
	defer cleanupTicker.Stop()
	defer saveTicker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-cleanupTicker.C:
			s.cleanExpired()
		case <-saveTicker.C:
			if err := s.save(); err != nil {
				logger.Errorf("auto-save sessions: %v", err)
			}
		case <-s.autoSaveCh:
			// Debounced save triggered by Set/Delete
			time.Sleep(100 * time.Millisecond)
			if err := s.save(); err != nil {
				logger.Errorf("triggered save sessions: %v", err)
			}
		}
	}
}

// cleanExpired removes expired sessions and saves
func (s *SessionStore) cleanExpired() {
	now := time.Now()
	s.mu.Lock()
	cleaned := 0
	for id, data := range s.sessions {
		if now.After(data.ExpiresAt) {
			delete(s.sessions, id)
			cleaned++
		}
	}
	s.mu.Unlock()

	if cleaned > 0 {
		logger.Debugf("cleaned %d expired sessions", cleaned)
		if err := s.save(); err != nil {
			logger.Errorf("save after cleanup: %v", err)
		}
	}
}

// save writes current sessions to file atomically
func (s *SessionStore) save() error {
	s.mu.RLock()
	data, err := json.Marshal(s.sessions)
	s.mu.RUnlock()

	if err != nil {
		return err
	}

	tmpPath := s.storePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.storePath)
}

// load reads sessions from file
func (s *SessionStore) load() error {
	data, err := os.ReadFile(s.storePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // First run, no file yet
		}
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sessions := make(map[string]*SessionData)
	if err := json.Unmarshal(data, &sessions); err != nil {
		return err
	}

	// Filter out already-expired sessions during load
	now := time.Now()
	for id, sess := range sessions {
		if now.Before(sess.ExpiresAt) {
			s.sessions[id] = sess
		}
	}

	return nil
}
