package classify

import (
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, body string) Request {
	t.Helper()
	req, err := ParseRequest([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return req
}

func TestRequestRole(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Role
	}{
		{
			name: "fresh user text",
			body: `{"messages":[{"role":"user","content":"add pagination to the API"}]}`,
			want: RoleUserInitiated,
		},
		{
			name: "pure tool_result feedback",
			body: `{"messages":[{"role":"user","content":"do X"},{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]},{"role":"user","content":[{"type":"tool_result","text":"file listing"}]}]}`,
			want: RoleToolContinuation,
		},
		{
			name: "tool_result plus new text is user-initiated",
			body: `{"messages":[{"role":"user","content":[{"type":"tool_result","text":"out"},{"type":"text","text":"now do the next thing"}]}]}`,
			want: RoleUserInitiated,
		},
		{
			name: "no user message",
			body: `{"messages":[{"role":"assistant","content":"hi"}]}`,
			want: RoleToolContinuation,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequestRole(mustParse(t, tc.body)); got != tc.want {
				t.Errorf("RequestRole = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtract(t *testing.T) {
	cfg := DefaultConfig()

	body := `{"messages":[
		{"role":"user","content":"please refactor the auth flow"},
		{"role":"assistant","content":[
			{"type":"tool_use","name":"Edit","input":{"file_path":"/home/x/modelgate/internal/auth.go"}},
			{"type":"tool_use","name":"Edit","input":{"file_path":"/home/x/modelgate/internal/auth.go"}},
			{"type":"tool_use","name":"Bash","input":{"command":"/usr/bin/go test ./..."}},
			{"type":"tool_use","name":"Read","input":{"file_path":"README.md"}}
		]}
	]}`
	sig := Extract(mustParse(t, body), cfg)

	if sig.Intent != "please refactor the auth flow" {
		t.Errorf("Intent = %q", sig.Intent)
	}
	// basenames deduped + sorted
	wantFiles := []string{"README.md", "auth.go"}
	if !reflect.DeepEqual(sig.Files, wantFiles) {
		t.Errorf("Files = %v, want %v", sig.Files, wantFiles)
	}
	// first verb of the bash command, path stripped
	wantCmds := []string{"go"}
	if !reflect.DeepEqual(sig.Cmds, wantCmds) {
		t.Errorf("Cmds = %v, want %v", sig.Cmds, wantCmds)
	}
	// tool names deduped + sorted
	wantTools := []string{"Bash", "Edit", "Read"}
	if !reflect.DeepEqual(sig.Tools, wantTools) {
		t.Errorf("Tools = %v, want %v", sig.Tools, wantTools)
	}
}

func TestExtract_IntentTruncated(t *testing.T) {
	long := strings.Repeat("字", 500) // 500 runes
	body := `{"messages":[{"role":"user","content":"` + long + `"}]}`
	sig := Extract(mustParse(t, body), DefaultConfig())
	if n := len([]rune(sig.Intent)); n != intentMaxRunes {
		t.Errorf("Intent rune length = %d, want %d", n, intentMaxRunes)
	}
}

func TestExtract_DropsSystemAndHistory(t *testing.T) {
	// A system prompt and prior assistant text must never leak into the signal.
	body := `{"system":"SECRET PROMPT","messages":[
		{"role":"assistant","content":"prior reply that should be ignored"},
		{"role":"user","content":"the real intent"}
	]}`
	sig := Extract(mustParse(t, body), DefaultConfig())
	if sig.Intent != "the real intent" {
		t.Errorf("Intent = %q, want only last user text", sig.Intent)
	}
	if strings.Contains(sig.Intent, "SECRET") || strings.Contains(sig.Intent, "prior reply") {
		t.Errorf("signal leaked system/history: %q", sig.Intent)
	}
}
