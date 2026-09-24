// Package mine walks git history and turns bug-fix commits with tests into tasks.
package mine

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Celaris-dev1/Bench/internal/detect"
	"github.com/Celaris-dev1/Bench/internal/gitx"
	"github.com/Celaris-dev1/Bench/internal/pathsafe"
	"github.com/Celaris-dev1/Bench/internal/task"
	"github.com/Celaris-dev1/Bench/internal/testrun"
)

// Options configures mining.
type Options struct {
	Since        string // only commits after this sha (exclusive)
	MaxCommits   int
	MaxDiffLines int
	Verify       bool
	Test         testrun.Options
	Log          func(format string, a ...any)
}

// maxRemoteResponseBytes caps any single HTTP response body or on-disk report file read while
// mining from an external source (GitHub, Gate, Ledger): all three are outside Bench's control
// and a malicious or misbehaving one sending gigabytes of JSON should fail cleanly, not exhaust
// memory.
const maxRemoteResponseBytes = 32 << 20 // 32MiB

var (
	issueRefRe = regexp.MustCompile(`(?i)\b(?:fix(?:e[sd])?|close[sd]?|resolve[sd]?)\s*:?\s*((?:[\w.-]+/[\w.-]+)?#\d+)`)
	hashRefRe  = regexp.MustCompile(`#\d+`)
	bugWordRe  = regexp.MustCompile(`(?i)\b(fix(e[sd])?|bug|close[sd]?|resolve[sd]?|regression|crash|broken)\b`)
	riskyRe    = regexp.MustCompile(`(?i)(https?://[a-z0-9]|http\.Get\(|requests\.(get|post)|\bfetch\(|net\.Dial|socket\.|api[_-]?key|secret|password|aws_|BEGIN (RSA|OPENSSH) PRIVATE|os\.Getenv\("[A-Z_]*(TOKEN|KEY)|rand\.(Seed|Int)|time\.Now\(\)\.Unix|Math\.random)`)
)

// Candidate is a commit that passed the static filters, with the reason when rejected.
type Candidate struct {
	Task   task.Task
	Reject string
}

type commit struct{ sha, parent, subject, body string }

func listCommits(repo string, o Options) ([]commit, error) {
	args := []string{"log", "--no-merges", "--format=%H%x00%P%x00%s%x00%b%x1e"}
	if o.Since != "" {
		args = append(args, o.Since+"..HEAD")
	}
	if o.MaxCommits > 0 {
		args = append(args, fmt.Sprintf("-n%d", o.MaxCommits))
	}
	out, err := gitx.Run(repo, args...)
	if err != nil {
		return nil, err
	}
	var cs []commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		p := strings.SplitN(rec, "\x00", 4)
		if len(p) < 4 || p[1] == "" {
			continue // root commit or empty
		}
		cs = append(cs, commit{p[0], strings.Fields(p[1])[0], p[2], strings.TrimSpace(p[3])})
	}
	return cs, nil
}

// Mine returns accepted tasks and all candidates (with rejection reasons).
func Mine(repo string, o Options) ([]task.Task, []Candidate, error) {
	if o.MaxDiffLines == 0 {
		o.MaxDiffLines = 400
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return nil, nil, err
	}
	cs, err := listCommits(abs, o)
	if err != nil {
		return nil, nil, err
	}
	var tasks []task.Task
	var cands []Candidate
	for _, c := range cs {
		msg := strings.TrimSpace(c.subject + "\n\n" + c.body)
		if !bugWordRe.MatchString(msg) && !issueRefRe.MatchString(msg) {
			continue
		}
		cand, ok, err := BuildCandidate(abs, c.sha, c.parent, msg, o)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		cands = append(cands, cand)
		if cand.Reject == "" {
			tasks = append(tasks, cand.Task)
			o.Log("accepted %s", cand.Task.ID)
		} else {
			o.Log("rejected %s: %s", cand.Task.ID, cand.Reject)
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].Score > tasks[j].Score })
	return tasks, cands, nil
}

