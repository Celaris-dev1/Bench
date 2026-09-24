package mine

import (
	"fmt"
	"strings"

	"github.com/Celaris-dev1/Bench/internal/gitx"
)

// findFix locates the first commit after badSHA (in --first-parent order, oldest first) that
// touches any of the given files, for turning a "something was wrong here" marker (a Gate
// rejection, a Ledger incident) into a task: the fix commit becomes the task's Commit, its git
// parent becomes Parent, and its message becomes the prompt (annotated by the caller). Returns
// ("", "", "", false) when badSHA can't be resolved in this repo or no later commit touches those
// files.
func findFix(abs, badSHA string, files []string) (sha, parent, msg string, ok bool) {
	if _, err := gitx.Run(abs, "cat-file", "-e", badSHA+"^{commit}"); err != nil {
		return "", "", "", false
	}
	args := []string{"log", "--no-merges", "--reverse", "--format=%H%x00%P%x00%s%x00%b%x1e", badSHA + "..HEAD"}
	if len(files) > 0 {
		args = append(args, "--")
		args = append(args, files...)
	}
	out, err := gitx.Run(abs, args...)
	if err != nil || strings.TrimSpace(out) == "" {
		return "", "", "", false
	}
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		p := strings.SplitN(rec, "\x00", 4)
		if len(p) < 4 || p[1] == "" {
			continue
		}
		parents := strings.Fields(p[1])
		if len(parents) != 1 {
			continue // skip merges/roots: we want a clean parent to diff against
		}
		return p[0], parents[0], strings.TrimSpace(p[2] + "\n\n" + p[3]), true
	}
	return "", "", "", false
}

// filesAt returns the files touched by sha (relative to its first parent), used to scope the
// search for a fix when the caller only has a bad commit and no explicit file list.
func filesAt(abs, sha string) []string {
	out, err := gitx.Run(abs, "show", "--no-commit-id", "--name-only", "--pretty=format:", sha)
	if err != nil {
		return nil
	}
	return strings.Fields(out)
}

// followupCandidate builds a Candidate for a fix that followed a bad commit, annotating the
// prompt with why the change is in the benchmark. It returns ok=false (not an error) when no
// later commit fixes badSHA in this repo -- callers should skip and log, not abort the mine.
func followupCandidate(abs, badSHA, note string, files []string, o Options) (Candidate, bool, error) {
	if len(files) == 0 {
		files = filesAt(abs, badSHA)
	}
	sha, parent, msg, ok := findFix(abs, badSHA, files)
	if !ok {
		return Candidate{}, false, nil
	}
	prompt := fmt.Sprintf("%s\n\n%s", note, msg)
	cand, ok, err := BuildCandidate(abs, sha, parent, prompt, o)
	return cand, ok, err
}
