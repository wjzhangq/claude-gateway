package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/model"
)

type statusResp struct {
	Date                  string  `json:"date"`
	GlobalBackendDailyMax float64 `json:"global_backend_daily_max"`
	Users                 []struct {
		UserID         int64   `json:"user_id"`
		Itcode         string  `json:"itcode"`
		Name           string  `json:"name"`
		DailyLimit     float64 `json:"daily_limit"`
		DailyUsed      float64 `json:"daily_used"`
		DailyRemaining float64 `json:"daily_remaining"`
		UsedRatio      float64 `json:"used_ratio"`
		LimitSource    string  `json:"limit_source"`
		Override       *struct {
			QuotaUSD    float64 `json:"quota_usd"`
			IsTemporary bool    `json:"is_temporary"`
			ExpiresAt   *string `json:"expires_at"`
			Note        string  `json:"note"`
			IsExpired   bool    `json:"is_expired"`
		} `json:"override"`
	} `json:"users"`
}

// setupQuotaTest builds a Status-serving router over a temp DB, seeding the
// given itcodes as active users. Returns the router and itcode -> userID.
func setupQuotaTest(t *testing.T, backendDailyMax float64, itcodes ...string) (*gin.Engine, *auth.KeyStore, *db.DB, map[string]int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	d, err := db.Init(filepath.Join(t.TempDir(), "test.db"), 1000)
	if err != nil {
		t.Fatalf("init test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	ids := make(map[string]int64, len(itcodes))
	for _, code := range itcodes {
		u := &model.User{Itcode: code, Name: "N-" + code, Role: "user", Status: "active"}
		if err := d.CreateUser(u); err != nil {
			t.Fatalf("create user %s: %v", code, err)
		}
		ids[code] = u.ID
	}

	ks := auth.NewKeyStore()
	cfg := &config.Config{BackendDailyMax: backendDailyMax}
	h := NewQuotaOverrideHandler(d, cfg, ks)

	r := gin.New()
	r.GET("/quota/status", h.Status)
	return r, ks, d, ids
}

func getStatus(t *testing.T, r *gin.Engine, query string) statusResp {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/quota/status"+query, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", query, w.Code, w.Body.String())
	}
	var resp statusResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v (body %s)", err, w.Body.String())
	}
	return resp
}

// limit_source must name who is capping the user, so an operator can tell an
// override apart from the global default without a second lookup.
func TestStatus_LimitSource(t *testing.T) {
	r, ks, d, ids := setupQuotaTest(t, 20, "withoverride", "globalonly")

	if err := d.UpsertUserQuotaOverride(ids["withoverride"], 50, false, nil, "manual"); err != nil {
		t.Fatalf("upsert override: %v", err)
	}
	ks.SetQuotaOverride(ids["withoverride"], 50)

	resp := getStatus(t, r, "?itcode=withoverride,globalonly")
	if len(resp.Users) != 2 {
		t.Fatalf("got %d users, want 2", len(resp.Users))
	}
	byCode := map[string]int{}
	for i, u := range resp.Users {
		byCode[u.Itcode] = i
	}

	o := resp.Users[byCode["withoverride"]]
	if o.LimitSource != "override" {
		t.Errorf("withoverride: limit_source = %q, want \"override\"", o.LimitSource)
	}
	if o.DailyLimit != 50 {
		t.Errorf("withoverride: daily_limit = %v, want 50 (override beats global)", o.DailyLimit)
	}
	if o.Override == nil || o.Override.QuotaUSD != 50 || o.Override.Note != "manual" {
		t.Errorf("withoverride: override block = %+v, want quota 50 note manual", o.Override)
	}

	g := resp.Users[byCode["globalonly"]]
	if g.LimitSource != "global" {
		t.Errorf("globalonly: limit_source = %q, want \"global\"", g.LimitSource)
	}
	if g.DailyLimit != 20 {
		t.Errorf("globalonly: daily_limit = %v, want 20", g.DailyLimit)
	}
	if g.Override != nil {
		t.Errorf("globalonly: override = %+v, want null", g.Override)
	}
}

