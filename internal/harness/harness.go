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
	"path/filepath"
	"strings"
	"time"

	"github.com/Celaris-dev1/Bench/internal/classify"
	"github.com/Celaris-dev1/Bench/internal/detect"
	"github.com/Celaris-dev1/Bench/internal/dockerx"
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

// AgentInfo is what an Adapter's turn observed, returned alongside its
// error. Adapters that cannot determine transcript/usage leave it zero.
type AgentInfo struct {
	Transcript string
	TokensIn   int
	TokensOut  int
	CostUSD    *float64
}

// Adapter produces changes in a workspace for a task.
type Adapter interface {
	Name() string
	Solve(ctx context.Context, ws Workspace, t task.Task) (AgentInfo, error)
}

// Gold applies the reference fix (upper-bound baseline).
type Gold struct{}

func (Gold) Name() string { return "gold" }
func (Gold) Solve(_ context.Context, ws Workspace, t task.Task) (AgentInfo, error) {
	return AgentInfo{}, ws.Apply(t.GoldDiff)
}

// Noop makes no changes (lower-bound baseline).
type Noop struct{}

func (Noop) Name() string { return "noop" }
func (Noop) Solve(context.Context, Workspace, task.Task) (AgentInfo, error) {
	return AgentInfo{}, nil
}

// scratchDir is a directory inside the workspace used to pass the prompt to
// command-based adapters (and, for real agent CLIs, capture their
// transcript/session files). It is always removed before the agent's diff
// is computed, and is excluded from that diff defensively either way, so it
// never shows up as a spurious "change" the agent made.
const scratchDir = ".bench-agent"

// BuildInput is what a CommandAdapter.Build function is given to construct
// its argv and env.
type BuildInput struct {
	WorkspaceDir  string // host path to the workspace (== Workspace.Path())
	PromptFile    string // absolute host path to a file containing the task prompt
	PromptFileRel string // that same file's path relative to WorkspaceDir (for use inside a container, where WorkspaceDir is mounted at /work)
	InDocker      bool   // true when the built command will run inside a container (see CommandAdapter.DockerImage), so a Build func must translate any host path (e.g. PromptFile) to its /work-relative form
	Task          task.Task
}

// ParseUsage extracts token counts / cost from a command's combined
// stdout+stderr, when the underlying CLI reports them (e.g. as trailing
// JSON). It may return zero values if the output carries none.
type ParseUsage func(output string) (tokensIn, tokensOut int, costUSD *float64)

// CommandAdapter runs a single external command, built by Build, to solve a
// task. It is the common implementation behind "shell:" and the built-in
// agent CLIs (claude-code, codex, cursor, aider): all of them just build an
// argv/env and exec it, so docker wrapping (--agent-docker) and prompt/
// transcript handling live here once instead of once per adapter.
type CommandAdapter struct {
	AgentName     string
	Build         func(BuildInput) (argv []string, env []string, err error)
	Usage         ParseUsage // optional
	DockerImage   string     // if set, the built command runs inside this image via `docker run`
	DockerNetwork bool       // for DockerImage: allow the container network (most agent CLIs need to reach their API)
}

func (c CommandAdapter) Name() string { return c.AgentName }

func (c CommandAdapter) Solve(ctx context.Context, ws Workspace, t task.Task) (AgentInfo, error) {
	scratch := filepath.Join(ws.Path(), scratchDir)
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return AgentInfo{}, err
	}
	defer os.RemoveAll(scratch)
	promptAbs := filepath.Join(scratch, "prompt.md")
	if err := os.WriteFile(promptAbs, []byte(t.Prompt), 0o644); err != nil {
		return AgentInfo{}, err
	}

	argv, env, err := c.Build(BuildInput{
		WorkspaceDir:  ws.Path(),
		PromptFile:    promptAbs,
		PromptFileRel: scratchDir + "/prompt.md",
		InDocker:      c.DockerImage != "",
		Task:          t,
	})
	if err != nil {
		return AgentInfo{}, err
	}
	if len(argv) == 0 {
		return AgentInfo{}, fmt.Errorf("%s: empty command", c.AgentName)
	}

	var cmd *exec.Cmd
	if c.DockerImage != "" {
		dargs := append([]string{"docker"}, dockerx.Args(dockerx.RunOptions{
			Image: c.DockerImage, Dir: ws.Path(), Network: c.DockerNetwork,
			Env: env, Command: argv,
		})...)
		cmd = exec.CommandContext(ctx, dargs[0], dargs[1:]...)
		cmd.Env = os.Environ()
	} else {
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = ws.Path()
		cmd.Env = append(os.Environ(), env...)
	}

	out, runErr := cmd.CombinedOutput()
	info := AgentInfo{Transcript: string(out)}
	if c.Usage != nil {
		info.TokensIn, info.TokensOut, info.CostUSD = c.Usage(string(out))
	}
	if runErr != nil {
		if _, ok := runErr.(*exec.Error); ok {
			return info, fmt.Errorf("%s: %w (is it installed and on PATH?)", c.AgentName, runErr)
		}
		return info, fmt.Errorf("agent command failed: %v: %s", runErr, tail(string(out), 2000))
	}
	return info, nil
}

