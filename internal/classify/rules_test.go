package classify

import "testing"

func TestClassify_CodeDirectionBySuffix(t *testing.T) {
	cfg := DefaultConfig()
	req := mustParse(t, `{"messages":[{"role":"user","content":"edit"}]}`)

	sig := Signal{Files: []string{"a.go", "b.go", "c.js"}}
	res := Classify(req, sig, cfg)
	if res.TaskType != "code" {
		t.Errorf("TaskType = %q, want code", res.TaskType)
	}
	// 后端 (2 votes) beats 前端 (1 vote)
	if res.CodeDirection != "后端" {
		t.Errorf("CodeDirection = %q, want 后端", res.CodeDirection)
	}
}

func TestClassify_TieLeavesDirectionEmpty(t *testing.T) {
	cfg := DefaultConfig()
	req := mustParse(t, `{"messages":[{"role":"user","content":"edit"}]}`)

	sig := Signal{Files: []string{"a.go", "b.js"}} // 后端 vs 前端, 1-1 tie
	res := Classify(req, sig, cfg)
	if res.TaskType != "code" {
		t.Errorf("TaskType = %q, want code", res.TaskType)
	}
	if res.CodeDirection != "" {
		t.Errorf("CodeDirection = %q, want empty on tie", res.CodeDirection)
	}
	if !res.NeedHaiku {
		t.Errorf("NeedHaiku = false, want true (code with no direction)")
	}
}

func TestClassify_RepoHitSetsWorkRelated(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Repos = []string{"modelgate"}
	req := mustParse(t, `{"messages":[{"role":"user","content":"edit"}]}`)

	sig := Signal{Files: []string{"auth.go"}, Repo: "modelgate"}
	res := Classify(req, sig, cfg)
	if res.WorkRelated == nil || !*res.WorkRelated {
		t.Fatalf("WorkRelated = %v, want true", res.WorkRelated)
	}
	if res.WorkReason == "" {
		t.Errorf("WorkReason empty, want a reason mentioning the repo")
	}
	// code + direction (后端) + work_related all decided ⇒ no Haiku needed.
	if res.NeedHaiku {
		t.Errorf("NeedHaiku = true, want false (fully decided)")
	}
}

func TestClassify_DocTask(t *testing.T) {
	cfg := DefaultConfig()
	req := mustParse(t, `{"messages":[{"role":"user","content":"write docs"}]}`)

	sig := Signal{Files: []string{"README.md", "guide.md"}}
	res := Classify(req, sig, cfg)
	if res.TaskType != "doc" {
		t.Errorf("TaskType = %q, want doc", res.TaskType)
	}
	// doc task with no repo hit ⇒ work_related undetermined ⇒ needs Haiku.
	if !res.NeedHaiku {
		t.Errorf("NeedHaiku = false, want true (work_related undetermined)")
	}
}

func TestNeedHaiku(t *testing.T) {
	tr := true
	cases := []struct {
		name string
		res  Result
		want bool
	}{
		{"empty task_type", Result{TaskType: "", WorkRelated: &tr}, true},
		{"undetermined work", Result{TaskType: "other", WorkRelated: nil}, true},
		{"code no direction", Result{TaskType: "code", CodeDirection: "", WorkRelated: &tr}, true},
		{"fully decided code", Result{TaskType: "code", CodeDirection: "后端", WorkRelated: &tr}, false},
		{"fully decided other", Result{TaskType: "other", WorkRelated: &tr}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedHaiku(tc.res); got != tc.want {
				t.Errorf("NeedHaiku = %v, want %v", got, tc.want)
			}
		})
	}
}
