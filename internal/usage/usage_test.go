package usage

import "testing"

func TestParseTokens(t *testing.T) {
	cases := []struct {
		name                          string
		payload                       string
		in, out, cacheRead, cacheWrite int
	}{
		{
			name:    "anthropic message_start (nested usage with cache)",
			payload: `{"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1,"cache_read_input_tokens":91000,"cache_creation_input_tokens":1200}}}`,
			in:      12, out: 1, cacheRead: 91000, cacheWrite: 1200,
		},
		{
			name:    "anthropic message_delta (output only)",
			payload: `{"type":"message_delta","usage":{"output_tokens":842}}`,
			in:      0, out: 842, cacheRead: 0, cacheWrite: 0,
		},
		{
			name:    "anthropic final body (top-level usage)",
			payload: `{"type":"message","usage":{"input_tokens":5000,"output_tokens":300,"cache_read_input_tokens":2000}}`,
			in:      5000, out: 300, cacheRead: 2000, cacheWrite: 0,
		},
		{
			name:    "openai body",
			payload: `{"usage":{"prompt_tokens":100,"completion_tokens":50}}`,
			in:      100, out: 50, cacheRead: 0, cacheWrite: 0,
		},
		{
			name:    "openai stream chunk with usage",
			payload: `{"choices":[{"delta":{}}],"usage":{"prompt_tokens":77,"completion_tokens":33}}`,
			in:      77, out: 33, cacheRead: 0, cacheWrite: 0,
		},
		{
			name:    "no usage (content delta)",
			payload: `{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`,
			in:      0, out: 0, cacheRead: 0, cacheWrite: 0,
		},
		{
			name:    "invalid json",
			payload: `{oops`,
			in:      0, out: 0, cacheRead: 0, cacheWrite: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, out, cr, cw := ParseTokens([]byte(tc.payload))
			if in != tc.in || out != tc.out || cr != tc.cacheRead || cw != tc.cacheWrite {
				t.Errorf("got (%d,%d,%d,%d), want (%d,%d,%d,%d)",
					in, out, cr, cw, tc.in, tc.out, tc.cacheRead, tc.cacheWrite)
			}
		})
	}
}

// TestAccumulatorAnthropicSequence replays a real Anthropic SSE stream:
// input_tokens (+cache) arrive once in message_start, message_delta then
// reports ONLY output_tokens. The accumulator must not clobber input.
func TestAccumulatorAnthropicSequence(t *testing.T) {
	events := []string{
		`{"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1,"cache_read_input_tokens":91000,"cache_creation_input_tokens":1200}}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":" world"}}`,
		`{"type":"message_delta","usage":{"output_tokens":842}}`,
	}
	var acc Accumulator
	for _, e := range events {
		acc.Add(ParseTokens([]byte(e)))
	}
	if !acc.SawUsage {
		t.Fatal("SawUsage should be true")
	}
	if acc.Input != 12 {
		t.Errorf("Input clobbered: got %d, want 12", acc.Input)
	}
	if acc.Output != 842 {
		t.Errorf("Output: got %d, want 842", acc.Output)
	}
	if acc.CacheRead != 91000 {
		t.Errorf("CacheRead: got %d, want 91000", acc.CacheRead)
	}
	if acc.CacheWrite != 1200 {
		t.Errorf("CacheWrite: got %d, want 1200", acc.CacheWrite)
	}
}

// TestAccumulatorNoUsage covers the estimation-fallback precondition: a
// stream with zero usage events must leave SawUsage=false so the caller
// estimates tokens heuristically.
func TestAccumulatorNoUsage(t *testing.T) {
	var acc Accumulator
	acc.Add(ParseTokens([]byte(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`)))
	if acc.SawUsage {
		t.Error("SawUsage should stay false when no event carries usage")
	}
}
