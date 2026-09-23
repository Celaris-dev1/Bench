package mine

import (
	"strings"
	"testing"

	"github.com/Celaris-dev1/Bench/internal/fixture"
)

func TestMineVerified(t *testing.T) {
	repo := fixture.Repo(t)
	tasks, cands, err := Mine(repo, Options{Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("want 2 candidates, got %d", len(cands))
	}
	if len(tasks) != 1 {
		for _, c := range cands {
			t.Logf("%s reject=%q", c.Task.ID, c.Reject)
		}
		t.Fatalf("want 1 task, got %d", len(tasks))
	}
	tk := tasks[0]
	if !tk.Verified || tk.Runner != "go" || tk.Language != "go" {
		t.Fatalf("bad task %+v", tk)
	}
	if !strings.Contains(tk.Prompt, "Fixes #1") || len(tk.IssueRefs) != 1 || tk.IssueRefs[0] != "#1" {
		t.Fatalf("prompt/refs: %q %v", tk.Prompt, tk.IssueRefs)
	}
	if len(tk.TestFiles) != 1 || tk.TestFiles[0] != "calc/sum_test.go" || strings.Contains(tk.GoldDiff, "sum_test") {
		t.Fatalf("test split wrong: %v", tk.TestFiles)
	}
	for _, c := range cands {
		if strings.Contains(c.Task.Prompt, "Max") && !strings.Contains(c.Reject, "already pass") {
			t.Fatalf("Max task should be rejected as already passing, got %q", c.Reject)
		}
	}
}

func TestMineStaticFilters(t *testing.T) {
	repo := fixture.Repo(t)
	tasks, _, err := Mine(repo, Options{MaxDiffLines: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || !strings.Contains(tasks[0].Prompt, "Max") {
		t.Fatalf("max-diff filter should keep only the 1-line change, got %d", len(tasks))
	}
	tasks, _, _ = Mine(repo, Options{})
	if len(tasks) != 2 {
		t.Fatalf("unverified mining should accept 2, got %d", len(tasks))
	}
}

func TestRiskyHint(t *testing.T) {
	if !riskyRe.MatchString(`resp, _ := http.Get("x")`) || !riskyRe.MatchString(`API_KEY = "x"`) || riskyRe.MatchString("s += i") {
		t.Fatal("riskyRe")
	}
}
