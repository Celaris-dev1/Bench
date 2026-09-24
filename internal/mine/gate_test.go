package mine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Celaris-dev1/Bench/internal/fixture"
	"github.com/Celaris-dev1/Bench/internal/gitx"
)

func rootSHA(t *testing.T, repo string) string {
	t.Helper()
	out, err := gitx.Run(repo, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

func TestMineFromGateFileAndDir(t *testing.T) {
	repo := fixture.Repo(t)
	root := rootSHA(t, repo)

	report := gateReport{
		ID: "abc-123", Repo: repo, Files: []string{"calc/sum.go"},
		Verdict: gateVerdict{Score: 0.81, Decision: "reject", Reasons: []string{"off-by-one risk"}},
		HeadSHA: root,
	}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}

	// As a single file.
	f := filepath.Join(t.TempDir(), "gate-result.json")
	if err := os.WriteFile(f, b, 0o644); err != nil {
		t.Fatal(err)
	}
	tasks, cands, err := MineFromGate(repo, f, Options{Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || len(tasks) != 1 {
		t.Fatalf("want 1 candidate/task, got %d/%d", len(cands), len(tasks))
	}
	tk := tasks[0]
	if !tk.Verified {
		t.Fatalf("task not verified: %+v", tk)
	}
	if !strings.Contains(tk.Prompt, "Gate rejected") || !strings.Contains(tk.Prompt, "off-by-one risk") {
		t.Fatalf("prompt missing Gate annotation: %q", tk.Prompt)
	}
	if tk.Commit == root || tk.Parent != root {
		t.Fatalf("expected the commit after root as the fix, got commit=%s parent=%s root=%s", tk.Commit, tk.Parent, root)
	}

	// As a directory of report files (also exercises the JSON-array form).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.json"), []byte("["+string(b)+"]"), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks2, _, err := MineFromGate(repo, dir, Options{Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks2) != 1 {
		t.Fatalf("want 1 task from dir form, got %d", len(tasks2))
	}
}

func TestMineFromGateSkipsNonRejectAndUnfixed(t *testing.T) {
	repo := fixture.Repo(t)
	root := rootSHA(t, repo)
	head, err := gitx.Head(repo)
	if err != nil {
		t.Fatal(err)
	}

	reports := []gateReport{
		{ID: "approved", Verdict: gateVerdict{Decision: "approve"}, HeadSHA: root},
		{ID: "no-fix-followed", Verdict: gateVerdict{Decision: "reject"}, HeadSHA: head, Files: []string{"README.md"}},
	}
	b, _ := json.Marshal(reports)
	f := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(f, b, 0o644); err != nil {
		t.Fatal(err)
	}
	tasks, cands, err := MineFromGate(repo, f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 || len(cands) != 0 {
		t.Fatalf("expected nothing mined (not rejected / no later fix), got tasks=%d cands=%d", len(tasks), len(cands))
	}
}
