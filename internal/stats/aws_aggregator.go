package stats

import (
	"time"

	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/logger"
)

// AWSAggregator rolls up aws_usage_logs into aws_daily_stats on a schedule.
type AWSAggregator struct {
	db       *db.DB
	interval time.Duration
}

// NewAWSAggregator creates an AWSAggregator.
func NewAWSAggregator(database *db.DB, interval time.Duration) *AWSAggregator {
	return &AWSAggregator{db: database, interval: interval}
}

// Start launches the aggregation loop in the background.
func (a *AWSAggregator) Start() {
	go a.loop()
}

func (a *AWSAggregator) loop() {
	a.run()
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for range ticker.C {
		a.run()
	}
}

// RunNow triggers an immediate aggregation (used during reload).
func (a *AWSAggregator) RunNow() {
	a.run()
}

func (a *AWSAggregator) run() {
	if err := a.db.AggregateAWSDaily(); err != nil {
		logger.Errorf("aws daily stats aggregation: %v", err)
	}
}
