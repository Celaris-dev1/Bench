package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Celaris-dev1/Bench/internal/fixture"
	"github.com/Celaris-dev1/Bench/internal/mine"
	"github.com/Celaris-dev1/Bench/internal/task"
)

func mined(t *testing.T) []task.Task {
	ts, _, err := mine.Mine(fixture.Repo(t), mine.Options{Verify: true})
	if err != nil || len(ts) != 1 {
		t.Fatalf("mine: %v %d", err, len(ts))
	}
	return ts
}

func TestBaselinesAndShell(t *testing.T) {
	ts := mined(t)
	if r := Evaluate(Gold{}, ts, Options{}); r.PassRate() != 1 {
		t.Fatalf("gold should pass: %+v", r.Results)
	}
	r := Evaluate(Noop{}, ts, Options{})
	if r.PassRate() != 0 || r.Results[0].FailureMode != "no_change" {
		t.Fatalf("noop: %+v", r.Results)
	}
	fixed := filepath.Join(t.TempDir(), "sum.go")
	os.WriteFile(fixed, []byte(fixture.FixedSum), 0o644)
	sh, _ := ParseAdapter(`shell:test -n "$BENCH_PROMPT" && test -f "$BENCH_PROMPT_FILE" && cp ` + fixed + ` calc/sum.go`)
	if r := Evaluate(sh, ts, Options{AgentName: "cp"}); r.PassRate() != 1 || r.Agent != "cp" {
		t.Fatalf("shell fix: %+v", r.Results)
	}
	// An agent that edits the held-out test to cheat must not benefit.
	cheat, _ := ParseAdapter(`shell:printf 'package calc\n' > calc/sum_test.go && echo '// x' >> calc/sum.go`)
	if r := Evaluate(cheat, ts, Options{}); r.PassRate() != 0 {
		t.Fatalf("cheat should fail: %+v", r.Results)
	}
	bad, _ := ParseAdapter(`shell:sed -i 's/s := 0/s := 1/' calc/sum.go`)
	r = Evaluate(bad, ts, Options{})
	if r.PassRate() != 0 || r.Results[0].AgentDiff == "" {
		t.Fatalf("bad: %+v", r.Results)
	}
	broken, _ := ParseAdapter(`shell:exit 3`)
	if r := Evaluate(broken, ts, Options{}); r.Results[0].FailureMode != "agent_error" {
		t.Fatalf("broken: %+v", r.Results)
	}
}

func TestParseRisk(t *testing.T) {
	if r := parseRisk([]byte(`{"risk_score": 42}`)); r == nil || *r != 0.42 {
		t.Fatal(r)
	}
	if r := parseRisk([]byte("log\n{\"decision\":{\"risk\":0.3}}")); r == nil || *r != 0.3 {
		t.Fatal(r)
	}
	if parseRisk([]byte("nope")) != nil {
		t.Fatal("expected nil")
	}
	if _, err := ParseAdapter("claude"); err == nil {
		t.Fatal("expected error")
	}
}
