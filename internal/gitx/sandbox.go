package gitx

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Celaris-dev1/Bench/internal/pathsafe"
)

// Sandbox is a leakage-isolated workspace for running an agent: a fresh,
// one-commit synthetic git repository built from a snapshot of a parent
// tree's content at a given revision. It shares no git objects, refs,
// reflogs or packs with the source repository (unlike a linked `git
// worktree`, which keeps the whole history reachable), so nothing committed
// after that revision -- in particular any fix commit an agent is being
// tested against -- can be recovered from inside it via `git log`, `git
// cat-file`, `git fsck --lost-found`, or by grepping .git.
//
// Held-out test files must be added to the sandbox only via InjectFiles,
// after an agent's turn has ended, using content read from outside the
// sandbox (ShowFile against the real repository). The sandbox itself is
// never told the fix commit's sha.
type Sandbox struct{ Dir string }

// Path returns the sandbox's working directory.
func (s *Sandbox) Path() string { return s.Dir }

// NewSandbox creates dir, exports the tree at rev from repo into it via
// `git archive` (content only -- no .git history travels with it), and
// commits that content as the sole commit of a brand-new repository.
func NewSandbox(repo, rev, dir string) (*Sandbox, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := exportTree(repo, rev, dir); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "bench@localhost"},
		{"config", "user.name", "bench"},
		{"config", "commit.gpgsign", "false"},
		{"add", "-A"},
		{"commit", "-q", "-m", "workspace", "--allow-empty"},
	} {
		if _, err := Run(dir, args...); err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("sandbox init: %w", err)
		}
	}
	return &Sandbox{Dir: dir}, nil
}

// exportTree writes repo's tree at rev into dir using `git archive`, read
// entirely into memory and extracted by Go's archive/tar rather than shelling
// out to `tar`, so every entry's destination path is validated with
// pathsafe before anything is written, and symlinks/hardlinks (which could
// otherwise point outside dir) are dropped.
func exportTree(repo, rev, dir string) error {
	cmd := exec.Command("git", "archive", "--format=tar", rev)
	cmd.Dir = repo
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git archive %s: %w: %s", rev, err, strings.TrimSpace(errb.String()))
	}
	tr := tar.NewReader(&out)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			continue // never follow/create links out of the sandbox
		}
		target, perr := pathsafe.Join(dir, hdr.Name)
		if perr != nil {
			continue // defense in depth; git archive should never emit these
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode) & 0o777
			if mode == 0 {
				mode = 0o644
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.CopyN(f, tr, hdr.Size); err != nil && err != io.EOF {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

// Remove deletes the sandbox. Unlike a linked worktree it owns its own
// .git, so a plain recursive delete is correct and sufficient.
func (s *Sandbox) Remove() { os.RemoveAll(s.Dir) }

// Diff returns the sandbox's working-tree diff against its single base
// commit, excluding the given paths. This never touches the source repo.
func (s *Sandbox) Diff(exclude []string) (string, error) {
	if _, err := Run(s.Dir, "add", "-A"); err != nil {
		return "", err
	}
	args := []string{"diff", "--cached", "HEAD", "--", "."}
	for _, e := range exclude {
		args = append(args, ":(exclude)"+e)
	}
	out, err := Run(s.Dir, args...)
	_, _ = Run(s.Dir, "reset", "-q")
	return out, err
}

// Apply applies a patch to the sandbox's working tree.
func (s *Sandbox) Apply(patch string) error {
	if strings.TrimSpace(patch) == "" {
		return nil
	}
	_, err := RunInput(s.Dir, patch, "apply", "--whitespace=nowarn", "-")
	return err
}

// ResetToBase discards all working-tree changes, returning the sandbox to
// its single base commit.
func (s *Sandbox) ResetToBase() error {
	if _, err := Run(s.Dir, "reset", "-q", "--hard", "HEAD"); err != nil {
		return err
	}
	_, err := Run(s.Dir, "clean", "-fdq")
	return err
}

// InjectFiles writes held-out file content directly into the sandbox's
// working tree -- no git object access is used, so this is the only way
// test files reach a Sandbox. Callers read that content from outside the
// sandbox (typically ShowFile against the real repository) and pass it
// here only once an agent's turn has ended. A nil value for a path deletes
// that path (used for files that did not exist before the fix). Every path
// is validated with pathsafe to ensure it cannot escape the sandbox.
func (s *Sandbox) InjectFiles(files map[string][]byte) error {
	for rel, content := range files {
		target, err := pathsafe.Join(s.Dir, rel)
		if err != nil {
			return fmt.Errorf("inject %s: %w", rel, err)
		}
		if content == nil {
			os.Remove(target)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			return err
		}
	}
	return nil
}
