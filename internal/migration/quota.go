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
	for _, limit := range cfg.UserDailyLimits {
		if limit.BackendDailyUSD <= 0 {
			continue
		}
		user, err := database.GetUserByItcode(limit.Itcode)
		if err != nil {
			return fmt.Errorf("lookup user %s: %w", limit.Itcode, err)
		}
		if user == nil {
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

	if err := database.MarkQuotaMigrationDone(); err != nil {
		return fmt.Errorf("mark quota migration done: %w", err)
	}
	logger.Infof("quota migration: migrated %d entries from config.yaml user_daily_limits", migrated)
	return nil
}
