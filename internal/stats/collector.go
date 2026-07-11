package stats

import (
	"time"

	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/logger"
	"github.com/wjzhangq/claude-gateway/internal/model"
)

const (
	batchSize    = 100             // flush when this many records accumulate
	batchTimeout = 5 * time.Second // or after this duration
)

// Record holds the data for a single API call to be persisted.
type Record struct {
	UserID       int64
	GroupID      int
	APIKeyID     int64
	KeyStr       string // raw key string, used to update KeyStore.LastUsedAt
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
	ErrorReason  string
	IP           string
	City         string
	IsHQ         bool
	CreatedAt    time.Time
}

// Collector receives usage records asynchronously and batch-writes them to the DB.
type Collector struct {
	ch       chan Record
	db       *db.DB
	keyStore *auth.KeyStore
}

// NewCollector creates a Collector with a buffered channel and starts the worker.
func NewCollector(database *db.DB, ks *auth.KeyStore, bufSize int) *Collector {
	c := &Collector{
		ch:       make(chan Record, bufSize),
		db:       database,
		keyStore: ks,
	}
	go c.worker()
	return c
}

// Emit sends a record to the collector (non-blocking) and immediately updates
// the in-memory LastUsedAt and cost accumulators on the KeyStore — no DB write on the hot path.
func (c *Collector) Emit(r Record) {
	r.CreatedAt = time.Now()
	if r.KeyStr != "" && c.keyStore != nil {
		// Update last_used_at in memory immediately — O(1), no lock contention on writes
		c.keyStore.MarkUsed(r.KeyStr, r.CreatedAt)
		// Accumulate cost in memory; flushed to DB every minute
		if r.CostUSD > 0 {
			c.keyStore.AddCost(r.KeyStr, "backend", r.CostUSD)
		}
	}
	select {
	case c.ch <- r:
	default:
		logger.Warn("stats collector channel full, dropping record")
	}
}

// Flush drains all pending records and writes them to DB in one batch.
func (c *Collector) Flush() {
	var batch []*model.UsageLog
	for {
		select {
		case r := <-c.ch:
			batch = append(batch, recordToLog(r))
		default:
			goto done
		}
	}
done:
	if len(batch) > 0 {
		if err := c.db.BatchInsertUsageLogs(batch); err != nil {
			logger.Errorf("flush usage logs: %v", err)
		}
	}
}

func (c *Collector) worker() {
	ticker := time.NewTicker(batchTimeout)
	defer ticker.Stop()
	batch := make([]*model.UsageLog, 0, batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := c.db.BatchInsertUsageLogs(batch); err != nil {
			logger.Errorf("batch insert usage logs: %v", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case r, ok := <-c.ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, recordToLog(r))
			if len(batch) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func recordToLog(r Record) *model.UsageLog {
	return &model.UsageLog{
		UserID:       r.UserID,
		GroupID:      r.GroupID,
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
		ErrorReason:  r.ErrorReason,
		IP:           r.IP,
		City:         r.City,
		IsHQ:         r.IsHQ,
		CreatedAt:    r.CreatedAt,
	}
}