// Shell runs an arbitrary shell command in the workspace. The prompt is
// passed via BENCH_PROMPT and BENCH_PROMPT_FILE; the workspace path via
// BENCH_WORKTREE. With DockerImage set, the command runs inside that image
// instead (BENCH_WORKTREE/BENCH_PROMPT_FILE are then container paths under
// /work, and BENCH_PROMPT is omitted to avoid unbounded -e argument sizes).
func Shell(command, dockerImage string, dockerNetwork bool) CommandAdapter {
	return CommandAdapter{
		AgentName:     "shell",
		DockerImage:   dockerImage,
		DockerNetwork: dockerNetwork,
		Build: func(in BuildInput) ([]string, []string, error) {
			if dockerImage != "" {
				return []string{"sh", "-c", command},
					[]string{"BENCH_PROMPT_FILE=/work/" + in.PromptFileRel, "BENCH_WORKTREE=/work",
						"BENCH_TASK_ID=" + in.Task.ID, "BENCH_LANGUAGE=" + in.Task.Language}, nil
			}
			return []string{"sh", "-c", command},
				[]string{"BENCH_PROMPT=" + in.Task.Prompt, "BENCH_PROMPT_FILE=" + in.PromptFile,
					"BENCH_WORKTREE=" + in.WorkspaceDir, "BENCH_TASK_ID=" + in.Task.ID, "BENCH_LANGUAGE=" + in.Task.Language}, nil
		},
	}
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

// AdapterOptions carries flags that affect how a parsed adapter runs
// (currently, agent-side Docker sandboxing).
type AdapterOptions struct {
	DockerImage   string // --agent-docker: run the agent's command inside this image
	DockerNetwork bool   // allow that container network access (default true set by caller; most agents need their API)
}

// ParseAdapter builds an adapter from a spec: "gold", "noop", "shell:<cmd>",
// or one of the built-in agent CLIs ("claude-code[:model]", "codex[:model]",
// "cursor[:model]", "aider[:model]"); see internal/harness/adapters.go.
func ParseAdapter(spec string, o AdapterOptions) (Adapter, error) {
	switch {
	case spec == "gold":
		return Gold{}, nil
	case spec == "noop":
		return Noop{}, nil
	case strings.HasPrefix(spec, "shell:"):
		return Shell(strings.TrimPrefix(spec, "shell:"), o.DockerImage, o.DockerNetwork), nil
	}
	name, model, _ := strings.Cut(spec, ":")
	if build, ok := builtinAgents[name]; ok {
		ca := build(model)
		ca.DockerImage, ca.DockerNetwork = o.DockerImage, o.DockerNetwork
		return ca, nil
	}
	return nil, fmt.Errorf("unknown adapter %q (want gold, noop, shell:<command>, or one of %s)", spec, builtinAgentNames())
}

// Options configures a run.
type Options struct {
	AgentName, Model string
	AgentTimeout     time.Duration
	Test             testrun.Options
	UseGate          bool
	UseLLM           bool
	Trials           int // trials per task, for pass@k and flakiness (default 1)
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
	if o.Trials <= 0 {
		o.Trials = 1
	}
	b := make([]byte, 6)
	rand.Read(b)
	run := task.Run{ID: time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b), Agent: o.AgentName, Model: o.Model, StartedAt: time.Now().UTC()}
	if len(tasks) > 0 {
		run.Repo = tasks[0].Repo
	}
	for _, t := range tasks {
		for trial := 0; trial < o.Trials; trial++ {
			r := One(a, t, o)
			o.Log("%s %s trial=%d/%d pass=%v %s", run.Agent, t.ID, trial+1, o.Trials, r.Passed, r.FailureMode)
			run.Results = append(run.Results, r)
		}
	}
	return run
}

// One evaluates a single task, once. The agent runs inside a
// leakage-isolated gitx.Sandbox: a fresh, one-commit synthetic repo built
// from the task's parent tree, with no history and no reachable fix commit.
// Held-out test files are read from the real repository (outside the
// sandbox) and injected only after the agent's turn has ended, so an agent
// can never see them, let alone the fix, from inside its workspace.
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
	info, agentErr := a.Solve(ctx, sb, t)
	cancel()
	res.Transcript, res.TokensIn, res.TokensOut, res.CostUSD = info.Transcript, info.TokensIn, info.TokensOut, info.CostUSD
	res.AgentDiff, _ = sb.Diff(append(append([]string{}, t.TestFiles...), scratchDir))

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
