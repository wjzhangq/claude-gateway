package migration

import (
	"fmt"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/logger"
)

// MigrateConfigYamlQuotas imports user_daily_limits from config.yaml into the
// user_quota_overrides table. It is idempotent — skipped if already done.
// config.yaml entries are written as permanent overrides and take precedence
// over any row already inserted by migration 51 (users.daily_quota_usd).
func MigrateConfigYamlQuotas(cfg *config.Config, database *db.DB, store *auth.KeyStore) error {
	done, err := database.IsQuotaMigrationDone()
	if err != nil {
		return fmt.Errorf("check quota migration flag: %w", err)
	}
	if done {
		return nil
	}

	migrated := 0
	lookupFailed := false
	for _, limit := range cfg.UserDailyLimits {
		if limit.BackendDailyUSD <= 0 {
			continue
		}
		user, err := database.GetUserByItcode(limit.Itcode)
		if err != nil {
			// Transient DB error — leave the flag unset so the next startup
			// retries instead of silently dropping this user's limit.
			logger.Warnf("quota migration: lookup user %q failed, will retry next start: %v", limit.Itcode, err)
			lookupFailed = true
			continue
		}
		if user == nil {
			// Permanently missing user — nothing to retry, don't block the flag.
			logger.Warnf("quota migration: user %q not found in DB, skipping", limit.Itcode)
			continue
		}
		if err := database.UpsertUserQuotaOverride(
			user.ID, limit.BackendDailyUSD, false, nil, "migrated from config.yaml",
		); err != nil {
			return fmt.Errorf("upsert quota override for %s: %w", limit.Itcode, err)
		}
		store.SetQuotaOverride(user.ID, limit.BackendDailyUSD)
		migrated++
	}

	if lookupFailed {
		return fmt.Errorf("quota migration incomplete: some user lookups failed, will retry next start")
	}
	if err := database.MarkQuotaMigrationDone(); err != nil {
		return fmt.Errorf("mark quota migration done: %w", err)
	}
	logger.Infof("quota migration: migrated %d entries from config.yaml user_daily_limits", migrated)
	return nil
}