func TestStatus_UnlimitedWhenNoOverrideAndNoGlobal(t *testing.T) {
	r, _, _, _ := setupQuotaTest(t, 0, "nolimit")

	resp := getStatus(t, r, "?itcode=nolimit")
	if len(resp.Users) != 1 {
		t.Fatalf("got %d users, want 1", len(resp.Users))
	}
	u := resp.Users[0]
	if u.LimitSource != "unlimited" {
		t.Errorf("limit_source = %q, want \"unlimited\"", u.LimitSource)
	}
	if u.DailyLimit != 0 {
		t.Errorf("daily_limit = %v, want 0", u.DailyLimit)
	}
	if u.DailyRemaining != 0 {
		t.Errorf("daily_remaining = %v, want 0 for unlimited", u.DailyRemaining)
	}
	if u.UsedRatio != 0 {
		t.Errorf("used_ratio = %v, want 0 for unlimited", u.UsedRatio)
	}
}

// Spend must come from the in-process counter, the same value the proxy gates on.
func TestStatus_UsedComesFromKeyStore(t *testing.T) {
	r, ks, _, ids := setupQuotaTest(t, 100, "spender")
	ks.AddDailyCost(ids["spender"], 30.5)

	u := getStatus(t, r, "?itcode=spender").Users[0]
	if u.DailyUsed != 30.5 {
		t.Errorf("daily_used = %v, want 30.5", u.DailyUsed)
	}
	if u.DailyRemaining != 69.5 {
		t.Errorf("daily_remaining = %v, want 69.5", u.DailyRemaining)
	}
	if u.UsedRatio < 0.304 || u.UsedRatio > 0.306 {
		t.Errorf("used_ratio = %v, want ~0.305", u.UsedRatio)
	}
}

// Overspend must clamp remaining at zero rather than report a negative balance.
func TestStatus_OverspendClampsRemaining(t *testing.T) {
	r, ks, _, ids := setupQuotaTest(t, 10, "over")
	ks.AddDailyCost(ids["over"], 25)

	u := getStatus(t, r, "?itcode=over").Users[0]
	if u.DailyRemaining != 0 {
		t.Errorf("daily_remaining = %v, want 0", u.DailyRemaining)
	}
	if u.UsedRatio != 2.5 {
		t.Errorf("used_ratio = %v, want 2.5", u.UsedRatio)
	}
}

func TestStatus_UnknownItcodeIsSkipped(t *testing.T) {
	r, _, _, _ := setupQuotaTest(t, 20, "real")

	resp := getStatus(t, r, "?itcode=real,ghost")
	if len(resp.Users) != 1 {
		t.Fatalf("got %d users, want 1 (ghost skipped)", len(resp.Users))
	}
	if resp.Users[0].Itcode != "real" {
		t.Errorf("itcode = %q, want \"real\"", resp.Users[0].Itcode)
	}
}

// over=true is the "who is about to be cut off" query: it must keep users at or
// above the threshold and drop the rest, including unlimited users.
func TestStatus_OverFilter(t *testing.T) {
	r, ks, _, ids := setupQuotaTest(t, 100, "hot", "cold")
	ks.AddDailyCost(ids["hot"], 90)
	ks.AddDailyCost(ids["cold"], 10)

	resp := getStatus(t, r, "?over=true&threshold=0.8")
	if len(resp.Users) != 1 {
		t.Fatalf("got %d users, want 1", len(resp.Users))
	}
	if resp.Users[0].Itcode != "hot" {
		t.Errorf("itcode = %q, want \"hot\"", resp.Users[0].Itcode)
	}

	if n := len(getStatus(t, r, "?over=true&threshold=0.05").Users); n != 2 {
		t.Errorf("threshold 0.05: got %d users, want 2", n)
	}
	if n := len(getStatus(t, r, "?over=true").Users); n != 1 {
		t.Errorf("default threshold: got %d users, want 1", n)
	}
}

func TestStatus_OverFilterExcludesUnlimited(t *testing.T) {
	r, ks, _, ids := setupQuotaTest(t, 0, "unlimited")
	ks.AddDailyCost(ids["unlimited"], 500)

	if n := len(getStatus(t, r, "?over=true").Users); n != 0 {
		t.Errorf("got %d users, want 0 (unlimited user has no ratio to exceed)", n)
	}
}

