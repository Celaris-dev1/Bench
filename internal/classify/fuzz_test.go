package classify

// Fuzzes the heuristic classifier's regex parsing of test-runner output, which is arbitrary text
// from whatever language/runner a task uses (and, when run against an agent's own diff, output
// the agent effectively controls).

import "testing"

func FuzzHeuristic(f *testing.F) {
	seeds := []string{
		"", "panic: runtime error: index out of range [3] with length 2",
		"got 4, want 5", "AttributeError: 'NoneType' object has no attribute 'x'",
		"error[E0432]: unresolved import", "\x00\x01\x02 binary noise",
		"got -1 expected 0 got 2 want 2 got 3 want 4",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, out string) {
		mode := Heuristic(Input{Output: out})
		found := false
		for _, m := range Modes {
			if m == mode {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Heuristic returned unknown mode %q for output %q", mode, out)
		}
	})
}
