package publicproxy

import (
	"testing"

	"github.com/wjzhangq/claude-gateway/internal/tokenest"
	"github.com/wjzhangq/claude-gateway/internal/usage"
)

func TestConsumeSSELines(t *testing.T) {
	cases := []struct {
		name                            string
		input                           []byte
		wantRemainder                   string
		wantIn, wantOut, wantCR, wantCW int
		wantSawUsage                    bool
		wantOutCountsCJK                int // tokenest.Counts.CJK as proxy
	}{
		{
			name: "anthropic message_start with cache",
			input: []byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1,"cache_read_input_tokens":2000,"cache_creation_input_tokens":300}}}
data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"你好"}}
`),
			wantRemainder: "",
			wantIn:        12, wantOut: 1, wantCR: 2000, wantCW: 300,
			wantSawUsage:     true,
			wantOutCountsCJK: 2, // "你好"
		},
		{
			name: "anthropic message_delta overwrites output",
			input: []byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":50}}}
data: {"type":"message_delta","usage":{"output_tokens":200}}
`),
			wantRemainder: "",
			wantIn:        50, wantOut: 200, wantCR: 0, wantCW: 0,
			wantSawUsage: true,
		},
		{
			name:          "partial line kept as remainder",
			input:         []byte("data: {\"usage\":{\"input_tokens\":10}}\ndata: partial"),
			wantRemainder: "data: partial",
			wantIn:        10, wantOut: 0, wantCR: 0, wantCW: 0,
			wantSawUsage:     true,
			wantOutCountsCJK: 0,
		},
		{
			name:          "no usage events",
			input:         []byte("data: {\"type\":\"ping\"}\ndata: [DONE]\n"),
			wantRemainder: "",
			wantIn:        0, wantOut: 0, wantCR: 0, wantCW: 0,
			wantSawUsage:     false,
			wantOutCountsCJK: 0,
		},
		{
			name:          "openai format",
			input:         []byte(`data: {"usage":{"prompt_tokens":77,"completion_tokens":33}}` + "\n"),
			wantRemainder: "",
			wantIn:        77, wantOut: 33, wantCR: 0, wantCW: 0,
			wantSawUsage:     true,
			wantOutCountsCJK: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var acc usage.Accumulator
			var outCounts tokenest.Counts
			remainder, acc := consumeSSELines(tc.input, acc, &outCounts)

			if string(remainder) != tc.wantRemainder {
				t.Errorf("remainder: got %q, want %q", remainder, tc.wantRemainder)
			}
			if acc.Input != tc.wantIn {
				t.Errorf("Input: got %d, want %d", acc.Input, tc.wantIn)
			}
			if acc.Output != tc.wantOut {
				t.Errorf("Output: got %d, want %d", acc.Output, tc.wantOut)
			}
			if acc.CacheRead != tc.wantCR {
				t.Errorf("CacheRead: got %d, want %d", acc.CacheRead, tc.wantCR)
			}
			if acc.CacheWrite != tc.wantCW {
				t.Errorf("CacheWrite: got %d, want %d", acc.CacheWrite, tc.wantCW)
			}
			if acc.SawUsage != tc.wantSawUsage {
				t.Errorf("SawUsage: got %v, want %v", acc.SawUsage, tc.wantSawUsage)
			}
			if outCounts.CJK != tc.wantOutCountsCJK {
				t.Errorf("outCounts.CJK: got %d, want %d", outCounts.CJK, tc.wantOutCountsCJK)
			}
		})
	}
}

// TestConsumeSSELinesMultipleCallsAccumulate verifies value-passing semantics:
// the caller must capture returned acc on every call.
func TestConsumeSSELinesMultipleCallsAccumulate(t *testing.T) {
	chunk1 := []byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n")
	chunk2 := []byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":20}}\n")

	var acc usage.Accumulator
	var outCounts tokenest.Counts

	_, acc = consumeSSELines(chunk1, acc, &outCounts)
	if acc.Input != 10 || acc.Output != 0 {
		t.Fatalf("after chunk1: got (%d,%d), want (10,0)", acc.Input, acc.Output)
	}

	_, acc = consumeSSELines(chunk2, acc, &outCounts)
	if acc.Input != 10 || acc.Output != 20 {
		t.Errorf("after chunk2: got (%d,%d), want (10,20) — accumulation failed", acc.Input, acc.Output)
	}
}
