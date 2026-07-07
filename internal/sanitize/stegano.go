// Package sanitize normalizes request bodies to strip per-session steganographic
// markers that some clients embed in system prompts.
//
// The known marker is the "Today's date is YYYY-MM-DD" line, where the apostrophe
// in "Today's" is swapped for one of several look-alike Unicode glyphs and the date
// separators may be varied. Each variant encodes a fingerprint. Normalizing every
// request to a single canonical byte sequence removes that signal.
//
// Design notes:
//   - Fast path: bytes.Contains gates the regex so the ~all non-matching traffic
//     never touches the regex engine. bytes.Contains uses SIMD-optimized assembly.
//   - The regex matches on the raw byte stream and accepts BOTH literal UTF-8
//     apostrophe glyphs AND their JSON-escaped \uXXXX forms, so no JSON parse is
//     required.
//   - Callers rebuild the upstream request from the returned slice, so Content-Length
//     is recomputed automatically — no header surgery here.
package sanitize

import (
	"bytes"
	"regexp"
)

// deSteganoRegex matches "today<apostrophe>s date is <date>" and captures the
// year, month, and day. It is case-insensitive on the word "today".
//
//   - \\?              : optional backslash — some clients JSON-escape the apostrophe as \'
//   - (?:'|’|ʼ|ʹ|\\u2019|\\u02bc|\\u02b9)
//     : standard ASCII apostrophe, three look-alike glyphs (U+2019 RIGHT SINGLE
//     QUOTATION MARK, U+02BC MODIFIER LETTER APOSTROPHE, U+02B9 MODIFIER LETTER
//     PRIME), or their JSON \uXXXX escaped forms as they appear in raw bytes.
//   - (\d{4})[/-](\d{2})[/-](\d{2}) : YYYY-MM-DD or YYYY/MM/DD.
//
// In a Go raw string literal, `\\u2019` is the six literal characters ’ —
// exactly what a JSON encoder emits for U+2019 — which is what we want to match.
var deSteganoRegex = regexp.MustCompile(`(?i)today\\?(?:'|’|ʼ|ʹ|\\u2019|\\u02bc|\\u02b9)s date is (\d{4})[/-](\d{2})[/-](\d{2})`)

// canonical is the single normalized form every matching request is rewritten to.
var canonical = []byte("Today's date is $1-$2-$3")

// fastPathToken is the cheap substring probe that gates the regex. "today" (with
// (?i) in the regex) covers both "today" and "Today".
var fastPathTokenLower = []byte("today")
var fastPathTokenUpper = []byte("Today")

// Body normalizes steganographic date markers in a request body. It returns the
// (possibly rewritten) body and a bool that is true only when a marker was found
// and cleaned. The original slice is returned unchanged when no marker is present
// (the common case), so it is safe and cheap to call on every request.
func Body(body []byte) (out []byte, cleaned bool) {
	// Fast path: skip the regex entirely unless the trigger word is present.
	if !bytes.Contains(body, fastPathTokenLower) && !bytes.Contains(body, fastPathTokenUpper) {
		return body, false
	}
	if !deSteganoRegex.Match(body) {
		return body, false
	}
	return deSteganoRegex.ReplaceAll(body, canonical), true
}
