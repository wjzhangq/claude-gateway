package classify

import (
	"fmt"
	"path"
	"strings"
)

// Classify applies only cheap, deterministic rules and leaves whatever it cannot
// decide empty, setting NeedHaiku so the caller knows a fallback is warranted:
//
//   - code_direction: majority vote of touched file suffixes; a tie leaves it empty.
//   - task_type: code if any code suffix voted; doc if only doc suffixes; else "".
//   - work_related: true if the request hit an internal repo (with a reason);
//     otherwise left nil (undetermined) for Haiku.
//
// The verdict's Role and ToolUsed are copied straight from the inputs.
func Classify(req Request, sig Signal, cfg Config) Result {
	return classifyFromSignal(sig, RequestRole(req), cfg)
}

// classifyFromSignal is the signal-only core shared by Classify (proxy side, has
// the raw Request) and AnalyzeSignal (analyzer side, only has the persisted
// Signal). Role is passed in because only Classify can recompute it from the raw
// request; the analyzer carries the role determined at collection time.
func classifyFromSignal(sig Signal, role Role, cfg Config) Result {
	res := Result{
		Role:     role,
		ToolUsed: sig.Tools,
	}

	// Suffix voting over the touched files.
	dirVotes := map[string]int{}
	codeVotes, docVotes := 0, 0
	for _, f := range sig.Files {
		suffix := strings.ToLower(path.Ext(f))
		if suffix == "" {
			continue
		}
		if dir, ok := cfg.DirBySuffix[suffix]; ok {
			dirVotes[dir]++
			codeVotes++
			continue
		}
		if cfg.DocSuffixes[suffix] {
			docVotes++
		}
	}

	switch {
	case codeVotes > 0:
		res.TaskType = "code"
		res.CodeDirection = topDirection(dirVotes) // "" on a tie — defer that part
	case docVotes > 0:
		res.TaskType = "doc"
	default:
		res.TaskType = "" // no file evidence — defer to Haiku
	}

	// Internal-repo hit ⇒ work-related, with a human-readable reason.
	if sig.Repo != "" {
		t := true
		res.WorkRelated = &t
		res.WorkReason = fmt.Sprintf("命中内部仓库 %s", sig.Repo)
	}

	res.NeedHaiku = needHaiku(res)
	return res
}

// needHaiku reports whether rules left enough undecided to justify a Haiku call.
// Continuation / subagent rounds never need Haiku (the caller does not even reach
// this for them). A verdict needs Haiku when task_type is empty, or work_related
// is still undetermined, or a code task has no direction.
func needHaiku(res Result) bool {
	if res.TaskType == "" {
		return true
	}
	if res.WorkRelated == nil {
		return true
	}
	if res.TaskType == "code" && res.CodeDirection == "" {
		return true
	}
	return false
}

// NeedHaiku is the exported predicate mirroring the internal rule, so tests and
// callers can query "would rules alone leave this incomplete?" without a Fill.
func NeedHaiku(res Result) bool { return needHaiku(res) }

// topDirection returns the single direction with the most votes, or "" when the
// map is empty or the top two are tied (an ambiguous mix left for Haiku).
func topDirection(votes map[string]int) string {
	best, bestN, tie := "", 0, false
	for dir, n := range votes {
		switch {
		case n > bestN:
			best, bestN, tie = dir, n, false
		case n == bestN:
			tie = true
		}
	}
	if tie {
		return ""
	}
	return best
}
