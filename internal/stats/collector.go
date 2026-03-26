package stats

import (
	"time"

	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/logger"
	"github.com/wjzhangq/claude-gateway/internal/model"
)

// Record holds the data for a single API call to be persisted.
type Record struct {
	UserID       int64
	APIKeyID     int64
	Model        string
	Backend      string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	CostUSD      float64
	StatusCode   int
	Latency      time.Duration
	IsOpenClaw   bool
	IsDowngraded bool
	UA           string
}

// Collector receives usage records asynchronously and batch-writes them to the DB.
type Collector struct {
	ch chan Record
	db *db.DB
}

// NewCollector creates a Collector with a buffered channel and starts the worker.
func NewCollector(database *db.DB, bufSize int) *Collector {
	c := &Collector{
		ch: make(chan Record, bufSize),
		db: database,
	}
	go c.worker()
	return c
}

// Emit sends a record to the collector. Drops silently if the channel is full.
func (c *Collector) Emit(r Record) {
	select {
	case c.ch <- r:
	default:
		logger.Warn("stats collector channel full, dropping record")
	}
}

// Flush drains all pending records in the channel and writes them to the DB.
// Blocks until the channel is empty at the time of the call.
func (c *Collector) Flush() {
	for {
		select {
		case r := <-c.ch:
			log := &model.UsageLog{
				UserID:       r.UserID,
				APIKeyID:     r.APIKeyID,
				Model:        r.Model,
				Backend:      r.Backend,
				InputTokens:  r.InputTokens,
				OutputTokens: r.OutputTokens,
				TotalTokens:  r.TotalTokens,
				CostUSD:      r.CostUSD,
				StatusCode:   r.StatusCode,
				Latency:      r.Latency.Milliseconds(),
				IsOpenClaw:   r.IsOpenClaw,
				IsDowngraded: r.IsDowngraded,
				UA:           r.UA,
			}
			if err := c.db.InsertUsageLog(log); err != nil {
				logger.Errorf("flush usage log: %v", err)
			}
		default:
			return
		}
	}
}

func (c *Collector) worker() {
	for r := range c.ch {
		log := &model.UsageLog{
			UserID:       r.UserID,
			APIKeyID:     r.APIKeyID,
			Model:        r.Model,
			Backend:      r.Backend,
			InputTokens:  r.InputTokens,
			OutputTokens: r.OutputTokens,
			TotalTokens:  r.TotalTokens,
			CostUSD:      r.CostUSD,
			StatusCode:   r.StatusCode,
			Latency:      r.Latency.Milliseconds(),
			IsOpenClaw:   r.IsOpenClaw,
			IsDowngraded: r.IsDowngraded,
			UA:           r.UA,
		}
		if err := c.db.InsertUsageLog(log); err != nil {
			logger.Errorf("insert usage log: %v", err)
		}
	}
}
