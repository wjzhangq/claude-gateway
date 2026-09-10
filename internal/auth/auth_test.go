package auth_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/auth"
)

func TestCodeStore_SetAndVerify(t *testing.T) {
	cs := auth.NewCodeStore(5 * time.Minute)

	cs.Set("13800000001", "123456")

	// Wrong code
	if cs.Verify("13800000001", "000000") {
		t.Fatal("expected false for wrong code")
	}

	// Correct code
	if !cs.Verify("13800000001", "123456") {
		t.Fatal("expected true for correct code")
	}

	// Code should be consumed after successful verify
	if cs.Verify("13800000001", "123456") {
		t.Fatal("expected false after code consumed")
	}
}

func TestCodeStore_Expiry(t *testing.T) {
	cs := auth.NewCodeStore(50 * time.Millisecond)
	cs.Set("13800000002", "654321")

	time.Sleep(100 * time.Millisecond)

	if cs.Verify("13800000002", "654321") {
		t.Fatal("expected false for expired code")
	}
}

func TestKeyStore_AddAndGet(t *testing.T) {
	ks := auth.NewKeyStore()

	key, err := auth.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	info := &auth.KeyInfo{
		KeyID:         1,
		UserID:        42,
		DailyQuotaUSD: 10.0,
		UserStatus:    "active",
	}
	ks.Add(key, info)

	got := ks.Get(key)
	if got == nil {
		t.Fatal("expected key info, got nil")
	}
	if got.UserID != 42 {
		t.Fatalf("expected UserID 42, got %d", got.UserID)
	}

	ks.Remove(key)
	if ks.Get(key) != nil {
		t.Fatal("expected nil after remove")
	}
}

func TestGenerateKey(t *testing.T) {
	key, err := auth.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if len(key) < 10 {
		t.Fatalf("key too short: %s", key)
	}
	if key[:3] != "sk-" {
		t.Fatalf("key should start with sk-: %s", key)
	}
}

func TestSessionStore_Persistence(t *testing.T) {
	tmpFile := t.TempDir() + "/sessions.json"

	// Create store and add sessions
	store1 := auth.NewSessionStore(tmpFile)
	store1.Set("sess_001", 123, "admin", 24*time.Hour)
	store1.Set("sess_002", 456, "user", 7*24*time.Hour)

	// Force save and close
	if err := store1.Close(); err != nil {
		t.Fatalf("close store1: %v", err)
	}

	// Create new store from same file (simulates restart)
	store2 := auth.NewSessionStore(tmpFile)
	defer store2.Close()

	// Verify sessions were restored
	data1, ok1 := store2.Get("sess_001")
	if !ok1 {
		t.Fatal("expected sess_001 to be restored")
	}
	if data1.UserID != 123 || data1.UserRole != "admin" {
		t.Fatalf("wrong data for sess_001: %+v", data1)
	}

	data2, ok2 := store2.Get("sess_002")
	if !ok2 {
		t.Fatal("expected sess_002 to be restored")
	}
	if data2.UserID != 456 || data2.UserRole != "user" {
		t.Fatalf("wrong data for sess_002: %+v", data2)
	}
}

func TestSessionStore_ExpiredNotLoadedAfterRestart(t *testing.T) {
	tmpFile := t.TempDir() + "/sessions_expiry.json"

	// Write a pre-expired session directly to the file, simulating
	// a session that expired while the server was offline.
	expired := map[string]auth.SessionData{
		"expired_sess": {UserID: 999, UserRole: "user", ExpiresAt: time.Now().Add(-time.Hour)},
	}
	raw, err := json.Marshal(expired)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(tmpFile, raw, 0600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	store := auth.NewSessionStore(tmpFile)
	defer store.Close()

	if _, ok := store.Get("expired_sess"); ok {
		t.Fatal("expected pre-expired session to be filtered out on load")
	}
}
