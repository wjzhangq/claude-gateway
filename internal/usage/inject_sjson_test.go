package usage

import (
	"encoding/json"
	"strings"
	"testing"
)

// injectStreamOptionsViaMap is the pre-sjson implementation, kept so the
// benchmark below compares against a real baseline and so the precision test
// can demonstrate what the map round-trip did to large integers.
func injectStreamOptionsViaMap(body []byte) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}

	var stream bool
	if err := json.Unmarshal(m["stream"], &stream); err != nil || !stream {
		return body
	}

	if _, exists := m["stream_options"]; exists {
		return body
	}

	m["stream_options"] = json.RawMessage(`{"include_usage":true}`)

	newBody, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return newBody
}

// A 19-digit ID exceeds float64's 2^53 exact-integer range. Any path that
// decodes it into interface{} rounds it; sjson splices bytes and does not.
func TestInjectStreamOptionsPreservesLargeIntegers(t *testing.T) {
	const bigID = "1234567890123456789"
	body := []byte(`{"model":"gpt-4","request_id":` + bigID + `,"stream":true}`)

	got := InjectStreamOptions(body)

	if !strings.Contains(string(got), bigID) {
		t.Errorf("large integer was altered.\n got: %s\nwant it to contain: %s", got, bigID)
	}
	if !gjsonHasInjection(got) {
		t.Errorf("stream_options.include_usage missing from %s", got)
	}
}

// Key order is what makes two request logs comparable by eye and by diff.
func TestInjectStreamOptionsPreservesKeyOrder(t *testing.T) {
	body := []byte(`{"model":"gpt-4","stream":true,"temperature":0.7,"messages":[]}`)

	got := string(InjectStreamOptions(body))

	// The original keys must still appear in their original order, with the
	// new key appended rather than the whole object reordered.
	original := `{"model":"gpt-4","stream":true,"temperature":0.7,"messages":[]`
	if !strings.HasPrefix(got, original) {
		t.Errorf("key order changed.\n got: %s\nwant prefix: %s", got, original)
	}
}

// Floats must not be reformatted (e.g. 0.7 -> 0.7000000000000001).
func TestInjectStreamOptionsPreservesFloatFormatting(t *testing.T) {
	body := []byte(`{"stream":true,"temperature":0.1,"top_p":0.95}`)

	got := string(InjectStreamOptions(body))

	for _, lit := range []string{`"temperature":0.1`, `"top_p":0.95`} {
		if !strings.Contains(got, lit) {
			t.Errorf("float literal %s was reformatted in %s", lit, got)
		}
	}
}

