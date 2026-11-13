package usage

import (
	"encoding/json"
	"testing"
)

func TestInjectStreamOptions(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]interface{}
		wantKey string
	}{
		{
			name:    "streaming request without stream_options",
			input:   `{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			wantKey: "stream_options",
		},
		{
			name:    "non-streaming request",
			input:   `{"model":"gpt-4","stream":false,"messages":[]}`,
			wantKey: "", // should not inject
		},
		{
			name:    "already has stream_options",
			input:   `{"model":"gpt-4","stream":true,"stream_options":{"include_usage":false},"messages":[]}`,
			wantKey: "preserved", // should not overwrite
		},
		{
			name:    "invalid JSON",
			input:   `{invalid`,
			wantKey: "", // should return unchanged
		},
		{
			name:    "stream field missing",
			input:   `{"model":"gpt-4","messages":[]}`,
			wantKey: "", // should not inject
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InjectStreamOptions([]byte(tt.input))

			if tt.wantKey == "" {
				// Should return unchanged
				if string(got) != tt.input {
					t.Errorf("expected unchanged body, got: %s", got)
				}
				return
			}

			var m map[string]interface{}
			if err := json.Unmarshal(got, &m); err != nil {
				t.Fatalf("result is not valid JSON: %v", err)
			}

			if tt.wantKey == "preserved" {
				// Check that original stream_options value is kept
				opts, ok := m["stream_options"].(map[string]interface{})
				if !ok {
					t.Fatal("stream_options missing or wrong type")
				}
				if opts["include_usage"] != false {
					t.Errorf("stream_options.include_usage should be false (preserved), got: %v", opts)
				}
				return
			}

			// Normal case: check that stream_options was added
			opts, ok := m["stream_options"].(map[string]interface{})
			if !ok {
				t.Fatalf("stream_options not injected or wrong type, got: %v", m)
			}
			if opts["include_usage"] != true {
				t.Errorf("include_usage = %v, want true", opts["include_usage"])
			}
		})
	}
}