// An expired temporary override must not be reported as the effective limit —
// enforcement already ignores it, so the display must agree.
func TestStatus_ExpiredTemporaryOverrideFallsBackToGlobal(t *testing.T) {
	r, ks, d, ids := setupQuotaTest(t, 20, "expired")

	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	if err := d.UpsertUserQuotaOverride(ids["expired"], 500, true, &yesterday, "temp"); err != nil {
		t.Fatalf("upsert override: %v", err)
	}
	// Mirrors startup/midnight load, which drops expired overrides.
	ks.LoadQuotaOverrides([]*model.UserQuotaOverride{
		{UserID: ids["expired"], QuotaUSD: 500, IsTemporary: true, ExpiresAt: &yesterday},
	})

	u := getStatus(t, r, "?itcode=expired").Users[0]
	if u.DailyLimit != 20 {
		t.Errorf("daily_limit = %v, want 20 (expired override ignored)", u.DailyLimit)
	}
	if u.LimitSource != "global" {
		t.Errorf("limit_source = %q, want \"global\"", u.LimitSource)
	}
	if u.Override == nil || !u.Override.IsExpired {
		t.Errorf("override block should still be shown with is_expired=true, got %+v", u.Override)
	}
}

func TestStatus_ReportsDateAndGlobalMax(t *testing.T) {
	r, _, _, _ := setupQuotaTest(t, 42.5, "someone")

	resp := getStatus(t, r, "?itcode=someone")
	if want := time.Now().Format("2006-01-02"); resp.Date != want {
		t.Errorf("date = %q, want %q", resp.Date, want)
	}
	if resp.GlobalBackendDailyMax != 42.5 {
		t.Errorf("global_backend_daily_max = %v, want 42.5", resp.GlobalBackendDailyMax)
	}
}

// ── API key auth on the db-explorer route group ───────────────────────────────

