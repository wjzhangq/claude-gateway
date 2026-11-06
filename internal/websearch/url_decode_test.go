package websearch

import "testing"

func TestDecodeRedirectURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Bing redirect URL",
			input:    "https://api.microsoftol.com/ck/a?!&&p=5e7f58ffeb6c5b8013774f9d76f699bd7b9865da90725a34b033f45ea31d110cJmltdHM9MTc5Mzk1Njg2NyZpZ3VpZD1iNmJiNDZmNS03NDkwLTQ2MTEtYWFiMi03M2E1YmNhN2YzYmYmbGdtYXA9UDJOcFpEMDFRekl5UXpBNU56a3lNRU0wT1VaR1FUUkdSVVU0UlVNeFF6WTVSVGsxUkNaaGNIQnVjejFFWlhaRmVDWnJQVEV3TURBbQ&ptn=10&ntb=1&u=a1aHR0cHM6Ly9jbGF1ZGUuY29tLw",
			expected: "https://claude.com/",
		},
		{
			name:     "Another redirect example",
			input:    "https://api.microsoftol.com/ck/a?u=a1aHR0cHM6Ly93d3cuYW50aHJvcGljLmNvbS8",
			expected: "https://www.anthropic.com/",
		},
		{
			name:     "Regular URL unchanged",
			input:    "https://example.com/path",
			expected: "https://example.com/path",
		},
		{
			name:     "Empty URL",
			input:    "",
			expected: "",
		},
		{
			name:     "Malformed redirect (no u param)",
			input:    "https://api.microsoftol.com/ck/a?foo=bar",
			expected: "https://api.microsoftol.com/ck/a?foo=bar",
		},
		{
			name:     "Malformed redirect (invalid base64)",
			input:    "https://api.microsoftol.com/ck/a?u=a1###invalid###",
			expected: "https://api.microsoftol.com/ck/a?u=a1###invalid###",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := decodeRedirectURL(tt.input)
			if result != tt.expected {
				t.Errorf("decodeRedirectURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
