package stats

import (
	"time"

	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/logger"
)

// Aggregator runs a periodic job to roll up usage_logs into daily_stats.
type Aggregator struct {
	db       *db.DB
	interval time.Duration
}

func NewAggregator(database *db.DB, interval time.Duration) *Aggregator {
	return &Aggregator{db: database, interval: interval}
}

// Start launches the aggregation loop in the background.
func (a *Aggregator) Start() {
	go a.loop()
}

func (a *Aggregator) loop() {
	// Run once immediately on startup, then on interval.
	a.run()
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for range ticker.C {
		a.run()
	}
}

func (a *Aggregator) run() {
	if err := a.db.AggregateDaily(); err != nil {
		logger.Errorf("daily stats aggregation: %v", err)
	}
}
