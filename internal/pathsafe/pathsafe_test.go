package pathsafe

import (
	"path/filepath"
	"testing"
)

func TestJoin(t *testing.T) {
	base := t.TempDir()
	cases := []struct {
		rel string
		ok  bool
	}{
		{"a/b.go", true},
		{"a/b/../c.go", true},
		{"./a.go", true},
		{"", false},
		{"/etc/passwd", false},
		{"..", false},
		{"../x", false},
		{"a/../../x", false},
		{"a/../..", false},
		{"a/b/../../../etc/passwd", false},
	}
	for _, c := range cases {
		got, err := Join(base, c.rel)
		if c.ok && err != nil {
			t.Errorf("Join(%q) = err %v, want ok", c.rel, err)
		}
		if !c.ok && err == nil {
			t.Errorf("Join(%q) = %q, want error", c.rel, got)
		}
		if err == nil {
			baseAbs, _ := filepath.Abs(base)
			if got != baseAbs && len(got) <= len(baseAbs) {
				t.Errorf("Join(%q) = %q escapes base %q", c.rel, got, baseAbs)
			}
		}
	}
}

func TestCheck(t *testing.T) {
	if !Check("a/b.go") {
		t.Fatal("expected safe")
	}
	if Check("../x") {
		t.Fatal("expected unsafe")
	}
	if Check("/abs") {
		t.Fatal("expected unsafe")
	}
}
