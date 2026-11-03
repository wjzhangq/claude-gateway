package auth_test

import (
	"sync"
	"testing"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/auth"
)

func TestSnapshotDailyCosts_ReturnsSeededValues(t *testing.T) {
	ks := auth.NewKeyStore()
	today := time.Now().Format("2006-01-02")
	ks.InitDailyCosts(today, map[int64]float64{1: 12.5, 2: 0.25})

	date, costs := ks.SnapshotDailyCosts()

	if date != today {
		t.Errorf("date = %q, want %q", date, today)
	}
	if len(costs) != 2 {
		t.Fatalf("len(costs) = %d, want 2", len(costs))
	}
	if costs[1] != 12.5 {
		t.Errorf("costs[1] = %v, want 12.5", costs[1])
	}
	if costs[2] != 0.25 {
		t.Errorf("costs[2] = %v, want 0.25", costs[2])
	}
}

// The snapshot must be a copy: callers hand it to JSON encoders and filters,
// and must not be able to corrupt the live counters that gate enforcement.
func TestSnapshotDailyCosts_ReturnsCopy(t *testing.T) {
	ks := auth.NewKeyStore()
	ks.InitDailyCosts(time.Now().Format("2006-01-02"), map[int64]float64{1: 10})

	_, costs := ks.SnapshotDailyCosts()
	costs[1] = 999
	costs[42] = 777

	if got := ks.GetDailyCost(1); got != 10 {
		t.Errorf("mutating the snapshot changed the store: GetDailyCost(1) = %v, want 10", got)
	}
	if got := ks.GetDailyCost(42); got != 0 {
		t.Errorf("mutating the snapshot added an entry: GetDailyCost(42) = %v, want 0", got)
	}
}

// Seeded with a stale date, the snapshot must report today and no spend —
// same rollover semantics as GetDailyCost.
func TestSnapshotDailyCosts_ResetsOnDateRollover(t *testing.T) {
	ks := auth.NewKeyStore()
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	ks.InitDailyCosts(yesterday, map[int64]float64{1: 10, 2: 20})

	date, costs := ks.SnapshotDailyCosts()

	if want := time.Now().Format("2006-01-02"); date != want {
		t.Errorf("date = %q, want %q", date, want)
	}
	if len(costs) != 0 {
		t.Errorf("costs = %v, want empty after rollover", costs)
	}
}

func TestSnapshotDailyCosts_Concurrent(t *testing.T) {
	ks := auth.NewKeyStore()
	ks.InitDailyCosts(time.Now().Format("2006-01-02"), nil)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ks.AddDailyCost(int64(n%5), 1)
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, costs := ks.SnapshotDailyCosts(); costs == nil {
				t.Error("snapshot returned a nil map")
			}
		}()
	}
	wg.Wait()

	_, costs := ks.SnapshotDailyCosts()
	var total float64
	for _, c := range costs {
		total += c
	}
	if total != 50 {
		t.Errorf("total spend = %v, want 50", total)
	}
}
