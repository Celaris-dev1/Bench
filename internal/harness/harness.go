// Package harness replays tasks against agents and scores them.
package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Celaris-dev1/Bench/internal/classify"
	"github.com/Celaris-dev1/Bench/internal/detect"
	"github.com/Celaris-dev1/Bench/internal/gitx"
	"github.com/Celaris-dev1/Bench/internal/task"
	"github.com/Celaris-dev1/Bench/internal/testrun"
)

// Adapter produces changes in a worktree for a task.
type Adapter interface {
	Name() string
	Solve(ctx context.Context, wt *gitx.Worktree, t task.Task) error
}

// Gold applies the reference fix (upper-bound baseline).
type Gold struct{}

func (Gold) Name() string { return "gold" }
func (Gold) Solve(_ context.Context, wt *gitx.Worktree, t task.Task) error {
	return wt.Apply(t.GoldDiff)
}

// Noop makes no changes (lower-bound baseline).
type Noop struct{}

func (Noop) Name() string                                           { return "noop" }
func (Noop) Solve(context.Context, *gitx.Worktree, task.Task) error { return nil }

// Shell runs an arbitrary command in the worktree. The prompt is passed via
// BENCH_PROMPT and BENCH_PROMPT_FILE; the worktree path via BENCH_WORKTREE.
type Shell struct{ Command string }

func (s Shell) Name() string { return "shell" }
func (s Shell) Solve(ctx context.Context, wt *gitx.Worktree, t task.Task) error {
	pf, err := os.CreateTemp("", "bench-prompt-*.md")
	if err != nil {
		return err
	}
	defer os.Remove(pf.Name())
	pf.WriteString(t.Prompt)
	pf.Close()
	cmd := exec.CommandContext(ctx, "sh", "-c", s.Command)
	cmd.Dir = wt.Dir
	cmd.Env = append(os.Environ(), "BENCH_PROMPT="+t.Prompt, "BENCH_PROMPT_FILE="+pf.Name(),
		"BENCH_WORKTREE="+wt.Dir, "BENCH_TASK_ID="+t.ID, "BENCH_LANGUAGE="+t.Language)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("agent command failed: %v: %s", err, tail(string(out), 2000))
	}
	return nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// ParseAdapter builds an adapter from a spec: "gold", "noop", or "shell:<cmd>".
func ParseAdapter(spec string) (Adapter, error) {
	switch {
	case spec == "gold":
		return Gold{}, nil
	case spec == "noop":
		return Noop{}, nil
	case strings.HasPrefix(spec, "shell:"):
		return Shell{Command: strings.TrimPrefix(spec, "shell:")}, nil
	}
	return nil, fmt.Errorf("unknown adapter %q (want gold, noop, or shell:<command>)", spec)
}

// Options configures a run.
type Options struct {
	AgentName, Model string
	AgentTimeout     time.Duration
	Test             testrun.Options
	UseGate          bool
	UseLLM           bool
	Log              func(string, ...any)
}

// Evaluate runs adapter over tasks.
func Evaluate(a Adapter, tasks []task.Task, o Options) task.Run {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.AgentName == "" {
		o.AgentName = a.Name()
	}
	if o.AgentTimeout == 0 {
		o.AgentTimeout = 30 * time.Minute
	}
	b := make([]byte, 6)
	rand.Read(b)
	run := task.Run{ID: time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b), Agent: o.AgentName, Model: o.Model, StartedAt: time.Now().UTC()}
	if len(tasks) > 0 {
		run.Repo = tasks[0].Repo
	}
	for _, t := range tasks {
		r := One(a, t, o)
		o.Log("%s %s pass=%v %s", run.Agent, t.ID, r.Passed, r.FailureMode)
		run.Results = append(run.Results, r)
	}
	return run
}