// setupQuotaAuthTest mounts the quota routes behind AdminAPIKeyAuth exactly as
// cmd/server/main.go does, and returns an admin key and a plain-user key.
func setupQuotaAuthTest(t *testing.T) (*gin.Engine, string, string, map[string]int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	d, err := db.Init(filepath.Join(t.TempDir(), "test.db"), 1000)
	if err != nil {
		t.Fatalf("init test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	ids := map[string]int64{}
	mkUser := func(itcode, role string) int64 {
		u := &model.User{Itcode: itcode, Name: "N-" + itcode, Role: role, Status: "active"}
		if err := d.CreateUser(u); err != nil {
			t.Fatalf("create user %s: %v", itcode, err)
		}
		ids[itcode] = u.ID
		return u.ID
	}
	adminID := mkUser("adminuser", "admin")
	plainID := mkUser("plainuser", "user")
	mkUser("target", "user")

	ks := auth.NewKeyStore()
	adminKey := "sk-admin-" + t.Name()
	plainKey := "sk-plain-" + t.Name()
	ks.Add(adminKey, &auth.KeyInfo{KeyID: 1, UserID: adminID, UserStatus: "active"})
	ks.Add(plainKey, &auth.KeyInfo{KeyID: 2, UserID: plainID, UserStatus: "active"})

	cfg := &config.Config{BackendDailyMax: 20}
	quotaH := NewQuotaOverrideHandler(d, cfg, ks)
	dbExplorerH := NewDBExplorerHandler(d, ks, cfg)

	r := gin.New()
	g := r.Group("/admin/api/db")
	g.Use(dbExplorerH.AdminAPIKeyAuth())
	{
		g.GET("/quota/status", quotaH.Status)
		g.GET("/quota/overrides", quotaH.List)
		g.PUT("/quota/overrides/:itcode", quotaH.Upsert)
		g.DELETE("/quota/overrides/:itcode", quotaH.Delete)
	}
	return r, adminKey, plainKey, ids
}

func doReq(t *testing.T, r *gin.Engine, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestQuotaRoutes_RejectMissingKey(t *testing.T) {
	r, _, _, _ := setupQuotaAuthTest(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/admin/api/db/quota/status?itcode=target"},
		{http.MethodGet, "/admin/api/db/quota/overrides"},
		{http.MethodPut, "/admin/api/db/quota/overrides/target"},
		{http.MethodDelete, "/admin/api/db/quota/overrides/target"},
	} {
		if w := doReq(t, r, tc.method, tc.path, "", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without key: status %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}

// A plain user's key must not be able to read or change anyone's quota.
func TestQuotaRoutes_RejectNonAdminKey(t *testing.T) {
	r, _, plainKey, _ := setupQuotaAuthTest(t)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/admin/api/db/quota/status?itcode=target", ""},
		{http.MethodGet, "/admin/api/db/quota/overrides", ""},
		{http.MethodPut, "/admin/api/db/quota/overrides/target", `{"quota_usd":999}`},
		{http.MethodDelete, "/admin/api/db/quota/overrides/target", ""},
	} {
		if w := doReq(t, r, tc.method, tc.path, plainKey, tc.body); w.Code != http.StatusForbidden {
			t.Errorf("%s %s with plain key: status %d, want 403", tc.method, tc.path, w.Code)
		}
	}
}

func TestQuotaRoutes_RejectUnknownKey(t *testing.T) {
	r, _, _, _ := setupQuotaAuthTest(t)

	w := doReq(t, r, http.MethodGet, "/admin/api/db/quota/status?itcode=target", "sk-nonexistent", "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unknown key: status %d, want 401", w.Code)
	}
}

// The round trip the skill performs: read, write, read back. The second read
// must show the new limit, which proves the in-memory cache was synced — a
// direct SQL UPDATE would not show up until midnight.
func TestQuotaRoutes_UpsertThenStatusReflectsNewLimit(t *testing.T) {
	r, adminKey, _, _ := setupQuotaAuthTest(t)

	before := getStatusWithKey(t, r, adminKey, "?itcode=target")
	if before.Users[0].DailyLimit != 20 || before.Users[0].LimitSource != "global" {
		t.Fatalf("before: limit %v source %q, want 20/global",
			before.Users[0].DailyLimit, before.Users[0].LimitSource)
	}

	w := doReq(t, r, http.MethodPut, "/admin/api/db/quota/overrides/target", adminKey,
		`{"quota_usd":200,"note":"raised for release"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: status %d: %s", w.Code, w.Body.String())
	}

	after := getStatusWithKey(t, r, adminKey, "?itcode=target")
	u := after.Users[0]
	if u.DailyLimit != 200 {
		t.Errorf("after PUT: daily_limit = %v, want 200", u.DailyLimit)
	}
	if u.LimitSource != "override" {
		t.Errorf("after PUT: limit_source = %q, want \"override\"", u.LimitSource)
	}
	if u.Override == nil || u.Override.Note != "raised for release" {
		t.Errorf("after PUT: override = %+v, want note \"raised for release\"", u.Override)
	}

	if w := doReq(t, r, http.MethodDelete, "/admin/api/db/quota/overrides/target", adminKey, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status %d: %s", w.Code, w.Body.String())
	}

	restored := getStatusWithKey(t, r, adminKey, "?itcode=target")
	if restored.Users[0].DailyLimit != 20 || restored.Users[0].LimitSource != "global" {
		t.Errorf("after DELETE: limit %v source %q, want 20/global",
			restored.Users[0].DailyLimit, restored.Users[0].LimitSource)
	}
	if restored.Users[0].Override != nil {
		t.Errorf("after DELETE: override = %+v, want null", restored.Users[0].Override)
	}
}

func TestQuotaRoutes_UpsertRejectsNegativeQuota(t *testing.T) {
	r, adminKey, _, _ := setupQuotaAuthTest(t)

	w := doReq(t, r, http.MethodPut, "/admin/api/db/quota/overrides/target", adminKey, `{"quota_usd":-5}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("negative quota: status %d, want 400", w.Code)
	}
}

// A temporary override without an expiry date must be refused rather than
// silently stored as permanent.
func TestQuotaRoutes_TemporaryRequiresExpiry(t *testing.T) {
	r, adminKey, _, _ := setupQuotaAuthTest(t)

	w := doReq(t, r, http.MethodPut, "/admin/api/db/quota/overrides/target", adminKey,
		`{"quota_usd":100,"is_temporary":true}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("temporary without expires_at: status %d, want 400", w.Code)
	}

	w = doReq(t, r, http.MethodPut, "/admin/api/db/quota/overrides/target", adminKey,
		`{"quota_usd":100,"is_temporary":true,"expires_at":"not-a-date"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("temporary with bad expires_at: status %d, want 400", w.Code)
	}
}

func TestQuotaRoutes_UpsertUnknownItcode(t *testing.T) {
	r, adminKey, _, _ := setupQuotaAuthTest(t)

	w := doReq(t, r, http.MethodPut, "/admin/api/db/quota/overrides/ghost", adminKey, `{"quota_usd":100}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown itcode: status %d, want 404", w.Code)
	}
}

func getStatusWithKey(t *testing.T, r *gin.Engine, key, query string) statusResp {
	t.Helper()
	w := doReq(t, r, http.MethodGet, "/admin/api/db/quota/status"+query, key, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", query, w.Code, w.Body.String())
	}
	var resp statusResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v (body %s)", err, w.Body.String())
	}
	if len(resp.Users) == 0 {
		t.Fatalf("GET %s returned no users: %s", query, w.Body.String())
	}
	return resp
}
