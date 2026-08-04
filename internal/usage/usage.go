// Package usage parses token usage from upstream responses. It is shared by
// the backend, public and AWS proxy channels so all channels count tokens
// (and money) the same way.
package usage

import "encoding/json"

// ParseTokens extracts token counts from a response body or a single SSE
// data payload. Handles:
//   - OpenAI:    usage.prompt_tokens / completion_tokens
//   - Anthropic: usage.input_tokens / output_tokens, including the nested
//     message.usage of message_start events, plus
//     cache_creation_input_tokens / cache_read_input_tokens.
//
// Fields absent from the payload come back as 0. Within an SSE stream 0
// means "not reported in this event", not "zero tokens" — callers must merge
// events field-by-field (see Accumulator) rather than overwrite wholesale:
// Anthropic's message_delta reports only output_tokens and would clobber the
// input_tokens seen in message_start.
func ParseTokens(data []byte) (input, output, cacheRead, cacheWrite int) {
	var r struct {
		Usage struct {
			// OpenAI format
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			// Anthropic format
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
		// message_start nests usage under "message"
		Message struct {
			Usage struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return
	}
	input = r.Usage.PromptTokens + r.Usage.InputTokens + r.Message.Usage.InputTokens
	output = r.Usage.CompletionTokens + r.Usage.OutputTokens + r.Message.Usage.OutputTokens
	cacheRead = r.Usage.CacheReadInputTokens + r.Message.Usage.CacheReadInputTokens
	cacheWrite = r.Usage.CacheCreationInputTokens + r.Message.Usage.CacheCreationInputTokens
	return
}

// Accumulator merges per-event token reports across an SSE stream
// field-by-field: a non-zero field updates the stored value, a zero field
// leaves it untouched. SawUsage records whether ANY usage data was seen;
// callers should only fall back to heuristic estimation when it is false
// (a legitimate cache-hit response can report input_tokens=0).
type Accumulator struct {
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int
	SawUsage   bool
}

// Add merges one parsed payload (from ParseTokens) into the accumulator.
func (a *Accumulator) Add(input, output, cacheRead, cacheWrite int) {
	if input > 0 || output > 0 || cacheRead > 0 || cacheWrite > 0 {
		a.SawUsage = true
	}
	if input > 0 {
		a.Input = input
	}
	if output > 0 {
		a.Output = output
	}
	if cacheRead > 0 {
		a.CacheRead = cacheRead
	}
	if cacheWrite > 0 {
		a.CacheWrite = cacheWrite
	}
}
