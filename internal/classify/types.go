// Package classify turns a Claude Code request into an offline "what was this
// person doing" verdict. It is deliberately dependency-free and pure-function
// heavy so both the proxy (zero-cost signal extraction on the response path) and
// the cmd/check --analyze batch job (rules → Haiku fallback → aggregation) can
// share it. Nothing here persists raw messages: only the compressed Signal.
package classify

import "time"

// Role tags whether a request is a fresh human instruction or a continuation of
// an in-flight agent turn. Only user_initiated requests count as one logical task
// and are enqueued for analysis; the others inherit their parent turn's verdict.
type Role string

const (
	RoleUserInitiated    Role = "user_initiated"    // a real human instruction — one logical task
	RoleToolContinuation Role = "tool_continuation" // a tool_result feed-back round — not counted separately
	RoleSubagent         Role = "subagent"          // a subagent round — folded into tool_continuation (spec decision)
)

// Signal is the compressed, privacy-safe view of a request. It is the ONLY thing
// persisted (as pending_analysis.signal JSON) and the ONLY thing sent to Haiku.
// It never contains the system prompt, tool schemas, prior assistant replies, or
// file bodies — just enough to classify (FR-004, FR-011).
type Signal struct {
	Intent string   `json:"intent"`         // last user text, truncated to 300 runes
	Files  []string `json:"files"`          // basenames touched via tool_use, deduped + sorted
	Cmds   []string `json:"cmds,omitempty"` // first verb of each Bash command, deduped
	Tools  []string `json:"tools"`          // tool_use names, deduped + sorted
}

// Result is the classification verdict. Fields flow rules → Haiku (only the ones
// rules leave empty). The落点 (persistence target) for each field is described in
// data-model §2.2.
type Result struct {
	Role          Role     `json:"request_role"`
	ToolUsed      []string `json:"tool_used"`
	TaskType      string   `json:"task_type"`      // code | doc | other | "" (defer to Haiku)
	WorkRelated   *bool    `json:"work_related"`   // nil = defer to Haiku
	WorkReason    string   `json:"work_reason"`    // → usage_logs.error_reason (work: segment)
	CodeDirection string   `json:"code_direction"` // only for code
	DocActivity   string   `json:"doc_activity"`   // only for doc → error_reason (doc: segment)
	NeedHaiku     bool     `json:"-"`              // internal: rules could not fully decide
	FromHaiku     bool     `json:"from_haiku"`     // runtime: Haiku filled some field (for logging)
	HaikuInTok    int      `json:"-"`              // runtime: Haiku call input tokens (0 if no call)
	HaikuOutTok   int      `json:"-"`              // runtime: Haiku call output tokens (0 if no call)
}

// Record is one already-tagged usage_logs row, read back for aggregation.
type Record struct {
	UserID        int64
	CreatedAt     time.Time
	TaskType      string   // code | doc | other | "" (unanalyzed)
	WorkRelated   int      // 1 = yes, 0 = no, -1 = undetermined
	CodeDirection string   //
	Tools         []string // decoded from the pending signal at emit time (best-effort)
	WorkReason    string   // the work: segment of error_reason, for non-work sampling
}

// Rollup is a per-person portrait over a time window, computed at read time
// (never materialized). See data-model §2.4.
type Rollup struct {
	UserID         int64          `json:"user_id"`
	WindowStart    time.Time      `json:"window_start"`
	PhysicalCount  int            `json:"physical_count"`  // usage_logs rows in the window
	LogicalTasks   int            `json:"logical_tasks"`   // rows with a non-empty task_type
	TaskTypeCount  map[string]int `json:"task_type_count"` //
	CodeDirCount   map[string]int `json:"code_dir_count"`  //
	ToolCount      map[string]int `json:"tool_count"`      //
	WorkTasks      int            `json:"work_tasks"`      // work_related = 1
	NonWorkTasks   int            `json:"non_work_tasks"`  // work_related = 0
	NonWorkExample []string       `json:"non_work_examples"`
	AbuseScore     float64        `json:"abuse_score"`
}

// ScoreWeights parameterizes the abuse score. Mirrors config.ScoreConfig so the
// classify package stays free of a config import.
type ScoreWeights struct {
	NonWork       float64
	Volume        float64
	BaselineTasks int
	Threshold     float64
}

// Config bundles everything the pure classifier needs: the suffix → code-direction
// map, the set of documentation suffixes, plus the scoring parameters used by
// aggregation.
type Config struct {
	DirBySuffix map[string]string // file suffix (".go") → code direction ("后端")
	DocSuffixes map[string]bool   // suffixes that mark a documentation task
	Score       ScoreWeights      //
}
