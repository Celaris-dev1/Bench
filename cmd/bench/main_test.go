package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Celaris-dev1/Bench/internal/fixture"
)

// TestEndToEnd builds the binary and drives mine → run → report → watch on a fixture repo.
func TestEndToEnd(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bench")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	repo := fixture.Repo(t)
	data := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "BENCH_DATABASE_URL=", "LEDGER_URL=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bench %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if out := run("mine", "--repo", repo, "--data", data); !strings.Contains(out, "accepted 1 tasks (1 new)") {
		t.Fatal(out)
	}
	run("run", "--agent", "gold", "--data", data)
	run("run", "--agent", "noop", "--data", data)
	cmp := run("compare", "--a", "gold", "--b", "noop", "--data", data)
	if !strings.Contains(cmp, "1 common tasks") || !strings.Contains(cmp, "McNemar") {
		t.Fatal(cmp)
	}
	md := run("report", "--data", data)
	if !strings.Contains(md, "| 1 | gold |  | 1/1 | 100.0%") || !strings.Contains(md, "no_change×1") {
		t.Fatal(md)
	}
	html := filepath.Join(data, "r.html")
	run("report", "--format", "html", "--out", html, "--data", data)
	if b, _ := os.ReadFile(html); !strings.Contains(string(b), "Bench leaderboard") {
		t.Fatal("html")
	}
	if out := run("watch", "--repo", repo, "--data", data, "--once"); !strings.Contains(out, "(0 new)") {
		t.Fatal(out)
	}
	if out := run("watch", "--repo", repo, "--data", data, "--once"); strings.Contains(out, "scanned") {
		t.Fatal("second watch should see no new commits: " + out)
	}
}
