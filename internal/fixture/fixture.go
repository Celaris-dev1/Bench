// Package fixture builds tiny git repositories for tests.
package fixture

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Repo creates a Go repo with history:
//  1. initial buggy Sum (no tests)
//  2. "fix off-by-one in Sum, fixes #1" — fixes source and adds sum_test.go  → task
//  3. "docs: readme" — no tests                                              → skipped
//  4. "fix: Max ignores negatives (closes #2)" — test already passes before  → rejected by verification
func Repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("go.mod", "module example.com/calc\n\ngo 1.21\n")
	write("calc/sum.go", "package calc\n\n// Sum adds 1..n.\nfunc Sum(n int) int {\n\ts := 0\n\tfor i := 1; i < n; i++ {\n\t\ts += i\n\t}\n\treturn s\n}\n\nfunc Max(a, b int) int {\n\tif a > b {\n\t\treturn a\n\t}\n\treturn b\n}\n")
	git("add", "-A")
	git("commit", "-qm", "initial calc")

	write("calc/sum.go", "package calc\n\n// Sum adds 1..n.\nfunc Sum(n int) int {\n\ts := 0\n\tfor i := 1; i <= n; i++ {\n\t\ts += i\n\t}\n\treturn s\n}\n\nfunc Max(a, b int) int {\n\tif a > b {\n\t\treturn a\n\t}\n\treturn b\n}\n")
	write("calc/sum_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestSum(t *testing.T) {\n\tif got := Sum(3); got != 6 {\n\t\tt.Fatalf(\"Sum(3) got %d want %d\", got, 6)\n\t}\n}\n")
	git("add", "-A")
	git("commit", "-qm", "Fix off-by-one in Sum\n\nSum(n) should include n itself. Fixes #1")

	write("README.md", "calc\n")
	git("add", "-A")
	git("commit", "-qm", "docs: readme")

	write("calc/sum.go", "package calc\n\n// Sum adds 1..n.\nfunc Sum(n int) int {\n\ts := 0\n\tfor i := 1; i <= n; i++ {\n\t\ts += i\n\t}\n\treturn s\n}\n\n// Max returns the larger value.\nfunc Max(a, b int) int {\n\tif a > b {\n\t\treturn a\n\t}\n\treturn b\n}\n")
	write("calc/max_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestMax(t *testing.T) {\n\tif Max(-1, -2) != -1 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")
	git("add", "-A")
	git("commit", "-qm", "fix: Max ignores negatives (closes #2)")
	return dir
}

// FixedSum is the correct sum.go body, handy for shell-adapter tests.
const FixedSum = "package calc\n\nfunc Sum(n int) int {\n\treturn n * (n + 1) / 2\n}\n\nfunc Max(a, b int) int {\n\tif a > b {\n\t\treturn a\n\t}\n\treturn b\n}\n"
