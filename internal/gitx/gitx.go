// Package gitx wraps the git CLI.
package gitx

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Run executes git in dir and returns trimmed stdout.
func Run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// RunInput executes git with stdin.
func RunInput(dir, stdin string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Head returns the HEAD sha.
func Head(dir string) (string, error) {
	s, err := Run(dir, "rev-parse", "HEAD")
	return strings.TrimSpace(s), err
}

// Worktree is a detached checkout of a revision.
type Worktree struct {
	Repo, Dir string
}

// AddWorktree creates a detached worktree at rev in dir.
func AddWorktree(repo, dir, rev string) (*Worktree, error) {
	if _, err := Run(repo, "worktree", "add", "--detach", "--force", dir, rev); err != nil {
		return nil, err
	}
	return &Worktree{Repo: repo, Dir: dir}, nil
}

// Remove deletes the worktree.
func (w *Worktree) Remove() {
	_, _ = Run(w.Repo, "worktree", "remove", "--force", w.Dir)
	_, _ = Run(w.Repo, "worktree", "prune")
}

// CheckoutFiles writes files as of rev into the worktree; files deleted at rev are removed.
func (w *Worktree) CheckoutFiles(rev string, files []string) error {
	for _, f := range files {
		if _, err := Run(w.Dir, "cat-file", "-e", rev+":"+f); err != nil {
			_, _ = Run(w.Dir, "rm", "-f", "-q", "--", f)
			continue
		}
		if _, err := Run(w.Dir, "checkout", rev, "--", f); err != nil {
			return err
		}
	}
	return nil
}

// Diff returns the working-tree diff (including untracked files) against HEAD, excluding given paths.
func (w *Worktree) Diff(exclude []string) (string, error) {
	if _, err := Run(w.Dir, "add", "-A"); err != nil {
		return "", err
	}
	args := []string{"diff", "--cached", "HEAD", "--", "."}
	for _, e := range exclude {
		args = append(args, ":(exclude)"+e)
	}
	out, err := Run(w.Dir, args...)
	_, _ = Run(w.Dir, "reset", "-q")
	return out, err
}

// Apply applies a patch to the worktree.
func (w *Worktree) Apply(patch string) error {
	if strings.TrimSpace(patch) == "" {
		return nil
	}
	_, err := RunInput(w.Dir, patch, "apply", "--whitespace=nowarn", "-")
	return err
}
