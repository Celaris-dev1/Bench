package gitx_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Celaris-dev1/Bench/internal/fixture"
	"github.com/Celaris-dev1/Bench/internal/gitx"
)

// fixCommit returns the sha and parent sha of the fixture's "Fix off-by-one"
// commit, plus a distinctive line from the gold diff to grep for.
func fixCommit(t *testing.T, repo string) (sha, parent, distinctive string) {
	t.Helper()
	out, err := gitx.Run(repo, "log", "--format=%H%x00%P%x00%s")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		p := strings.SplitN(line, "\x00", 3)
		if len(p) == 3 && strings.Contains(p[2], "off-by-one") {
			diff, _ := gitx.Run(repo, "show", p[0])
			return p[0], p[1], strings.TrimSpace(strings.SplitN(p[2], "\n", 2)[0]) + "|" + firstAddedLine(diff)
		}
	}
	t.Fatal("fix commit not found in fixture")
	return
}

func firstAddedLine(diff string) string {
	for _, l := range strings.Split(diff, "\n") {
		if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") && strings.TrimSpace(l) != "+" {
			return strings.TrimSpace(strings.TrimPrefix(l, "+"))
		}
	}
	return ""
}

func TestSandboxIsolation(t *testing.T) {
	repo := fixture.Repo(t)
	fixSha, parentSha, marker := fixCommit(t, repo)
	distinctive := strings.SplitN(marker, "|", 2)[1] // e.g. "for i := 1; i <= n; i++ {"

	dir := filepath.Join(t.TempDir(), "ws")
	sb, err := gitx.NewSandbox(repo, parentSha, dir)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	defer sb.Remove()

	// 1. git log inside the sandbox shows exactly one commit, never the fix.
	log, err := gitx.Run(sb.Dir, "log", "--format=%H")
	if err != nil {
		t.Fatalf("git log in sandbox: %v", err)
	}
	shas := strings.Fields(log)
	if len(shas) != 1 {
		t.Fatalf("expected exactly 1 commit in sandbox, got %d: %v", len(shas), shas)
	}
	for _, s := range shas {
		if s == fixSha {
			t.Fatalf("sandbox log contains the fix commit %s", fixSha)
		}
	}

	// 2. git cat-file cannot resolve the fix commit's sha from inside the sandbox.
	if _, err := gitx.Run(sb.Dir, "cat-file", "-e", fixSha); err == nil {
		t.Fatalf("git cat-file -e %s unexpectedly succeeded inside sandbox", fixSha)
	}

	// 3. git fsck --unreachable --lost-found finds nothing (no dangling
	// objects reachable at all -- the object store only has the one commit
	// the sandbox itself made).
	fsckOut, _ := gitx.Run(sb.Dir, "fsck", "--full", "--unreachable", "--lost-found")
	if strings.Contains(fsckOut, fixSha) {
		t.Fatalf("git fsck surfaced the fix commit: %s", fsckOut)
	}

	// 4. grepping .git for content unique to the gold diff finds nothing --
	// the fix's blob was never fetched into this object store.
	found := false
	filepath.Walk(filepath.Join(sb.Dir, ".git"), func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), distinctive) {
			found = true
		}
		return nil
	})
	if found {
		t.Fatalf("found gold-diff content %q inside sandbox .git", distinctive)
	}

	// 5. Held-out test content is fetched from OUTSIDE the sandbox (the real
	// repo, at the fix commit) and only then injected -- never checked out
	// via a git ref the sandbox itself could reach.
	content, ok, err := gitx.ShowFile(repo, fixSha, "calc/sum_test.go")
	if err != nil || !ok {
		t.Fatalf("ShowFile: %v ok=%v", err, ok)
	}
	if err := sb.InjectFiles(map[string][]byte{"calc/sum_test.go": content}); err != nil {
		t.Fatalf("InjectFiles: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(sb.Dir, "calc/sum_test.go"))
	if err != nil || string(got) != string(content) {
		t.Fatalf("injected file mismatch: %v", err)
	}
	// The sandbox's own git history still doesn't know the fix commit even
	// after injection (injection is a plain file write, not a git operation).
	log2, _ := gitx.Run(sb.Dir, "log", "--format=%H")
	if len(strings.Fields(log2)) != 1 {
		t.Fatalf("injection changed sandbox history: %v", log2)
	}

	// 6. Path traversal in a held-out file path is refused, not written
	// outside the sandbox.
	if err := sb.InjectFiles(map[string][]byte{"../../etc/evil": []byte("x")}); err == nil {
		t.Fatal("expected traversal path to be refused")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(sb.Dir)), "etc", "evil")); err == nil {
		t.Fatal("traversal path was written outside the sandbox")
	}
}

func TestSandboxResetAndDiff(t *testing.T) {
	repo := fixture.Repo(t)
	_, parentSha, _ := fixCommit(t, repo)
	sb, err := gitx.NewSandbox(repo, parentSha, filepath.Join(t.TempDir(), "ws"))
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Remove()

	if err := os.WriteFile(filepath.Join(sb.Dir, "calc", "sum.go"), []byte("package calc\n// edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff, err := sb.Diff(nil)
	if err != nil || !strings.Contains(diff, "edited") {
		t.Fatalf("Diff: %v %q", err, diff)
	}
	if err := sb.ResetToBase(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(sb.Dir, "calc", "sum.go"))
	if strings.Contains(string(b), "edited") {
		t.Fatal("ResetToBase did not discard changes")
	}
}
