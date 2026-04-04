package stats

import (
	"time"

	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/logger"
	"github.com/wjzhangq/claude-gateway/internal/model"
)

// AWSRecord holds data for a single AWS Bedrock API call.
type AWSRecord struct {
	UserID           int64
	GroupID          int
	APIKeyID         int64
	KeyStr           string // raw key string, used to update KeyStore.LastUsedAt
	Model            string // requested model name
	BedrockModel     string // actual Bedrock model ARN/ID used
	InputTokens      int
	OutputTokens     int
	TotalTokens      int
	CacheReadTokens  int
	CacheWriteTokens int
	CostUSD          float64
	StatusCode       int
	Latency          time.Duration
	UA               string
	CreatedAt        time.Time
}

// AWSCollector receives AWS usage records asynchronously and batch-writes them.
type AWSCollector struct {
	ch       chan AWSRecord
	db       *db.DB
	keyStore *auth.KeyStore
}

// NewAWSCollector creates an AWSCollector with a buffered channel and starts the worker.
func NewAWSCollector(database *db.DB, ks *auth.KeyStore, bufSize int) *AWSCollector {
	c := &AWSCollector{
		ch:       make(chan AWSRecord, bufSize),
		db:       database,
		keyStore: ks,
	}
	go c.worker()
	return c
}

// Emit sends a record to the collector (non-blocking).
func (c *AWSCollector) Emit(r AWSRecord) {
	r.CreatedAt = time.Now()
	if r.KeyStr != "" && c.keyStore != nil {
		c.keyStore.MarkUsed(r.KeyStr, r.CreatedAt)
		// Accumulate AWS cost in memory; flushed to DB every minute
		if r.CostUSD > 0 {
			c.keyStore.AddCost(r.KeyStr, "aws", r.CostUSD)
		}
	}
	select {
	case c.ch <- r:
	default:
		logger.Warn("aws stats collector channel full, dropping record")
	}
}

// Flush drains all pending records and writes them to DB.
func (c *AWSCollector) Flush() {
	var batch []*model.AWSUsageLog
	for {
		select {
		case r := <-c.ch:
			batch = append(batch, awsRecordToLog(r))
		default:
			goto done
		}
	}
done:
	if len(batch) > 0 {
		if err := c.db.BatchInsertAWSUsageLogs(batch); err != nil {
			logger.Errorf("flush aws usage logs: %v", err)
		}
	}
}

func (c *AWSCollector) worker() {
	ticker := time.NewTicker(batchTimeout)
	defer ticker.Stop()
	batch := make([]*model.AWSUsageLog, 0, batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := c.db.BatchInsertAWSUsageLogs(batch); err != nil {
			logger.Errorf("batch insert aws usage logs: %v", err)
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
			batch = append(batch, awsRecordToLog(r))
			if len(batch) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func awsRecordToLog(r AWSRecord) *model.AWSUsageLog {
	return &model.AWSUsageLog{
		UserID:           r.UserID,
		GroupID:          r.GroupID,
		APIKeyID:         r.APIKeyID,
		Model:            r.Model,
		BedrockModel:     r.BedrockModel,
		InputTokens:      r.InputTokens,
		OutputTokens:     r.OutputTokens,
		TotalTokens:      r.TotalTokens,
		CacheReadTokens:  r.CacheReadTokens,
		CacheWriteTokens: r.CacheWriteTokens,
		CostUSD:          r.CostUSD,
		StatusCode:       r.StatusCode,
		Latency:          r.Latency.Milliseconds(),
		UA:               r.UA,
		CreatedAt:        r.CreatedAt,
	}
}
