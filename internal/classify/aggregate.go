package classify

import "time"

// maxNonWorkExamples caps how many non-work reason strings a Rollup samples.
const maxNonWorkExamples = 10

// Aggregate folds a user's already-tagged records (one time window) into a Rollup.
// It counts physical rows vs logical tasks, work/non-work, and samples
// non-work reasons. Only records with a non-empty task_type count as logical
// tasks; work/non-work counts only consider work_related ∈ {0,1} (‑1 = undetermined
// is ignored, invariant D).
func Aggregate(recs []Record, windowStart time.Time, cfg Config) Rollup {
	r := Rollup{
		UserID:        userIDOf(recs),
		WindowStart:   windowStart,
		PhysicalCount: len(recs),
		TaskTypeCount: map[string]int{},
		CodeDirCount:  map[string]int{},
		ToolCount:     map[string]int{},
	}
	for i := range recs {
		rec := &recs[i]
		if rec.TaskType == "" {
			continue // not a logical task (unanalyzed / continuation)
		}
		r.LogicalTasks++
		r.TaskTypeCount[rec.TaskType]++
		if rec.CodeDirection != "" {
			r.CodeDirCount[rec.CodeDirection]++
		}
		for _, t := range rec.Tools {
			r.ToolCount[t]++
		}
		switch rec.WorkRelated {
		case 1:
			r.WorkTasks++
		case 0:
			r.NonWorkTasks++
			if rec.WorkReason != "" && len(r.NonWorkExample) < maxNonWorkExamples {
				r.NonWorkExample = append(r.NonWorkExample, rec.WorkReason)
			}
		}
	}
	r.AbuseScore = Score(r, cfg.Score)
	return r
}

// userIDOf returns the UserID of the first record, or 0 for an empty slice.
// Callers group by user before aggregating, so all records share one UserID.
func userIDOf(recs []Record) int64 {
	if len(recs) == 0 {
		return 0
	}
	return recs[0].UserID
}

// Score computes the 0..1 abuse score from a Rollup:
//
//	nonWork  = NonWorkTasks / LogicalTasks
//	vol      = clamp((LogicalTasks - Baseline) / Baseline, 0, 1)   // only above baseline
//	score    = clamp(w.NonWork*nonWork + w.Volume*vol, 0, 1)
//
// LogicalTasks == 0 yields 0 (nothing to judge).
func Score(r Rollup, w ScoreWeights) float64 {
	if r.LogicalTasks == 0 {
		return 0
	}
	lt := float64(r.LogicalTasks)
	nonWork := float64(r.NonWorkTasks) / lt
	vol := 0.0
	if w.BaselineTasks > 0 {
		vol = clamp((lt-float64(w.BaselineTasks))/float64(w.BaselineTasks), 0, 1)
	}
	return clamp(w.NonWork*nonWork+w.Volume*vol, 0, 1)
}

// NeedsReview reports whether a Rollup's score reaches the review threshold. This
// only flags for human review — the system never auto-punishes (FR-021).
func NeedsReview(r Rollup, w ScoreWeights) bool {
	return Score(r, w) >= w.Threshold
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
