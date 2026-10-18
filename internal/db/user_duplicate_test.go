package db

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/wjzhangq/claude-gateway/internal/model"
)

func TestCreateUser_DuplicateItcode(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create first user
	user1 := &model.User{
		Itcode: "test123",
		Name:   "Test User",
		Role:   "user",
		Status: "active",
	}
	if err := db.CreateUser(user1); err != nil {
		t.Fatalf("CreateUser first: %v", err)
	}

	// Attempt to create duplicate
	user2 := &model.User{
		Itcode: "test123",
		Name:   "Another User",
		Role:   "user",
		Status: "active",
	}
	err := db.CreateUser(user2)
	if err == nil {
		t.Fatal("expected duplicate error, got nil")
	}
	if !errors.Is(err, ErrDuplicateItcode) {
		t.Errorf("expected ErrDuplicateItcode, got %v", err)
	}
}

func TestCreateUser_NormalizeItcode(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tests := []struct {
		input    string
		expected string
	}{
		{"test123", "test123"},
		{" test123 ", "test123"},
		{"test123@lenovo.com", "test123"},
		{" test123@lenovo.com ", "test123"},
		{"  user1  @lenovo.com  ", "user1"},
	}

	for _, tt := range tests {
		user := &model.User{
			Itcode: tt.input,
			Name:   "Test",
			Role:   "user",
			Status: "active",
		}
		if err := db.CreateUser(user); err != nil {
			t.Fatalf("CreateUser(%q): %v", tt.input, err)
		}
		if user.Itcode != tt.expected {
			t.Errorf("input %q: got itcode %q, want %q", tt.input, user.Itcode, tt.expected)
		}
		// Clean up for next iteration
		db.Exec("DELETE FROM users WHERE id=?", user.ID)
	}
}

func TestCreateUser_NormalizePreventsCollision(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create with trimmed itcode
	user1 := &model.User{
		Itcode: "collision",
		Name:   "First",
		Role:   "user",
		Status: "active",
	}
	if err := db.CreateUser(user1); err != nil {
		t.Fatalf("CreateUser first: %v", err)
	}

	// Attempt with whitespace version - should fail after normalization
	user2 := &model.User{
		Itcode: "  collision  ",
		Name:   "Second",
		Role:   "user",
		Status: "active",
	}
	err := db.CreateUser(user2)
	if !errors.Is(err, ErrDuplicateItcode) {
		t.Errorf("expected ErrDuplicateItcode for whitespace variant, got %v", err)
	}

	// Attempt with @lenovo.com suffix - should also fail
	user3 := &model.User{
		Itcode: "collision@lenovo.com",
		Name:   "Third",
		Role:   "user",
		Status: "active",
	}
	err = db.CreateUser(user3)
	if !errors.Is(err, ErrDuplicateItcode) {
		t.Errorf("expected ErrDuplicateItcode for @lenovo.com variant, got %v", err)
	}
}

func TestUpdateUserItcode_Duplicate(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create two users
	user1 := &model.User{Itcode: "user1", Name: "User One", Role: "user", Status: "active"}
	user2 := &model.User{Itcode: "user2", Name: "User Two", Role: "user", Status: "active"}

	if err := db.CreateUser(user1); err != nil {
		t.Fatalf("CreateUser user1: %v", err)
	}
	if err := db.CreateUser(user2); err != nil {
		t.Fatalf("CreateUser user2: %v", err)
	}

	// Try to rename user2 to user1's itcode - should fail
	err := db.UpdateUserItcode(user2.ID, "user1")
	if !errors.Is(err, ErrDuplicateItcode) {
		t.Errorf("expected ErrDuplicateItcode when renaming to existing itcode, got %v", err)
	}

	// Verify user2's itcode unchanged
	updated, _ := db.GetUserByID(user2.ID)
	if updated.Itcode != "user2" {
		t.Errorf("itcode should remain user2, got %s", updated.Itcode)
	}
}

func setupTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Init(filepath.Join(t.TempDir(), "test.db"), 1000)
	if err != nil {
		t.Fatalf("init test db: %v", err)
	}
	return d
}
