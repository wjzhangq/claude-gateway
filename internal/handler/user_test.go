package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
)

func setupTestHandler(t *testing.T) (*UserHandler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	d, err := db.Init(filepath.Join(t.TempDir(), "test.db"), 1000)
	if err != nil {
		t.Fatalf("init test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	keyStore := auth.NewKeyStore()
	handler := NewUserHandler(d, keyStore)

	router := gin.New()
	router.POST("/admin/api/users", handler.CreateUser)
	router.GET("/admin/api/users/:id", handler.GetUser)

	return handler, router
}

func TestCreateUser_Idempotent(t *testing.T) {
	_, router := setupTestHandler(t)

	payload := map[string]interface{}{
		"itcode": "test123",
		"name":   "Test User",
		"role":   "user",
		"status": "active",
	}
	body, _ := json.Marshal(payload)

	// First create - should succeed with 201
	req1 := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)

	if w1.Code != http.StatusCreated {
		t.Fatalf("first create: expected 201, got %d: %s", w1.Code, w1.Body.String())
	}

	var resp1 map[string]interface{}
	if err := json.Unmarshal(w1.Body.Bytes(), &resp1); err != nil {
		t.Fatalf("parse first response: %v", err)
	}

	userMap1, ok := resp1["user"].(map[string]interface{})
	if !ok {
		t.Fatalf("first response missing user field")
	}

	if existed, _ := resp1["existed"].(bool); existed {
		t.Error("first create should have existed=false")
	}

	firstID := userMap1["id"].(float64)

	// Second create with same itcode - should return existing user with 200
	req2 := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("second create: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}

	var resp2 map[string]interface{}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("parse second response: %v", err)
	}

	userMap2, ok := resp2["user"].(map[string]interface{})
	if !ok {
		t.Fatalf("second response missing user field")
	}

	if existed, _ := resp2["existed"].(bool); !existed {
		t.Error("second create should have existed=true")
	}

	secondID := userMap2["id"].(float64)
	if secondID != firstID {
		t.Errorf("second create should return same user: got ID %v, want %v", secondID, firstID)
	}

	msg, _ := resp2["message"].(string)
	if msg == "" {
		t.Error("second create should include message")
	}
	t.Logf("Idempotent create message: %s", msg)
}

func TestCreateUser_NormalizedDuplicate(t *testing.T) {
	_, router := setupTestHandler(t)

	// Create with clean itcode
	payload1 := map[string]interface{}{
		"itcode": "test456",
		"name":   "Test User",
		"role":   "user",
		"status": "active",
	}
	body1, _ := json.Marshal(payload1)
	req1 := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)

	if w1.Code != http.StatusCreated {
		t.Fatalf("first create: expected 201, got %d", w1.Code)
	}

	// Try with whitespace - should be idempotent after normalization
	payload2 := map[string]interface{}{
		"itcode": "  test456  ",
		"name":   "Another User",
		"role":   "admin",
	}
	body2, _ := json.Marshal(payload2)
	req2 := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("whitespace variant: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}

	var resp2 map[string]interface{}
	json.Unmarshal(w2.Body.Bytes(), &resp2)

	if existed, _ := resp2["existed"].(bool); !existed {
		t.Error("whitespace variant should have existed=true")
	}

	// Try with @lenovo.com suffix
	payload3 := map[string]interface{}{
		"itcode": "test456@lenovo.com",
		"name":   "Yet Another",
		"role":   "user",
	}
	body3, _ := json.Marshal(payload3)
	req3 := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body3))
	req3.Header.Set("Content-Type", "application/json")
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Fatalf("@lenovo.com variant: expected 200, got %d: %s", w3.Code, w3.Body.String())
	}

	var resp3 map[string]interface{}
	json.Unmarshal(w3.Body.Bytes(), &resp3)

	if existed, _ := resp3["existed"].(bool); !existed {
		t.Error("@lenovo.com variant should have existed=true")
	}
}
