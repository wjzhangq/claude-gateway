package classify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// stubHaiku spins up an httptest server that counts /v1/messages hits and returns
// the given body with the given status. Returns a client pointed at it plus the
// call counter.
func stubHaiku(t *testing.T, status int, respBody string) (*HaikuClient, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	hc := NewHaikuClient(srv.URL, "k", "claude-haiku-4-5")
	return hc, &calls
}

func TestAnalyze_RulesDecideTypeHaikuFillsWork(t *testing.T) {
	cfg := DefaultConfig()
	// Editing a .go file lets rules decide code/后端, but work_related is always
	// deferred to Haiku now (no internal-repo rule), so Haiku is still consulted.
	body := `{"messages":[
		{"role":"user","content":"fix auth"},
		{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/x/svc/auth.go"}}]}
	]}`
	req := mustParse(t, body)

	// Haiku fills only work_related; it must not override the rule's code/后端 hint.
	resp := `{"content":[{"type":"text","text":"{\"work_related\":true,\"work_reason\":\"修复服务鉴权\"}"}]}`
	hc, calls := stubHaiku(t, 200, resp)
	res, err := Analyze(context.Background(), req, cfg, hc)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if *calls != 1 {
		t.Errorf("Haiku calls = %d, want 1 (work_related deferred to Haiku)", *calls)
	}
	if res.TaskType != "code" || res.CodeDirection != "后端" {
		t.Errorf("verdict = %+v, want code/后端 (rule hint preserved)", res)
	}
	if res.WorkRelated == nil || !*res.WorkRelated {
		t.Errorf("WorkRelated = %v, want true (from Haiku)", res.WorkRelated)
	}
}

func TestAnalyze_ContinuationSkipsModel(t *testing.T) {
	cfg := DefaultConfig()
	body := `{"messages":[
		{"role":"user","content":"do X"},
		{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]},
		{"role":"user","content":[{"type":"tool_result","text":"listing"}]}
	]}`
	req := mustParse(t, body)

	hc, calls := stubHaiku(t, 200, `{"content":[{"type":"text","text":"{}"}]}`)
	res, err := Analyze(context.Background(), req, cfg, hc)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Role != RoleToolContinuation {
		t.Errorf("Role = %q, want tool_continuation", res.Role)
	}
	if *calls != 0 {
		t.Errorf("Haiku calls = %d, want 0 (continuation)", *calls)
	}
}

func TestAnalyze_HaikuFailureKeepsRuleVerdict(t *testing.T) {
	cfg := DefaultConfig()
	// A doc task with no repo hit → work_related undetermined → needs Haiku.
	body := `{"messages":[
		{"role":"user","content":"write the guide"},
		{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"guide.md"}}]}
	]}`
	req := mustParse(t, body)

	// Haiku returns 500 → Fill errors.
	hc, calls := stubHaiku(t, 500, `boom`)
	res, err := Analyze(context.Background(), req, cfg, hc)
	if err == nil {
		t.Fatalf("expected error from failed Haiku")
	}
	if *calls != 1 {
		t.Errorf("Haiku calls = %d, want 1", *calls)
	}
	// Rule verdict preserved despite the failure.
	if res.TaskType != "doc" {
		t.Errorf("TaskType = %q, want doc (rule verdict kept)", res.TaskType)
	}
	if res.FromHaiku {
		t.Errorf("FromHaiku = true, want false on failure")
	}
}

func TestAnalyze_HaikuFillsGaps(t *testing.T) {
	cfg := DefaultConfig()
	body := `{"messages":[
		{"role":"user","content":"just answer a question, no files"}
	]}`
	req := mustParse(t, body)

	// Rules leave task_type empty; Haiku supplies other + not-work.
	resp := `{"content":[{"type":"text","text":"{\"task_type\":\"other\",\"work_related\":false,\"work_reason\":\"个人问答\"}"}]}`
	hc, calls := stubHaiku(t, 200, resp)
	res, err := Analyze(context.Background(), req, cfg, hc)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if *calls != 1 {
		t.Errorf("Haiku calls = %d, want 1", *calls)
	}
	if res.TaskType != "other" {
		t.Errorf("TaskType = %q, want other (from Haiku)", res.TaskType)
	}
	if res.WorkRelated == nil || *res.WorkRelated {
		t.Errorf("WorkRelated = %v, want false", res.WorkRelated)
	}
	if !res.FromHaiku {
		t.Errorf("FromHaiku = false, want true")
	}
}

func TestAnalyze_PureRulesMode(t *testing.T) {
	cfg := DefaultConfig()
	body := `{"messages":[{"role":"user","content":"question with no files"}]}`
	req := mustParse(t, body)

	// hc == nil → pure rules, no fallback even though rules are incomplete.
	res, err := Analyze(context.Background(), req, cfg, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.FromHaiku {
		t.Errorf("FromHaiku = true, want false in pure-rules mode")
	}
	if !res.NeedHaiku {
		t.Errorf("NeedHaiku = false, want true (incomplete but no client)")
	}
}
