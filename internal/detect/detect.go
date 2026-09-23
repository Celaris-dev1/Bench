// Package detect identifies test files, languages and test runners.
package detect

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var testRe = []*regexp.Regexp{
	regexp.MustCompile(`_test\.go$`),
	regexp.MustCompile(`(^|/)test_[^/]*\.py$`),
	regexp.MustCompile(`_test\.py$`),
	regexp.MustCompile(`\.(test|spec)\.[cm]?[jt]sx?$`),
	regexp.MustCompile(`(^|/)__tests__/`),
	regexp.MustCompile(`(^|/)tests/[^/]*\.rs$`),
}

// IsTest reports whether path looks like a test file.
func IsTest(path string) bool {
	for _, r := range testRe {
		if r.MatchString(path) {
			return true
		}
	}
	return false
}

var srcExt = map[string]string{".go": "go", ".py": "python", ".js": "javascript", ".jsx": "javascript", ".mjs": "javascript", ".cjs": "javascript", ".ts": "typescript", ".tsx": "typescript", ".rs": "rust"}

// IsSource reports whether path is a recognised source file.
func IsSource(path string) bool { _, ok := srcExt[filepath.Ext(path)]; return ok }

// Language returns the dominant language among files.
func Language(files []string) string {
	c := map[string]int{}
	for _, f := range files {
		if l, ok := srcExt[filepath.Ext(f)]; ok {
			c[l]++
		}
	}
	best, n := "", 0
	for l, k := range c {
		if k > n || (k == n && l < best) {
			best, n = l, k
		}
	}
	return best
}

// Runner picks a test runner for a checkout directory and language.
func Runner(dir, lang string) string {
	exists := func(p string) bool { _, err := os.Stat(filepath.Join(dir, p)); return err == nil }
	switch lang {
	case "go":
		return "go"
	case "rust":
		return "cargo"
	case "python":
		return "pytest"
	case "javascript", "typescript":
		b, _ := os.ReadFile(filepath.Join(dir, "package.json"))
		if strings.Contains(string(b), "vitest") || exists("vitest.config.ts") || exists("vitest.config.js") {
			return "vitest"
		}
		return "jest"
	}
	switch {
	case exists("go.mod"):
		return "go"
	case exists("Cargo.toml"):
		return "cargo"
	case exists("package.json"):
		return "jest"
	case exists("pyproject.toml"), exists("setup.py"), exists("pytest.ini"):
		return "pytest"
	}
	return ""
}

// Command returns the argv to run the given test files with runner.
func Command(runner string, tests []string) []string {
	switch runner {
	case "go":
		set := map[string]bool{}
		for _, t := range tests {
			set["./"+filepath.ToSlash(filepath.Dir(t))] = true
		}
		pkgs := make([]string, 0, len(set))
		for p := range set {
			pkgs = append(pkgs, p)
		}
		sort.Strings(pkgs)
		return append([]string{"go", "test", "-count=1"}, pkgs...)
	case "pytest":
		return append([]string{"python3", "-m", "pytest", "-q"}, tests...)
	case "jest":
		return append([]string{"npx", "--no-install", "jest"}, tests...)
	case "vitest":
		return append([]string{"npx", "--no-install", "vitest", "run"}, tests...)
	case "cargo":
		return []string{"cargo", "test", "--quiet"}
	}
	return nil
}