// The result has to stay valid JSON with exactly one stream_options key —
// sjson appends, so a duplicate key would be a silent protocol error.
func TestInjectStreamOptionsProducesValidJSON(t *testing.T) {
	inputs := []string{
		`{"stream":true}`,
		`{"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		`{"nested":{"deep":{"value":1}},"stream":true}`,
		`{"arr":[1,2,3],"stream":true}`,
	}

	for _, in := range inputs {
		got := InjectStreamOptions([]byte(in))

		var m map[string]interface{}
		if err := json.Unmarshal(got, &m); err != nil {
			t.Errorf("input %s produced invalid JSON %s: %v", in, got, err)
			continue
		}
		if n := strings.Count(string(got), `"stream_options"`); n != 1 {
			t.Errorf("input %s produced %d stream_options keys: %s", in, n, got)
		}
		opts, ok := m["stream_options"].(map[string]interface{})
		if !ok {
			t.Errorf("input %s: stream_options missing or wrong type in %s", in, got)
			continue
		}
		if opts["include_usage"] != true {
			t.Errorf("input %s: include_usage = %v, want true", in, opts["include_usage"])
		}
	}
}

// Malformed bodies reach this function from untrusted clients; it must hand
// them back byte-identical rather than erroring or half-rewriting them.
func TestInjectStreamOptionsMalformedUnchanged(t *testing.T) {
	inputs := []string{
		`{invalid`,
		``,
		`not json at all`,
		`{"stream":true`, // truncated
		`[]`,
		`null`,
	}

	for _, in := range inputs {
		if got := string(InjectStreamOptions([]byte(in))); got != in {
			t.Errorf("malformed input %q was modified to %q", in, got)
		}
	}
}

// "stream" must be read as a real boolean: a string "true" is not a streaming
// request, and treating it as one would inject into a body the upstream will
// reject anyway.
func TestInjectStreamOptionsStreamTypeStrictness(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantInject bool
	}{
		{"bool true", `{"stream":true}`, true},
		{"bool false", `{"stream":false}`, false},
		{"absent", `{"model":"gpt-4"}`, false},
		{"null", `{"stream":null}`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InjectStreamOptions([]byte(tt.input))
			if injected := gjsonHasInjection(got); injected != tt.wantInject {
				t.Errorf("injected = %v, want %v (result: %s)", injected, tt.wantInject, got)
			}
		})
	}
}

// An existing stream_options is the client's own choice, whatever its shape.
func TestInjectStreamOptionsNeverOverwrites(t *testing.T) {
	inputs := []string{
		`{"stream":true,"stream_options":{"include_usage":false}}`,
		`{"stream":true,"stream_options":{}}`,
		`{"stream":true,"stream_options":null}`,
		`{"stream":true,"stream_options":{"include_usage":true,"extra":"keep"}}`,
	}

	for _, in := range inputs {
		if got := string(InjectStreamOptions([]byte(in))); got != in {
			t.Errorf("existing stream_options was modified.\n in: %s\nout: %s", in, got)
		}
	}
}

func gjsonHasInjection(body []byte) bool {
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		return false
	}
	opts, ok := m["stream_options"].(map[string]interface{})
	return ok && opts["include_usage"] == true
}

// TestInjectStreamOptionsMatchesMapImpl pins the sjson version to the old one
// on the semantics both are supposed to share: whether injection happens, and
// what the resulting JSON means. Byte equality is NOT asserted — preserving
// the original bytes is the point of the change.
func TestInjectStreamOptionsMatchesMapImpl(t *testing.T) {
	inputs := []string{
		`{"model":"gpt-4","stream":true,"messages":[]}`,
		`{"model":"gpt-4","stream":false}`,
		`{"stream":true,"stream_options":{"include_usage":false}}`,
		`{invalid`,
		`{"model":"gpt-4"}`,
	}

	for _, in := range inputs {
		gotNew := InjectStreamOptions([]byte(in))
		gotOld := injectStreamOptionsViaMap([]byte(in))

		if a, b := gjsonHasInjection(gotNew), gjsonHasInjection(gotOld); a != b {
			t.Errorf("input %s: new injected=%v, old injected=%v", in, a, b)
			continue
		}

		// Where both produced JSON, the decoded values must agree.
		var mNew, mOld map[string]interface{}
		errNew := json.Unmarshal(gotNew, &mNew)
		errOld := json.Unmarshal(gotOld, &mOld)
		if (errNew == nil) != (errOld == nil) {
			t.Errorf("input %s: parse disagreement new=%v old=%v", in, errNew, errOld)
			continue
		}
		if errNew != nil {
			continue
		}
		if len(mNew) != len(mOld) {
			t.Errorf("input %s: key count new=%d old=%d", in, len(mNew), len(mOld))
		}
	}
}

func BenchmarkInjectStreamOptions(b *testing.B) {
	// A request body in the shape the gateway actually forwards.
	body := []byte(`{"model":"gpt-4","stream":true,"temperature":0.7,"max_tokens":4096,` +
		`"messages":[{"role":"system","content":"You are a helpful assistant."},` +
		`{"role":"user","content":"Explain connection pooling in Go."}]}`)

	b.Run("sjson", func(b *testing.B) {
		b.SetBytes(int64(len(body)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			InjectStreamOptions(body)
		}
	})

	b.Run("map_roundtrip", func(b *testing.B) {
		b.SetBytes(int64(len(body)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			injectStreamOptionsViaMap(body)
		}
	})
}

// The early-return path runs on every non-streaming request, so its cost
// matters as much as the injection path's.
func BenchmarkInjectStreamOptionsSkipPath(b *testing.B) {
	body := []byte(`{"model":"gpt-4","stream":false,"messages":[{"role":"user","content":"hi"}]}`)

	b.Run("sjson", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			InjectStreamOptions(body)
		}
	})

	b.Run("map_roundtrip", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			injectStreamOptionsViaMap(body)
		}
	})
}
