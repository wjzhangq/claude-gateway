package auth_test

import (
	"testing"
	"time"

	"github.com/claude-gateway/claude-gateway/internal/auth"
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
		KeyID:       1,
		UserID:      42,
		QuotaTokens: 1000000,
		UserStatus:  "active",
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
