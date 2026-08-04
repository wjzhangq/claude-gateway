package quota

import (
	"testing"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/model"
)

func TestResolveBackendDaily(t *testing.T) {
	ks := auth.NewKeyStore()
	ks.SetQuotaOverride(42, 600)

	cases := []struct {
		name           string
		userID         int64
		backendDailyMax float64
		want           float64
	}{
		{"db override wins over global", 42, 200, 600},
		{"db override wins even when smaller", 42, 1000, 600},
		{"no override falls back to global", 7, 200, 200},
		{"no override and no global = unlimited", 7, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveBackendDaily(ks, tc.userID, tc.backendDailyMax); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExpiryDateStillValid locks the boundary shared by enforcement and the
// admin page: an override whose expires_at IS today is still active; it only
// lapses the day after.
func TestExpiryDateStillValid(t *testing.T) {
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	ks := auth.NewKeyStore()
	ks.LoadQuotaOverrides([]*model.UserQuotaOverride{
		{UserID: 1, QuotaUSD: 600, IsTemporary: true, ExpiresAt: &today},
		{UserID: 2, QuotaUSD: 600, IsTemporary: true, ExpiresAt: &yesterday},
		{UserID: 3, QuotaUSD: 600, IsTemporary: false},
	})

	if _, ok := ks.GetQuotaOverride(1); !ok {
		t.Error("override expiring today must still be active")
	}
	if _, ok := ks.GetQuotaOverride(2); ok {
		t.Error("override that expired yesterday must be inactive")
	}
	if _, ok := ks.GetQuotaOverride(3); !ok {
		t.Error("permanent override must be active")
	}
}
