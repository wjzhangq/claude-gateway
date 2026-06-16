// Package tokenest provides a lightweight, character-class-based token
// estimator used as a FALLBACK when an upstream provider does not return a
// usage object (or returns zeros). Real upstream usage always takes priority;
// these estimates only fill in when the real value is missing, so billing and
// quota tracking degrade gracefully instead of silently dropping to zero.
//
// The estimator classifies each rune as a digit, a CJK character, or "other",
// and applies empirical per-class coefficients. It is intentionally cheap:
// Counts.Add is O(len) with no allocation, so streaming callers can accumulate
// one delta at a time and finalize in O(1).
package tokenest

import (
	"encoding/json"
	"strings"
	"unicode"
)

// Coef holds empirical character-class → token conversion factors plus a fixed
// per-call formatting overhead.
type Coef struct {
	CJK, Digit, Other, Overhead float64
}

// Default is the hardcoded empirical coefficient set (cl100k / Claude scale):
// CJK ~1.1 tokens/char, digits ~1/3, other ~1/3.5, plus a small fixed overhead.
var Default = Coef{CJK: 1.1, Digit: 1.0 / 3, Other: 1.0 / 3.5, Overhead: 3}

// Counts is a running tally of runes by character class.
type Counts struct {
	CJK, Digit, Other int
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

// Add tallies the runes of s into c. O(len(s)), no allocation. Streaming
// callers feed each delta chunk separately; the tally accumulates.
func (c *Counts) Add(s string) {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			c.Digit++
		case isCJK(r):
			c.CJK++
		default:
			c.Other++
		}
	}
}

// Estimate converts the tally into an estimated token count, rounded to the
// nearest integer with a floor of 0.
func (c Counts) Estimate(k Coef) int {
	v := float64(c.CJK)*k.CJK + float64(c.Digit)*k.Digit +
		float64(c.Other)*k.Other + k.Overhead
	if v < 0 {
		v = 0
	}
	return int(v + 0.5)
}

// EstimateString is a one-shot estimate for a complete string (used for the
// request body, which is known up front).
func EstimateString(s string, k Coef) int {
	var c Counts
	c.Add(s)
	return c.Estimate(k)
}

// contentPart matches a structured content element. Both OpenAI and Anthropic
// use {type, text} for textual parts; non-text parts (images, tool_use) have an
// empty Text and are skipped — correct for a text-only estimate.
type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type reqMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type chatRequest struct {
	System   json.RawMessage `json:"system"` // Anthropic only; string or []part
	Messages []reqMessage    `json:"messages"`
}

// ExtractRequestText pulls all human-readable text from an OpenAI
// (/v1/chat/completions) or Anthropic (/v1/messages) request body. Returns ""
// on parse failure, which the caller treats as 0 tokens. Unknown fields are
// ignored by encoding/json.
func ExtractRequestText(body []byte) string {
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	var b strings.Builder
	appendContent(&b, req.System)
	for i := range req.Messages {
		appendContent(&b, req.Messages[i].Content)
	}
	return b.String()
}

// appendContent decodes a content field that may be a JSON string or a JSON
// array of {type,text} parts, appending any text to b.
func appendContent(b *strings.Builder, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var s string // fast path: string content
	if json.Unmarshal(raw, &s) == nil {
		if s != "" {
			b.WriteString(s)
			b.WriteByte('\n')
		}
		return
	}
	var parts []contentPart // array-of-parts content
	if json.Unmarshal(raw, &parts) == nil {
		for i := range parts {
			if parts[i].Text != "" {
				b.WriteString(parts[i].Text)
				b.WriteByte('\n')
			}
		}
	}
}

// streamDelta matches a single streaming SSE data payload. OpenAI nests the
// delta under choices[].delta.content; Anthropic puts it at the top level under
// a content_block_delta event. The two live on distinct JSON paths, so one
// struct covers both without collision.
type streamDelta struct {
	Choices []struct { // OpenAI streaming
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Type  string `json:"type"` // Anthropic streaming event type
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

// ExtractDeltaText returns the incremental output text from one SSE data
// payload, or "" if it carries no text delta (role-only chunks, ping,
// message_start/stop, content_block_start, etc.).
func ExtractDeltaText(payload []byte) string {
	var d streamDelta
	if err := json.Unmarshal(payload, &d); err != nil {
		return ""
	}
	if d.Type == "content_block_delta" && d.Delta.Type == "text_delta" && d.Delta.Text != "" {
		return d.Delta.Text
	}
	if len(d.Choices) > 0 && d.Choices[0].Delta.Content != "" {
		return d.Choices[0].Delta.Content
	}
	return ""
}

// bufResponse matches a non-streaming response body. OpenAI returns
// choices[].message.content (string); Anthropic returns content[].text (array).
type bufResponse struct {
	Choices []struct { // OpenAI
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Content []struct { // Anthropic
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// ExtractResponseText pulls the assistant's output text from a non-streaming
// OpenAI or Anthropic response body. Returns "" on parse failure. A null
// message.content (e.g. tool-call responses) unmarshals to "" and is skipped.
func ExtractResponseText(body []byte) string {
	var r bufResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return ""
	}
	var b strings.Builder
	for i := range r.Choices {
		if c := r.Choices[i].Message.Content; c != "" {
			b.WriteString(c)
			b.WriteByte('\n')
		}
	}
	for i := range r.Content {
		if r.Content[i].Text != "" {
			b.WriteString(r.Content[i].Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
