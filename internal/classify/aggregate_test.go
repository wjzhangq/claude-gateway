package classify

import (
	"testing"
	"time"
)

// mkRec is a terse Record builder for the aggregation tests.
func mkRec(user int64, at time.Time, taskType string, work int, dir, reason string, tools ...string) Record {
	return Record{
		UserID:        user,
		CreatedAt:     at,
		TaskType:      taskType,
		WorkRelated:   work,
		CodeDirection: dir,
		WorkReason:    reason,
		Tools:         tools,
	}
}

func TestAggregate_CountsAndSampling(t *testing.T) {
	cfg := DefaultConfig()
	// Fixed weekday midday so nothing is off-hours by the clock.
	noon := time.Date(2026, 7, 8, 12, 0, 0, 0, time.Local) // Wednesday
	recs := []Record{
		mkRec(42, noon, "code", 1, "后端", "命中内部仓库 x", "Edit"),
		mkRec(42, noon, "code", 0, "前端", "个人项目", "Edit", "Bash"),
		mkRec(42, noon, "doc", 0, "", "写个人博客"),
		mkRec(42, noon, "", -1, "", ""), // continuation / unanalyzed — not a logical task
	}
	r := Aggregate(recs, noon.Add(-24*time.Hour), cfg)

	if r.PhysicalCount != 4 {
		t.Errorf("PhysicalCount = %d, want 4", r.PhysicalCount)
	}
	if r.LogicalTasks != 3 {
		t.Errorf("LogicalTasks = %d, want 3", r.LogicalTasks)
	}
	if r.WorkTasks != 1 {
		t.Errorf("WorkTasks = %d, want 1", r.WorkTasks)
	}
	if r.NonWorkTasks != 2 {
		t.Errorf("NonWorkTasks = %d, want 2", r.NonWorkTasks)
	}
	if r.TaskTypeCount["code"] != 2 || r.TaskTypeCount["doc"] != 1 {
		t.Errorf("TaskTypeCount = %v", r.TaskTypeCount)
	}
	if r.CodeDirCount["后端"] != 1 || r.CodeDirCount["前端"] != 1 {
		t.Errorf("CodeDirCount = %v", r.CodeDirCount)
	}
	if r.ToolCount["Edit"] != 2 || r.ToolCount["Bash"] != 1 {
		t.Errorf("ToolCount = %v", r.ToolCount)
	}
	if len(r.NonWorkExample) != 2 {
		t.Errorf("NonWorkExample = %v, want 2 samples", r.NonWorkExample)
	}
}

func TestAggregate_NonWorkExampleCap(t *testing.T) {
	cfg := DefaultConfig()
	noon := time.Date(2026, 7, 8, 12, 0, 0, 0, time.Local)
	var recs []Record
	for i := 0; i < 25; i++ {
		recs = append(recs, mkRec(1, noon, "other", 0, "", "个人原因"))
	}
	r := Aggregate(recs, noon, cfg)
	if len(r.NonWorkExample) != maxNonWorkExamples {
		t.Errorf("NonWorkExample len = %d, want cap %d", len(r.NonWorkExample), maxNonWorkExamples)
	}
}

func TestOffHours(t *testing.T) {
	cfg := OffHoursCfg{StartHour: 22, EndHour: 8, WeekendOff: true}
	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"weekday 23:00 off", time.Date(2026, 7, 8, 23, 0, 0, 0, time.Local), true},   // Wed night
		{"weekday 03:00 off", time.Date(2026, 7, 8, 3, 0, 0, 0, time.Local), true},    // Wed early
		{"weekday 12:00 on", time.Date(2026, 7, 8, 12, 0, 0, 0, time.Local), false},   // Wed noon
		{"weekday 08:00 on (exclusive end)", time.Date(2026, 7, 8, 8, 0, 0, 0, time.Local), false},
		{"weekday 22:00 off (inclusive start)", time.Date(2026, 7, 8, 22, 0, 0, 0, time.Local), true},
		{"saturday noon off", time.Date(2026, 7, 11, 12, 0, 0, 0, time.Local), true}, // Saturday
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := offHours(tc.t, cfg); got != tc.want {
				t.Errorf("offHours(%v) = %v, want %v", tc.t, got, tc.want)
			}
		})
	}
}

func TestScoreAndNeedsReview(t *testing.T) {
	w := ScoreWeights{NonWork: 0.6, OffHours: 0.15, Volume: 0.25, BaselineTasks: 60, Threshold: 0.5}

	// Empty → 0.
	if s := Score(Rollup{LogicalTasks: 0}, w); s != 0 {
		t.Errorf("Score(empty) = %v, want 0", s)
	}

	// All non-work, at baseline volume → nonWork term dominates: 0.6*1 = 0.6 ≥ 0.5.
	high := Rollup{LogicalTasks: 60, NonWorkTasks: 60}
	if s := Score(high, w); s < 0.5 {
		t.Errorf("Score(all non-work) = %v, want >= 0.5", s)
	}
	if !NeedsReview(high, w) {
		t.Errorf("NeedsReview(all non-work) = false, want true")
	}

	// All work-related → nonWork term 0; well below threshold.
	low := Rollup{LogicalTasks: 60, WorkTasks: 60}
	if NeedsReview(low, w) {
		t.Errorf("NeedsReview(all work) = true, want false")
	}

	// Volume clamps to 1 even far above baseline.
	huge := Rollup{LogicalTasks: 10000, NonWorkTasks: 0}
	if s := Score(huge, w); s > 0.25+1e-9 {
		t.Errorf("Score(volume only) = %v, want <= 0.25 (volume weight)", s)
	}
}
