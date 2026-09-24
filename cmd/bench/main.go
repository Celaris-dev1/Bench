// Command bench mines a repository's history into an evaluation suite and scores agents on it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Celaris-dev1/Bench/internal/gitx"
	"github.com/Celaris-dev1/Bench/internal/harness"
	"github.com/Celaris-dev1/Bench/internal/ledger"
	"github.com/Celaris-dev1/Bench/internal/mine"
	"github.com/Celaris-dev1/Bench/internal/report"
	"github.com/Celaris-dev1/Bench/internal/store"
	"github.com/Celaris-dev1/Bench/internal/task"
	"github.com/Celaris-dev1/Bench/internal/testrun"
)

const usage = `bench — repo-native, self-generating evaluation harness

Usage:
  bench mine   --repo <path> [--since sha] [--max-diff 400] [--no-verify] [--docker image]
  bench mine   --github owner/repo [--repo <local-clone>] [--max-prs N]  (GITHUB_TOKEN optional)
  bench mine   --from-gate <gate-results.json-or-dir> [--repo <local-clone>]
  bench mine   --from-ledger <ledger-url> --chain <chain> [--repo <local-clone>]
  bench tasks  [--repo <path>]
  bench run    --agent gold|noop|shell:<cmd> [--name n] [--model m] [--repo path] [--task id] [--gate] [--llm] [--trials N]
  bench report [--format md|html] [--out file] [--repo path]
  bench compare --a agentName[/model] --b agentName[/model] [--repo path]
  bench watch  --repo <path> [--interval 60s] [--agent spec ...] [--once]

Storage: BENCH_DATABASE_URL (postgres://...) or JSON files under --data (default .bench).
Ledger:  LEDGER_URL / LEDGER_TOKEN (optional).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "mine":
		err = cmdMine(os.Args[2:])
	case "tasks":
		err = cmdTasks(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	case "compare":
		err = cmdCompare(os.Args[2:])
	case "watch":
		err = cmdWatch(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

type common struct {
	db, data string
}

func (c *common) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.db, "db", os.Getenv("BENCH_DATABASE_URL"), "postgres DSN (empty = JSON files)")
	fs.StringVar(&c.data, "data", envOr("BENCH_DATA_DIR", ".bench"), "JSON data directory")
}

func (c *common) open() (store.Store, error) { return store.Open(context.Background(), c.db, c.data) }

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func logf(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }

func absRepo(p string) string {
	if p == "" {
		return ""
	}
	a, _ := filepath.Abs(p)
	return a
}

type mineFlags struct {
	repo, since, docker, github string
	fromGate, fromLedger, chain string
	maxDiff, maxCommits, maxPRs int
	noVerify                    bool
	timeout                     time.Duration
}

func bindMine(fs *flag.FlagSet, m *mineFlags) {
	fs.StringVar(&m.repo, "repo", ".", "repository path")
	fs.StringVar(&m.since, "since", "", "only mine commits after this sha")
	fs.StringVar(&m.docker, "docker", "", "run tests in this Docker image (network disabled)")
	fs.IntVar(&m.maxDiff, "max-diff", 400, "max changed source lines")
	fs.IntVar(&m.maxCommits, "max-commits", 0, "limit commits scanned (0 = all)")
	fs.BoolVar(&m.noVerify, "no-verify", false, "skip fail-before/pass-after verification")
	fs.DurationVar(&m.timeout, "test-timeout", 5*time.Minute, "per test invocation timeout")
	fs.StringVar(&m.github, "github", "", "mine merged PRs from owner/repo via the GitHub API instead of local history (--repo is still used to compute diffs/verify, if given; GITHUB_TOKEN is optional but raises the rate limit)")
	fs.IntVar(&m.maxPRs, "max-prs", 0, "limit merged PRs scanned with --github (0 = no limit)")
	fs.StringVar(&m.fromGate, "from-gate", "", "mine fixes for Gate-rejected changes from a `gate run --format json` results file or directory of them (--repo is the local clone to search for the fix)")
	fs.StringVar(&m.fromLedger, "from-ledger", "", "mine fixes for incidents recorded on a Ledger chain, e.g. https://ledger.example.com (LEDGER_TOKEN optional; --repo is the local clone to search for the fix)")
	fs.StringVar(&m.chain, "chain", "incidents", "Ledger chain to read incident records from, with --from-ledger")
}

func doMine(st store.Store, rec *ledger.Recorder, m mineFlags) ([]task.Task, error) {
	var tasks []task.Task
	var cands []mine.Candidate
	var err error
	switch {
	case m.fromGate != "":
		tasks, cands, err = mine.MineFromGate(m.repo, m.fromGate, mine.Options{Verify: !m.noVerify, MaxDiffLines: m.maxDiff,
			Test: testrun.Options{Timeout: m.timeout, DockerImage: m.docker}, Log: logf})
	case m.fromLedger != "":
		tasks, cands, err = mine.MineFromLedger(m.repo, m.fromLedger, os.Getenv("LEDGER_TOKEN"), m.chain, mine.Options{Verify: !m.noVerify, MaxDiffLines: m.maxDiff,
			Test: testrun.Options{Timeout: m.timeout, DockerImage: m.docker}, Log: logf})
	case m.github != "":
		owner, repo, ok := strings.Cut(m.github, "/")
		if !ok {
			return nil, fmt.Errorf("--github wants owner/repo, got %q", m.github)
		}
		local := m.repo
		if local == "." {
			local = "" // don't accidentally treat cwd as the clone unless the user actually pointed --repo at one
		}
		tasks, cands, err = mine.MineGitHub(context.Background(), mine.GitHubOptions{
			Owner: owner, Repo: repo, Token: os.Getenv("GITHUB_TOKEN"), LocalRepo: absRepo(local),
			MaxPRs: m.maxPRs, Verify: !m.noVerify, MaxDiffLines: m.maxDiff,
			Test: testrun.Options{Timeout: m.timeout, DockerImage: m.docker}, Log: logf,
		})
	default:
		tasks, cands, err = mine.Mine(m.repo, mine.Options{Since: m.since, MaxCommits: m.maxCommits, MaxDiffLines: m.maxDiff, Verify: !m.noVerify,
			Test: testrun.Options{Timeout: m.timeout, DockerImage: m.docker}, Log: logf})
	}
	if err != nil {
		return nil, err
	}
	added, err := st.SaveTasks(context.Background(), tasks)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if err := rec.Emit("bench.task.mined", t.ID, nil, map[string]any{"task_id": t.ID, "repo": t.Repo, "commit": t.Commit, "parent": t.Parent,
			"language": t.Language, "runner": t.Runner, "test_files": t.TestFiles, "diff_lines": t.DiffLines, "verified": t.Verified, "issue_refs": t.IssueRefs}); err != nil {
			logf("ledger: %v", err)
		}
	}
	fmt.Printf("scanned %d candidate commits, accepted %d tasks (%d new)\n", len(cands), len(tasks), added)
	return tasks, nil
}

func cmdMine(args []string) error {
	fs := flag.NewFlagSet("mine", flag.ExitOnError)
	var c common
	var m mineFlags
	c.bind(fs)
	bindMine(fs, &m)
	fs.Parse(args)
	st, err := c.open()
	if err != nil {
		return err
	}
	defer st.Close()
	rec := ledger.FromEnv()
	defer rec.Stop()
	_, err = doMine(st, rec, m)
	return err
}

func cmdTasks(args []string) error {
	fs := flag.NewFlagSet("tasks", flag.ExitOnError)
	var c common
	c.bind(fs)
	repo := fs.String("repo", "", "filter by repository")
	fs.Parse(args)
	st, err := c.open()
	if err != nil {
		return err
	}
	defer st.Close()
	ts, err := st.Tasks(context.Background(), absRepo(*repo))
	if err != nil {
		return err
	}
	for _, t := range ts {
		first := strings.SplitN(t.Prompt, "\n", 2)[0]
		fmt.Printf("%s\t%s\t%s\tverified=%v\tlines=%d\t%s\n", t.ID, t.Language, t.Runner, t.Verified, t.DiffLines, first)
	}
	return nil
}

type runFlags struct {
	name, model, repo, taskID, docker, agentDocker string
	gate, llm, agentDockerNetwork                  bool
	limit, trials                                  int
	timeout, agentTimeout                          time.Duration
}

func doRun(st store.Store, rec *ledger.Recorder, spec string, f runFlags) (task.Run, error) {
	a, err := harness.ParseAdapter(spec, harness.AdapterOptions{DockerImage: f.agentDocker, DockerNetwork: f.agentDockerNetwork})
	if err != nil {
		return task.Run{}, err
	}
	ts, err := st.Tasks(context.Background(), absRepo(f.repo))
	if err != nil {
		return task.Run{}, err
	}
	var sel []task.Task
	for _, t := range ts {
		if f.taskID != "" && t.ID != f.taskID {
			continue
		}
		sel = append(sel, t)
		if f.limit > 0 && len(sel) >= f.limit {
			break
		}
	}
	if len(sel) == 0 {
		return task.Run{}, fmt.Errorf("no tasks; run `bench mine` first")
	}
	name := f.name
	if name == "" {
		name = a.Name()
	}
	run := harness.Evaluate(a, sel, harness.Options{AgentName: name, Model: f.model, UseGate: f.gate, UseLLM: f.llm, AgentTimeout: f.agentTimeout, Trials: f.trials,
		Test: testrun.Options{Timeout: f.timeout, DockerImage: f.docker}, Log: logf})
	if err := st.SaveRun(context.Background(), run); err != nil {
		return run, err
	}
	agent := ledger.Actor{Kind: "agent", ID: name, Model: f.model}
	for _, r := range run.Results {
		p := map[string]any{"run_id": run.ID, "task_id": r.TaskID, "agent": name, "model": f.model, "passed": r.Passed, "failure_mode": r.FailureMode, "duration_ms": r.DurationMS}
		if r.RiskScore != nil {
			p["risk_score"] = *r.RiskScore
		}
		if err := rec.Emit("bench.run.scored", r.TaskID, []ledger.Actor{agent}, p); err != nil {
			logf("ledger: %v", err)
		}
	}
	fmt.Printf("run %s: %s %d/%d passed (%.1f%%)\n", run.ID, name, int(run.PassRate()*float64(len(run.Results))+0.5), len(run.Results), run.PassRate()*100)
	return run, nil
}

func bindRun(fs *flag.FlagSet, f *runFlags) {
	fs.StringVar(&f.name, "name", "", "agent display name (default: adapter name)")
	fs.StringVar(&f.model, "model", "", "model identifier to record")
	fs.StringVar(&f.taskID, "task", "", "run a single task id")
	fs.BoolVar(&f.gate, "gate", false, "score diffs with `gate` if on PATH")
	fs.BoolVar(&f.llm, "llm", false, "classify failures with Claude (needs ANTHROPIC_API_KEY)")
	fs.IntVar(&f.limit, "limit", 0, "max tasks")
	fs.IntVar(&f.trials, "trials", 1, "trials per task (for pass@k and variance)")
	fs.DurationVar(&f.agentTimeout, "agent-timeout", 30*time.Minute, "per task agent timeout")
	fs.StringVar(&f.agentDocker, "agent-docker", "", "run the agent itself inside this Docker image")
	fs.BoolVar(&f.agentDockerNetwork, "agent-docker-network", true, "allow network from --agent-docker (most agent CLIs need to reach their API)")
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var c common
	var f runFlags
	c.bind(fs)
	bindRun(fs, &f)
	agent := fs.String("agent", "", "adapter: gold, noop, or shell:<command>")
	fs.StringVar(&f.repo, "repo", "", "only tasks from this repository")
	fs.StringVar(&f.docker, "docker", "", "run tests in this Docker image")
	fs.DurationVar(&f.timeout, "test-timeout", 5*time.Minute, "per test invocation timeout")
	fs.Parse(args)
	if *agent == "" {
		return fmt.Errorf("--agent required")
	}
	st, err := c.open()
	if err != nil {
		return err
	}
	defer st.Close()
	rec := ledger.FromEnv()
	defer rec.Stop()
	_, err = doRun(st, rec, *agent, f)
	return err
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	var c common
	c.bind(fs)
	format := fs.String("format", "md", "md or html")
	out := fs.String("out", "", "output file (default stdout)")
	repo := fs.String("repo", "", "filter by repository")
	fs.Parse(args)
	st, err := c.open()
	if err != nil {
		return err
	}
	defer st.Close()
	runs, err := st.Runs(context.Background(), absRepo(*repo))
	if err != nil {
		return err
	}
	w := os.Stdout
	if *out != "" {
		fh, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer fh.Close()
		w = fh
	}
	es := report.Build(runs)
	if *format == "html" {
		return report.HTML(w, es)
	}
	report.Markdown(w, es)
	return nil
}

// latestRun returns the most recent run for spec ("agent" or "agent/model").
func latestRun(runs []task.Run, spec string) (task.Run, error) {
	agent, model, _ := strings.Cut(spec, "/")
	var best task.Run
	found := false
	for _, r := range runs {
		if r.Agent != agent || (model != "" && r.Model != model) {
			continue
		}
		if !found || r.StartedAt.After(best.StartedAt) {
			best, found = r, true
		}
	}
	if !found {
		return task.Run{}, fmt.Errorf("no run found for %q", spec)
	}
	return best, nil
}

func cmdCompare(args []string) error {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	var c common
	c.bind(fs)
	a := fs.String("a", "", "first agent, as \"agent\" or \"agent/model\" (uses its latest run)")
	b := fs.String("b", "", "second agent, as \"agent\" or \"agent/model\"")
	repo := fs.String("repo", "", "filter by repository")
	fs.Parse(args)
	if *a == "" || *b == "" {
		return fmt.Errorf("--a and --b required")
	}
	st, err := c.open()
	if err != nil {
		return err
	}
	defer st.Close()
	runs, err := st.Runs(context.Background(), absRepo(*repo))
	if err != nil {
		return err
	}
	ra, err := latestRun(runs, *a)
	if err != nil {
		return err
	}
	rb, err := latestRun(runs, *b)
	if err != nil {
		return err
	}
	cmp := report.Compare(ra, rb)
	fmt.Printf("%s/%s: %.1f%%   %s/%s: %.1f%%   (%d common tasks)\n", cmp.AgentA, cmp.ModelA, cmp.PassRateA*100, cmp.AgentB, cmp.ModelB, cmp.PassRateB*100, cmp.TasksCompared)
	fmt.Printf("McNemar: chi2=%.3f p=%.4f (A-only wins=%d, B-only wins=%d)\n", cmp.McNemarChi2, cmp.McNemarP, cmp.AOnly, cmp.BOnly)
	sig := "not significant"
	if cmp.Significant {
		sig = "SIGNIFICANT"
	}
	fmt.Printf("Paired bootstrap: diff=%+.1f pts, 95%% CI=[%+.1f, %+.1f] pts -- %s\n", cmp.DiffEstimate*100, cmp.DiffCILo*100, cmp.DiffCIHi*100, sig)
	return nil
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func cmdWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	var c common
	var m mineFlags
	var f runFlags
	var agents multi
	c.bind(fs)
	bindMine(fs, &m)
	bindRun(fs, &f)
	fs.Var(&agents, "agent", "adapter to run on new tasks (repeatable)")
	interval := fs.Duration("interval", time.Minute, "poll interval")
	once := fs.Bool("once", false, "check once and exit")
	fs.Parse(args)
	st, err := c.open()
	if err != nil {
		return err
	}
	defer st.Close()
	rec := ledger.FromEnv()
	defer rec.Stop()
	m.repo = absRepo(m.repo)
	f.repo, f.docker, f.timeout = m.repo, m.docker, m.timeout
	stateFile := filepath.Join(c.data, "watch-"+strings.ReplaceAll(strings.Trim(m.repo, "/"), "/", "_")+".head")
	last := m.since
	if b, err := os.ReadFile(stateFile); err == nil && last == "" {
		last = strings.TrimSpace(string(b))
	}
	for {
		head, err := gitx.Head(m.repo)
		if err != nil {
			return err
		}
		if head != last {
			logf("watch: new commits %s..%s", short(last), short(head))
			mm := m
			mm.since = last
			if _, err := doMine(st, rec, mm); err != nil {
				logf("watch: mine: %v", err)
			} else {
				for _, a := range agents {
					if _, err := doRun(st, rec, a, f); err != nil {
						logf("watch: run %s: %v", a, err)
					}
				}
				last = head
				os.MkdirAll(c.data, 0o755)
				os.WriteFile(stateFile, []byte(head), 0o644)
			}
		}
		if *once {
			return nil
		}
		time.Sleep(*interval)
	}
}

func short(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	if s == "" {
		return "(start)"
	}
	return s
}