// One evaluates a single task.
func One(a Adapter, t task.Task, o Options) (res task.Result) {
	start := time.Now()
	res = task.Result{TaskID: t.ID}
	defer func() { res.DurationMS = time.Since(start).Milliseconds() }()
	dir, err := os.MkdirTemp("", "bench-run-")
	if err != nil {
		res.Error = err.Error()
		return res
	}
	os.Remove(dir)
	wt, err := gitx.AddWorktree(t.Repo, dir, t.Parent)
	if err != nil {
		res.Error = err.Error()
		res.FailureMode = classify.AgentError
		return res
	}
	defer wt.Remove()

	ctx, cancel := context.WithTimeout(context.Background(), o.AgentTimeout)
	agentErr := a.Solve(ctx, wt, t)
	cancel()
	res.AgentDiff, _ = wt.Diff(t.TestFiles)

	// Restore pristine tree and apply only the agent's non-test changes, then the held-out tests.
	_, _ = gitx.Run(wt.Dir, "reset", "-q", "--hard", t.Parent)
	_, _ = gitx.Run(wt.Dir, "clean", "-fdq")
	applyErr := wt.Apply(res.AgentDiff)
	if applyErr == nil {
		applyErr = wt.CheckoutFiles(t.Commit, t.TestFiles)
	}
	runner := t.Runner
	if runner == "" {
		runner = detect.Runner(wt.Dir, t.Language)
	}
	var out testrun.Outcome
	if applyErr == nil {
		out = testrun.Run(wt.Dir, detect.Command(runner, t.TestFiles), o.Test)
	}
	res.Output = out.Output
	res.Passed = agentErr == nil && applyErr == nil && out.Passed

	if o.UseGate && strings.TrimSpace(res.AgentDiff) != "" {
		res.RiskScore = GateRisk(wt.Dir, res.AgentDiff)
	}
	if !res.Passed {
		in := classify.Input{Prompt: t.Prompt, AgentDiff: res.AgentDiff, GoldDiff: t.GoldDiff, Output: out.Output, TimedOut: out.TimedOut, Risk: res.RiskScore}
		if agentErr != nil {
			in.AgentErr, res.Error = agentErr.Error(), agentErr.Error()
		} else if applyErr != nil {
			in.AgentErr, res.Error = applyErr.Error(), applyErr.Error()
		}
		res.FailureMode = classify.Classify(in, o.UseLLM)
	}
	return res
}

// GateRisk invokes the `gate` binary if present on PATH and extracts a 0..1 risk score.
// Invocation: gate run --repo <dir> --diff <file> --format json (override args via BENCH_GATE_ARGS,
// where {repo} and {diff} are substituted).
func GateRisk(dir, diff string) *float64 {
	bin, err := exec.LookPath("gate")
	if err != nil {
		return nil
	}
	f, err := os.CreateTemp("", "bench-diff-*.patch")
	if err != nil {
		return nil
	}
	defer os.Remove(f.Name())
	f.WriteString(diff)
	f.Close()
	argTmpl := os.Getenv("BENCH_GATE_ARGS")
	if argTmpl == "" {
		argTmpl = "run --repo {repo} --diff {diff} --format json --exit-zero"
	}
	var args []string
	for _, a := range strings.Fields(argTmpl) {
		a = strings.ReplaceAll(a, "{repo}", dir)
		args = append(args, strings.ReplaceAll(a, "{diff}", f.Name()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	out, _ := cmd.Output()
	return parseRisk(out)
}

func parseRisk(out []byte) *float64 {
	var m map[string]any
	if json.Unmarshal(out, &m) != nil {
		// tolerate trailing JSON line
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) == 0 || json.Unmarshal([]byte(lines[len(lines)-1]), &m) != nil {
			return nil
		}
	}
	for _, k := range []string{"risk_score", "risk"} {
		if v, ok := m[k].(float64); ok {
			if v > 1 {
				v /= 100
			}
			return &v
		}
	}
	// Gate's native report nests the score under verdict.score.
	if v, ok := m["verdict"].(map[string]any); ok {
		if sc, ok := v["score"].(float64); ok {
			return &sc
		}
	}
	if d, ok := m["decision"].(map[string]any); ok {
		b, _ := json.Marshal(d)
		return parseRisk(b)
	}
	return nil
}
