package mine

// Fuzz targets over the parsers that see untrusted input directly: commit messages (and their
// issue references) from arbitrary git history, and diff text used only to count changed lines.
// Neither should ever panic, however malformed the input -- a hostile commit message or a
// deliberately weird diff is exactly what `bench mine` is pointed at.

import "testing"

func FuzzRefs(f *testing.F) {
	for _, s := range []string{
		"Fixes #1", "fix: off by one (closes #42)", "", "###", "#", strings500(),
		"fixes acme/repo#7 and closes #8", "\xff\xfe not utf8 #3",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, msg string) {
		r := refs(msg)
		for _, ref := range r {
			if ref == "" {
				t.Fatalf("refs(%q) produced an empty ref", msg)
			}
		}
		// issueRefRe / bugWordRe / hashRefRe must never panic or hang on adversarial input either.
		_ = issueRefRe.MatchString(msg)
		_ = bugWordRe.MatchString(msg)
	})
}

func FuzzCountChanged(f *testing.F) {
	for _, s := range []string{
		"", "+a\n-b\n", "+++ x\n--- y\n+z\n", "no newline at end", strings500(),
		"+\n" + strings500(),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, diff string) {
		if n := countChanged(diff); n < 0 {
			t.Fatalf("countChanged(%q) = %d, want >= 0", diff, n)
		}
	})
}

func strings500() string {
	b := make([]byte, 500)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return string(b)
}
