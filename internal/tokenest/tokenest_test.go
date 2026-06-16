package tokenest

import "testing"

func TestCountsAdd(t *testing.T) {
	var c Counts
	c.Add("ab12中あ")
	// a,b -> Other; 1,2 -> Digit; 中 (Han), あ (Hiragana) -> CJK
	if c.Other != 2 || c.Digit != 2 || c.CJK != 2 {
		t.Fatalf("classify: got Other=%d Digit=%d CJK=%d, want 2/2/2", c.Other, c.Digit, c.CJK)
	}
}

func TestEstimateStringMonotonic(t *testing.T) {
	short := EstimateString("hello", Default)
	long := EstimateString("hello world this is a longer string", Default)
	if long <= short {
		t.Fatalf("longer string should estimate more tokens: short=%d long=%d", short, long)
	}
	// CJK weighs more per char than ASCII other.
	cjk := EstimateString("中文中文中文中文中文", Default)
	ascii := EstimateString("abcdefghij", Default)
	if cjk <= ascii {
		t.Fatalf("10 CJK chars should exceed 10 ascii chars: cjk=%d ascii=%d", cjk, ascii)
	}
}

func TestEstimateFloor(t *testing.T) {
	// Empty string still yields the overhead, never negative.
	if got := EstimateString("", Default); got < 0 {
		t.Fatalf("estimate must not be negative, got %d", got)
	}
}

func TestExtractRequestTextOpenAI(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"Hello world"}]}`)
	got := ExtractRequestText(body)
	if !contains(got, "You are helpful") || !contains(got, "Hello world") {
		t.Fatalf("openai string content not extracted: %q", got)
	}
}

func TestExtractRequestTextOpenAIArrayContent(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"part one"},{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"part two"}]}]}`)
	got := ExtractRequestText(body)
	if !contains(got, "part one") || !contains(got, "part two") {
		t.Fatalf("openai array content not extracted: %q", got)
	}
}

func TestExtractRequestTextAnthropic(t *testing.T) {
	body := []byte(`{"model":"claude","system":"sys prompt","messages":[{"role":"user","content":[{"type":"text","text":"anthropic body"}]}]}`)
	got := ExtractRequestText(body)
	if !contains(got, "sys prompt") || !contains(got, "anthropic body") {
		t.Fatalf("anthropic content not extracted: %q", got)
	}
}

func TestExtractRequestTextAnthropicSystemArray(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"sys array"}],"messages":[{"role":"user","content":"hi"}]}`)
	got := ExtractRequestText(body)
	if !contains(got, "sys array") || !contains(got, "hi") {
		t.Fatalf("anthropic system array not extracted: %q", got)
	}
}

func TestExtractRequestTextGarbage(t *testing.T) {
	if got := ExtractRequestText([]byte("not json")); got != "" {
		t.Fatalf("garbage should yield empty, got %q", got)
	}
}

func TestExtractDeltaTextOpenAI(t *testing.T) {
	if got := ExtractDeltaText([]byte(`{"choices":[{"delta":{"content":"chunk"}}]}`)); got != "chunk" {
		t.Fatalf("openai delta: got %q want chunk", got)
	}
	// role-only first chunk -> empty
	if got := ExtractDeltaText([]byte(`{"choices":[{"delta":{"role":"assistant"}}]}`)); got != "" {
		t.Fatalf("openai role-only delta should be empty, got %q", got)
	}
}

func TestExtractDeltaTextAnthropic(t *testing.T) {
	if got := ExtractDeltaText([]byte(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"piece"}}`)); got != "piece" {
		t.Fatalf("anthropic delta: got %q want piece", got)
	}
	// non-text events -> empty
	for _, p := range []string{
		`{"type":"message_start","message":{}}`,
		`{"type":"ping"}`,
		`{"type":"content_block_start","content_block":{"type":"text"}}`,
	} {
		if got := ExtractDeltaText([]byte(p)); got != "" {
			t.Fatalf("non-text event %s should be empty, got %q", p, got)
		}
	}
}

func TestExtractResponseTextOpenAI(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"the answer"}}]}`)
	if got := ExtractResponseText(body); !contains(got, "the answer") {
		t.Fatalf("openai response not extracted: %q", got)
	}
}

func TestExtractResponseTextAnthropic(t *testing.T) {
	body := []byte(`{"content":[{"type":"text","text":"reply one"},{"type":"text","text":"reply two"}]}`)
	got := ExtractResponseText(body)
	if !contains(got, "reply one") || !contains(got, "reply two") {
		t.Fatalf("anthropic response not extracted: %q", got)
	}
}

func TestExtractResponseTextNullContent(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[]}}]}`)
	if got := ExtractResponseText(body); got != "" {
		t.Fatalf("null content should yield empty, got %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
