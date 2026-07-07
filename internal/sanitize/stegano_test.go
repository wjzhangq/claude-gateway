package sanitize

import (
	"bytes"
	"testing"
)

// want is the single canonical form every marker must collapse to.
var want = []byte("Today's date is 2026-07-03")

func TestBody_NoMarker(t *testing.T) {
	inputs := []string{
		`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hello"}]}`,
		`{}`,
		`{"system":"You are a helpful assistant."}`,
	}
	for _, in := range inputs {
		b := []byte(in)
		out, cleaned := Body(b)
		if !bytes.Equal(out, b) {
			t.Errorf("expected unchanged body for %q, got %q", in, out)
		}
		if cleaned {
			t.Errorf("expected cleaned=false for %q", in)
		}
	}
}

func TestBody_CanonicalApostrophe(t *testing.T) {
	// Standard ASCII apostrophe, slash separators — should normalise separators.
	in := []byte(`{"system":"Today's date is 2026/07/03"}`)
	out, cleaned := Body(in)
	if !cleaned {
		t.Fatal("expected cleaned=true")
	}
	if !bytes.Contains(out, want) {
		t.Errorf("standard apostrophe: unexpected output %q", out)
	}
}

func TestBody_RightSingleQuotation(t *testing.T) {
	// U+2019 RIGHT SINGLE QUOTATION MARK, as an actual rune in the bytes.
	in := []byte("Today’s date is 2026-07-03")
	out, cleaned := Body(in)
	if !cleaned {
		t.Fatal("expected cleaned=true")
	}
	if !bytes.Equal(out, want) {
		t.Errorf("U+2019: got %q, want %q", out, want)
	}
}

func TestBody_ModifierLetterApostrophe(t *testing.T) {
	// U+02BC MODIFIER LETTER APOSTROPHE.
	in := []byte("Todayʼs date is 2026-07-03")
	out, cleaned := Body(in)
	if !cleaned {
		t.Fatal("expected cleaned=true")
	}
	if !bytes.Equal(out, want) {
		t.Errorf("U+02BC: got %q, want %q", out, want)
	}
}

func TestBody_ModifierLetterPrime(t *testing.T) {
	// U+02B9 MODIFIER LETTER PRIME.
	in := []byte("Todayʹs date is 2026-07-03")
	out, cleaned := Body(in)
	if !cleaned {
		t.Fatal("expected cleaned=true")
	}
	if !bytes.Equal(out, want) {
		t.Errorf("U+02B9: got %q, want %q", out, want)
	}
}

func TestBody_JSONEscapedU2019(t *testing.T) {
	// The 6 literal characters ’ as a JSON encoder would emit them.
	in := []byte(`Today’s date is 2026-07-03`)
	out, cleaned := Body(in)
	if !cleaned {
		t.Fatal("expected cleaned=true")
	}
	if !bytes.Equal(out, want) {
		t.Errorf("JSON \\u2019: got %q, want %q", out, want)
	}
}

func TestBody_JSONEscapedU02BC(t *testing.T) {
	// The 6 literal characters ʼ.
	in := []byte(`Todayʼs date is 2026-07-03`)
	out, cleaned := Body(in)
	if !cleaned {
		t.Fatal("expected cleaned=true")
	}
	if !bytes.Equal(out, want) {
		t.Errorf("JSON \\u02bc: got %q, want %q", out, want)
	}
}

func TestBody_JSONEscapedApostrophe(t *testing.T) {
	// Backslash-escaped ASCII apostrophe: Today\'s (as some encoders emit).
	in := []byte(`Today\'s date is 2026-07-03`)
	out, cleaned := Body(in)
	if !cleaned {
		t.Fatal("expected cleaned=true")
	}
	if !bytes.Equal(out, want) {
		t.Errorf("escaped apostrophe: got %q, want %q", out, want)
	}
}

func TestBody_SlashSeparator(t *testing.T) {
	// YYYY/MM/DD should yield YYYY-MM-DD.
	in := []byte("Today’s date is 2026/07/03")
	out, cleaned := Body(in)
	if !cleaned {
		t.Fatal("expected cleaned=true")
	}
	if !bytes.Equal(out, want) {
		t.Errorf("slash separator: got %q, want %q", out, want)
	}
}

func TestBody_CaseInsensitive(t *testing.T) {
	in := []byte("today’s date is 2026-07-03")
	out, cleaned := Body(in)
	if !cleaned {
		t.Fatal("expected cleaned=true")
	}
	if !bytes.Equal(out, want) {
		t.Errorf("lowercase today: got %q, want %q", out, want)
	}
}

func TestBody_InsideJSON(t *testing.T) {
	// Realistic payload: marker embedded in a system message value.
	in := []byte("{\"model\":\"claude-opus-4-8\",\"system\":\"You are helpful. Today’s date is 2026/07/03. Be concise.\",\"messages\":[]}")
	out, cleaned := Body(in)
	if !cleaned {
		t.Fatal("expected cleaned=true")
	}
	if bytes.Contains(out, []byte("’")) {
		t.Errorf("U+2019 still present after sanitise: %q", out)
	}
	if !bytes.Contains(out, want) {
		t.Errorf("canonical form not found in output: %q", out)
	}
}

func TestBody_Idempotent(t *testing.T) {
	// Running Body twice yields identical bytes. Note the canonical ASCII form
	// still matches the regex, so cleaned stays true on the second pass — the
	// guarantee is byte-stability, not that the flag flips.
	in := []byte("Today’s date is 2026-07-03")
	once, cleaned := Body(in)
	if !cleaned {
		t.Fatal("expected cleaned=true on first pass")
	}
	twice, _ := Body(once)
	if !bytes.Equal(once, twice) {
		t.Errorf("not idempotent: first=%q second=%q", once, twice)
	}
	if !bytes.Equal(once, want) {
		t.Errorf("first pass not canonical: got %q, want %q", once, want)
	}
}

func BenchmarkBody_NoMarker(b *testing.B) {
	payload := []byte(`{"model":"claude","messages":[{"role":"user","content":"hello world"}]}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Body(payload)
	}
}

func BenchmarkBody_WithMarker(b *testing.B) {
	payload := []byte("{\"model\":\"claude\",\"system\":\"Today’s date is 2026/07/03\",\"messages\":[]}")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Body(payload)
	}
}
