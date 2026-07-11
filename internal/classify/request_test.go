package classify

import "testing"

func TestParseRequest(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantErr  bool
		wantMsgs int
		// wantText is the concatenated text of the first message's text blocks.
		wantFirstText string
	}{
		{
			name:          "string content",
			body:          `{"model":"claude","messages":[{"role":"user","content":"hello world"}]}`,
			wantMsgs:      1,
			wantFirstText: "hello world",
		},
		{
			name:          "array content",
			body:          `{"model":"claude","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"text","text":"there"}]}]}`,
			wantMsgs:      1,
			wantFirstText: "hi there",
		},
		{
			name:     "no messages",
			body:     `{"model":"claude"}`,
			wantErr:  true,
		},
		{
			name:    "truncated json",
			body:    `{"model":"claude","messages":[{"role":"user","content":"hel`,
			wantErr: true,
		},
		{
			name:    "not json",
			body:    `this is not json`,
			wantErr: true,
		},
		{
			name:    "empty messages array",
			body:    `{"model":"claude","messages":[]}`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := ParseRequest([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (req=%+v)", req)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(req.Messages) != tc.wantMsgs {
				t.Fatalf("messages = %d, want %d", len(req.Messages), tc.wantMsgs)
			}
			if tc.wantFirstText != "" {
				got := lastUserText(req)
				if got != tc.wantFirstText {
					t.Errorf("text = %q, want %q", got, tc.wantFirstText)
				}
			}
		})
	}
}
