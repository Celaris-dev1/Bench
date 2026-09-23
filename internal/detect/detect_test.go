package detect

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestIsTest(t *testing.T) {
	for p, want := range map[string]bool{"a/b_test.go": true, "tests/test_x.py": true, "x_test.py": true, "src/a.test.ts": true, "a.spec.jsx": true,
		"src/__tests__/a.js": true, "tests/it.rs": true, "a.go": false, "src/lib.rs": false, "testdata.py": false} {
		if IsTest(p) != want {
			t.Errorf("%s", p)
		}
	}
}

func TestRunner(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "package.json"), []byte(`{"devDependencies":{"vitest":"1"}}`), 0o644)
	if Runner(d, "typescript") != "vitest" || Runner(d, "") != "jest" || Runner(d, "python") != "pytest" || Runner(d, "rust") != "cargo" {
		t.Fatal("runner")
	}
	if Language([]string{"a.py", "b.py", "c.go"}) != "python" {
		t.Fatal("lang")
	}
	if got := Command("go", []string{"a/x_test.go", "a/y_test.go", "b/z_test.go"}); !reflect.DeepEqual(got, []string{"go", "test", "-count=1", "./a", "./b"}) {
		t.Fatal(got)
	}
}
