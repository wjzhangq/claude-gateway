package sanitize

import (
	"testing"
)

func TestRemoveUpstreamDomains(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
		{
			name:  "no sensitive domains",
			input: `{"error": "something went wrong"}`,
			want:  `{"error": "something went wrong"}`,
		},
		{
			name:  "https URL with zjz-ai.webtrn.cn",
			input: `{"error": "request to https://zjz-ai.webtrn.cn/v1/messages failed"}`,
			want:  `{"error": "request to [upstream] failed"}`,
		},
		{
			name:  "https URL with dasheng.aibeke.com",
			input: `{"error": "upstream error from https://dasheng.aibeke.com/api/chat"}`,
			want:  `{"error": "upstream error from [upstream]"}`,
		},
		{
			name:  "http URL (lowercase)",
			input: `error connecting to http://zjz-ai.webtrn.cn:8080/test`,
			want:  `error connecting to [upstream]`,
		},
		{
			name:  "bare domain name",
			input: `{"message": "DNS resolution failed for zjz-ai.webtrn.cn"}`,
			want:  `{"message": "DNS resolution failed for [upstream]"}`,
		},
		{
			name:  "multiple occurrences",
			input: `Failed: https://zjz-ai.webtrn.cn/v1/chat and https://dasheng.aibeke.com/v1/messages both down`,
			want:  `Failed: [upstream] and [upstream] both down`,
		},
		{
			name:  "case insensitive matching",
			input: `Error from HTTPS://ZJZ-AI.WEBTRN.CN/endpoint`,
			want:  `Error from [upstream]`,
		},
		{
			name:  "domain in JSON with quotes",
			input: `{"url":"https://zjz-ai.webtrn.cn/v1/messages","status":500}`,
			want:  `{"url":"[upstream]","status":500}`,
		},
		{
			name:  "both domains in single message",
			input: `Tried zjz-ai.webtrn.cn then dasheng.aibeke.com, both failed`,
			want:  `Tried [upstream] then [upstream], both failed`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(RemoveUpstreamDomains([]byte(tt.input)))
			if got != tt.want {
				t.Errorf("RemoveUpstreamDomains() =\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestRemoveUpstreamDomainsIdempotent(t *testing.T) {
	input := []byte(`{"error": "failed at https://zjz-ai.webtrn.cn/v1/messages"}`)
	first := RemoveUpstreamDomains(input)
	second := RemoveUpstreamDomains(first)

	if string(first) != string(second) {
		t.Errorf("RemoveUpstreamDomains is not idempotent:\nfirst:  %q\nsecond: %q", first, second)
	}
}
