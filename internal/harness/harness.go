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

// Workspace is what an Adapter is given to work in: a directory it can edit
// freely. In practice it is always a *gitx.Sandbox -- a leakage-isolated,
// one-commit synthetic repo (see internal/gitx) -- so an agent has no way to
// reach the fix commit or any history beyond the task's starting point.
type Workspace interface {
	Path() string
	Apply(patch string) error
}

// Adapter produces changes in a workspace for a task.
type Adapter interface {
	Name() string
	Solve(ctx context.Context, ws Workspace, t task.Task) error
}

// Gold applies the reference fix (upper-bound baseline).
type Gold struct{}

func (Gold) Name() string { return "gold" }
func (Gold) Solve(_ context.Context, ws Workspace, t task.Task) error {
	return ws.Apply(t.GoldDiff)
}

// Noop makes no changes (lower-bound baseline).
type Noop struct{}

func (Noop) Name() string                                     { return "noop" }
func (Noop) Solve(context.Context, Workspace, task.Task) error { return nil }

// Shell runs an arbitrary command in the workspace. The prompt is passed via
// BENCH_PROMPT and BENCH_PROMPT_FILE; the workspace path via BENCH_WORKTREE.
type Shell struct{ Command string }

func (s Shell) Name() string { return "shell" }
func (s Shell) Solve(ctx context.Context, ws Workspace, t task.Task) error {
	pf, err := os.CreateTemp("", "bench-prompt-*.md")
	if err != nil {
		return err
	}
	defer os.Remove(pf.Name())
	pf.WriteString(t.Prompt)
	pf.Close()
	cmd := exec.CommandContext(ctx, "sh", "-c", s.Command)
	cmd.Dir = ws.Path()
	cmd.Env = append(os.Environ(), "BENCH_PROMPT="+t.Prompt, "BENCH_PROMPT_FILE="+pf.Name(),
		"BENCH_WORKTREE="+ws.Path(), "BENCH_TASK_ID="+t.ID, "BENCH_LANGUAGE="+t.Language)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("agent command failed: %v: %s", err, tail(string(out), 2000))
	}
	return nil
}

// injectHeldOut reads each held-out test file's content at t.Commit from the
// real repository (t.Repo) -- outside the sandbox entirely -- and writes it
// into the sandbox directly, bypassing git so the sandbox's own history
// never has to (and cannot) contain the fix.
func injectHeldOut(sb *gitx.Sandbox, t task.Task) error {
	files := make(map[string][]byte, len(t.TestFiles))
	for _, f := range t.TestFiles {
		content, ok, err := gitx.ShowFile(t.Repo, t.Commit, f)
		if err != nil {
			return err
		}
		if !ok {
			files[f] = nil // deleted at the fix commit
			continue
		}
		files[f] = content
	}
	return sb.InjectFiles(files)
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

// One evaluates a single task. The agent runs inside a leakage-isolated
// gitx.Sandbox: a fresh, one-commit synthetic repo built from the task's
// parent tree, with no history and no reachable fix commit. Held-out test
// files are read from the real repository (outside the sandbox) and
// injected only after the agent's turn has ended, so an agent can never see
// them, let alone the fix, from inside its workspace.
func One(a Adapter, t task.Task, o Options) (res task.Result) {
	start := time.Now()
	res = task.Result{TaskID: t.ID}
	defer func() { res.DurationMS = time.Since(start).Milliseconds() }()
	dir, err := os.MkdirTemp("", "bench-run-")
	if err != nil {
		res.Error = err.Error()
		return res
	}
	os.RemoveAll(dir)
	sb, err := gitx.NewSandbox(t.Repo, t.Parent, dir)
	if err != nil {
		res.Error = err.Error()
		res.FailureMode = classify.AgentError
		return res
	}
	defer sb.Remove()

	ctx, cancel := context.WithTimeout(context.Background(), o.AgentTimeout)
	agentErr := a.Solve(ctx, sb, t)
	cancel()
	res.AgentDiff, _ = sb.Diff(t.TestFiles)

	// Restore the sandbox's pristine base, apply only the agent's non-test
	// changes, then inject the held-out tests -- fetched from the real repo,
	// never from anything reachable inside the sandbox.
	applyErr := sb.ResetToBase()
	if applyErr == nil {
		applyErr = sb.Apply(res.AgentDiff)
	}
	if applyErr == nil {
		applyErr = injectHeldOut(sb, t)
	}
	runner := t.Runner
	if runner == "" {
		runner = detect.Runner(sb.Dir, t.Language)
	}
	var out testrun.Outcome
	if applyErr == nil {
		out = testrun.Run(sb.Dir, detect.Command(runner, t.TestFiles), o.Test)
	}
	res.Output = out.Output
	res.Passed = agentErr == nil && applyErr == nil && out.Passed

	if o.UseGate && strings.TrimSpace(res.AgentDiff) != "" {
		res.RiskScore = GateRisk(sb.Dir, res.AgentDiff)
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
