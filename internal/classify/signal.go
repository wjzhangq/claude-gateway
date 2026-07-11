package classify

import (
	"encoding/json"
	"path"
	"sort"
	"strings"
)

const intentMaxRunes = 300

// RequestRole judges whether the LAST user message is a real instruction or a
// tool-result feed-back. Claude Code sends tool outputs back as a user message
// whose content is entirely tool_result blocks; those are continuation rounds and
// must not be counted as separate logical tasks. A subagent turn is likewise
// folded into tool_continuation (spec decision, FR-006/FR-009).
func RequestRole(req Request) Role {
	last := lastUserMessage(req)
	if last == nil {
		// No user turn at all — treat as continuation (nothing to attribute).
		return RoleToolContinuation
	}
	hasText := false
	hasToolResult := false
	for _, b := range last.Blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				hasText = true
			}
		case "tool_result":
			hasToolResult = true
		}
	}
	// Pure tool_result feed-back (no fresh human text) ⇒ continuation.
	if hasToolResult && !hasText {
		return RoleToolContinuation
	}
	return RoleUserInitiated
}

// Extract scans the messages and distills a compressed Signal. It deliberately
// drops the system prompt, tool schemas, historical assistant replies, and file
// bodies — only intent text, touched file basenames, command verbs, and tool
// names survive. cfg is currently unused but kept so future rule tables can
// influence extraction without a signature change.
func Extract(req Request, _ Config) Signal {
	var sig Signal
	files := map[string]bool{}
	tools := map[string]bool{}
	cmds := map[string]bool{}

	// Intent: the last user text block.
	sig.Intent = truncate(lastUserText(req), intentMaxRunes)

	for i := range req.Messages {
		m := &req.Messages[i]
		for _, b := range m.Blocks {
			if b.Type != "tool_use" {
				continue
			}
			if b.Name != "" {
				tools[b.Name] = true
			}
			args := parseToolInput(b.Input)
			// File-bearing tools: collect basename.
			for _, fp := range []string{args.FilePath, args.Path, args.NotebookPath} {
				if fp == "" {
					continue
				}
				files[path.Base(fp)] = true
			}
			// Bash: first verb of the command.
			if b.Name == "Bash" && args.Command != "" {
				if v := firstVerb(args.Command); v != "" {
					cmds[v] = true
				}
			}
		}
	}

	sig.Files = sortedKeys(files)
	sig.Tools = sortedKeys(tools)
	sig.Cmds = sortedKeys(cmds)
	return sig
}

// toolArgs is the subset of tool_use.input fields that carry file/command hints.
type toolArgs struct {
	FilePath     string `json:"file_path"`
	Path         string `json:"path"`
	NotebookPath string `json:"notebook_path"`
	Command      string `json:"command"`
}

func parseToolInput(raw json.RawMessage) toolArgs {
	var a toolArgs
	if len(raw) == 0 {
		return a
	}
	_ = json.Unmarshal(raw, &a) // best-effort; unknown shapes yield zero values
	return a
}

// lastUserMessage returns a pointer to the last message with role "user", or nil.
func lastUserMessage(req Request) *Message {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return &req.Messages[i]
		}
	}
	return nil
}

// lastUserText returns the concatenated text of the last user message's text
// blocks (usually just one).
func lastUserText(req Request) string {
	m := lastUserMessage(req)
	if m == nil {
		return ""
	}
	var parts []string
	for _, b := range m.Blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// truncate limits s to at most n runes (not bytes), so multibyte text is not cut
// mid-character.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// firstVerb returns the first whitespace-delimited token of a command, stripped
// of any leading path (so "/usr/bin/go" → "go"). Empty for a blank command.
func firstVerb(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	return path.Base(fields[0])
}

// sortedKeys returns the map keys sorted; nil for an empty map so JSON omits it
// or renders an empty array consistently.
func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
