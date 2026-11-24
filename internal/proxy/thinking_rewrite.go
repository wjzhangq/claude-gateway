package proxy

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Rewrite actions returned by RewriteThinking, used in the log line that
// measures how much legacy traffic the gateway is patching up.
const (
	thinkingActionAdaptive = "rewrote_to_adaptive"
	thinkingActionDeleted  = "deleted"
)

// RewriteThinking rewrites a request body's "thinking" parameter from the
// shapes older clients send into ones current models accept, and reports what
// it did so the caller can log it.
//
// Claude Opus 5.5, Sonnet 5.5 and Haiku 5.5 reject the pre-4.6 shapes with a
// 400: thinking.type "enabled" always carried budget_tokens, and budget_tokens
// is removed on those models; thinking.type "disabled" is itself a 400 on Opus
// 5.5 (at every effort level) and on Sonnet 5.5. A client pinned to an older
// SDK therefore fails every request. The mapping:
//
//   - "enabled" / "extended_thinking" → "adaptive", and budget_tokens is
//     dropped from the object. Dropping it is not optional: "adaptive" plus a
//     leftover budget_tokens is still a 400 on these models, so a rewrite that
//     kept it would not fix the error it exists to fix.
//   - "disabled" or an empty type → the whole thinking field is removed, which
//     lets the model apply its default (adaptive on all three). Thinking cannot
//     be turned off on Opus 5.5 at all; Sonnet 5.5 wants "between_tools", but
//     that is accepted only at effort high or below and must not carry any
//     other field, so synthesizing it from a legacy request risks trading one
//     400 for another. Removing the field always parses.
//   - any other unrecognized value → the field is removed, since an unknown
//     type is a 400.
//   - "adaptive" / "between_tools" → untouched; both are current values.
//
// The body is also returned untouched when it is not valid JSON, when there is
// no thinking field, or when thinking is not an object.
//
// Reads and writes go through gjson/sjson rather than unmarshalling the body
// into a map: the guard checks allocate nothing, the write touches only the
// spliced region, and every other byte — key order, number formatting, and the
// large integer IDs a float64 round-trip would silently round — is preserved.
// This mirrors usage.InjectStreamOptions.
//
// It returns the (possibly rewritten) body, the original thinking.type for the
// log line, and the action taken; action is empty when nothing changed, and in
// that case the returned body is the caller's own slice.
func RewriteThinking(body []byte) (out []byte, originalType, action string) {
	// gjson reads fields out of malformed input rather than failing — it will
	// report type="enabled" for a truncated `{"thinking":{"type":"enabled"` —
	// and sjson would then splice into the broken body and emit garbage.
	// Validate first so an invalid body is handed back untouched.
	if !gjson.ValidBytes(body) {
		return body, "", ""
	}

	thinking := gjson.GetBytes(body, "thinking")
	if !thinking.Exists() {
		return body, "", ""
	}

	// A non-object thinking value (a bare string, a number) is not a shape any
	// client version ever sent, so there is no legacy mapping for it. Leave it
	// for the upstream to reject rather than guessing at intent.
	if !thinking.IsObject() {
		return body, "", ""
	}

	originalType = thinking.Get("type").String()

	var newBody []byte
	var err error

	switch originalType {
	case "adaptive", "between_tools":
		// Current values; nothing to do.
		return body, originalType, ""

	case "enabled", "extended_thinking":
		newBody, err = sjson.SetBytes(body, "thinking.type", "adaptive")
		// budget_tokens rides along with "enabled" and is a 400 on every model
		// this rewrite targets, so it has to go too. Delete unconditionally:
		// sjson.DeleteBytes is a no-op when the path is absent.
		if err == nil {
			newBody, err = sjson.DeleteBytes(newBody, "thinking.budget_tokens")
		}
		action = thinkingActionAdaptive

	default:
		// "disabled", "", and anything unrecognized: drop the field.
		newBody, err = sjson.DeleteBytes(body, "thinking")
		action = thinkingActionDeleted
	}

	// A write failure must not corrupt the request. Hand back the original body
	// and report no action; the request goes upstream exactly as the client sent
	// it, which is the same outcome as having the rewrite switched off.
	if err != nil {
		return body, originalType, ""
	}

	return newBody, originalType, action
}

// thinkingRewriteApplies reports whether the rewrite should run for this model.
// An empty list or a "*" entry means every model; otherwise a model matches when
// its name contains one of the configured patterns, case-insensitively — the
// same substring convention model_replacements uses, so "claude-opus-5-5" in the
// config also covers a dated or vendor-prefixed variant of that id.
func thinkingRewriteApplies(patterns []string, model string) bool {
	if len(patterns) == 0 {
		return true
	}
	m := strings.ToLower(model)
	for _, p := range patterns {
		if p == "*" {
			return true
		}
		if p == "" {
			continue
		}
		if strings.Contains(m, strings.ToLower(p)) {
			return true
		}
	}
	return false
}