// BuildCandidate builds a Candidate task from a specific commit range (parent..sha) with the given
// prompt text, running the same static-filter and (if o.Verify) fail-before/pass-after checks as
// Mine's own commit walk. ok is false when the commit touches no held-out tests or no source files
// (not a rejection worth reporting — just not shaped like a task). abs must be an absolute repo path.
func BuildCandidate(abs, sha, parent, msg string, o Options) (Candidate, bool, error) {
	out, err := gitx.Run(abs, "diff-tree", "--no-commit-id", "--name-only", "-r", parent, sha)
	if err != nil {
		return Candidate{}, false, err
	}
	var tests, srcs []string
	for _, f := range strings.Fields(out) {
		// Paths come straight from a commit's own tree, but a task's TestFiles/SourceFiles are
		// later used (e.g. gitx.Worktree.CheckoutFiles, dockerx sandbox file injection) as
		// relative paths into a fresh worktree/sandbox; reject anything that wouldn't stay
		// inside it rather than trust every commit a repo's history ever contained.
		if !pathsafe.Check(f) {
			continue
		}
		switch {
		case detect.IsTest(f):
			tests = append(tests, f)
		case detect.IsSource(f):
			srcs = append(srcs, f)
		}
	}
	if len(tests) == 0 || len(srcs) == 0 {
		return Candidate{}, false, nil
	}
	t := task.Task{
		ID: fmt.Sprintf("%s-%s", filepath.Base(abs), shortSHA(sha)), Repo: abs, Commit: sha, Parent: parent,
		Prompt: msg, IssueRefs: refs(msg), Language: detect.Language(srcs), Runner: detect.Runner(abs, detect.Language(srcs)), TestFiles: tests, SourceFiles: srcs,
		MinedAt: time.Now().UTC(),
	}
	t.GoldDiff, _ = gitx.Run(abs, append([]string{"diff", "--binary", parent, sha, "--"}, srcs...)...)
	t.TestDiff, _ = gitx.Run(abs, append([]string{"diff", "--binary", parent, sha, "--"}, tests...)...)
	t.DiffLines = countChanged(t.GoldDiff)
	t.Score = score(t)
	cand := Candidate{Task: t, Reject: staticReject(t, o)}
	if cand.Reject == "" && o.Verify {
		o.Log("verifying %s", t.ID)
		cand.Reject = verify(abs, &cand.Task, o)
	}
	return cand, true, nil
}

func shortSHA(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

func refs(msg string) []string {
	var r []string
	seen := map[string]bool{}
	for _, m := range hashRefRe.FindAllString(msg, -1) {
		if !seen[m] {
			seen[m] = true
			r = append(r, m)
		}
	}
	return r
}

func countChanged(diff string) int {
	n := 0
	for _, l := range strings.Split(diff, "\n") {
		if (strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++")) || (strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---")) {
			n++
		}
	}
	return n
}

// score ranks tasks: clearer descriptions and smaller diffs rank higher.
func score(t task.Task) float64 {
	words := len(strings.Fields(t.Prompt))
	clarity := float64(words)
	if clarity > 60 {
		clarity = 60
	}
	s := clarity / 60
	if len(t.IssueRefs) > 0 {
		s += 0.5
	}
	s += 1.0 / (1.0 + float64(t.DiffLines)/20.0)
	return s
}

func staticReject(t task.Task, o Options) string {
	if t.DiffLines == 0 {
		return "empty source diff"
	}
	if t.DiffLines > o.MaxDiffLines {
		return fmt.Sprintf("diff too large (%d > %d lines)", t.DiffLines, o.MaxDiffLines)
	}
	if len(strings.Fields(t.Prompt)) < 3 {
		return "description too short"
	}
	for _, d := range []string{t.GoldDiff, t.TestDiff} {
		for _, l := range strings.Split(d, "\n") {
			if strings.HasPrefix(l, "+") && riskyRe.MatchString(l) {
				return "network/secret/non-determinism hint: " + strings.TrimSpace(strings.TrimPrefix(l, "+"))
			}
		}
	}
	return ""
}

// verify checks tests fail at parent+tests and pass at commit, in a throwaway worktree.
func verify(repo string, t *task.Task, o Options) string {
	dir, err := os.MkdirTemp("", "bench-verify-")
	if err != nil {
		return err.Error()
	}
	os.Remove(dir)
	wt, err := gitx.AddWorktree(repo, dir, t.Parent)
	if err != nil {
		return "worktree: " + err.Error()
	}
	defer wt.Remove()
	t.Runner = detect.Runner(dir, t.Language)
	argv := detect.Command(t.Runner, t.TestFiles)
	if argv == nil {
		return "no test runner detected"
	}
	if err := wt.CheckoutFiles(t.Commit, t.TestFiles); err != nil {
		return "checkout tests: " + err.Error()
	}
	if pre := testrun.Run(dir, argv, o.Test); pre.Passed {
		return "tests already pass before fix"
	}
	if err := wt.Apply(t.GoldDiff); err != nil {
		return "apply gold diff: " + err.Error()
	}
	if post := testrun.Run(dir, argv, o.Test); !post.Passed {
		return "tests fail after fix (flaky or environment-dependent)"
	}
	t.Verified = true
	return ""
}
