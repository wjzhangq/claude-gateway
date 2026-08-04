// Package quota is the single source of truth for per-user backend spending
// limits. Both the enforcement path (proxy / public proxy) and every display
// path (dashboard, /api/quota, response headers) MUST resolve limits through
// this package, so the shown quota always equals the enforced quota.
package quota

import "github.com/wjzhangq/claude-gateway/internal/auth"

// ResolveBackendDaily returns the effective backend daily spending limit in
// USD for a user (0 = unlimited).
//
// Priority (highest first):
//  1. DB override via KeyStore (user_quota_overrides, set by admin).
//  2. Global backend_daily_max from config.
//
// YAML user_daily_limits and users.daily_quota_usd are intentionally NOT
// consulted — backend limits are DB-only.
func ResolveBackendDaily(ks *auth.KeyStore, userID int64, backendDailyMax float64) float64 {
	if ks != nil {
		if override, ok := ks.GetQuotaOverride(userID); ok {
			return override
		}
	}
	if backendDailyMax > 0 {
		return backendDailyMax
	}
	return 0
}
