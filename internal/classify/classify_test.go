package classify

import (
	"os"
	"testing"
)

func TestHeuristic(t *testing.T) {
	hi := 0.9
	cases := []struct {
		in   Input
		want string
	}{
		{Input{AgentDiff: ""}, NoChange},
		{Input{AgentDiff: "x", TimedOut: true}, Timeout},
		{Input{AgentDiff: "x", AgentErr: "boom"}, AgentError},
		{Input{AgentDiff: "x", Risk: &hi}, SecurityRegr},
		{Input{AgentDiff: "x", Output: "./a.go:3: undefined: Foo"}, WrongAPI},
		{Input{AgentDiff: "x", Output: "syntax error: unexpected }"}, BuildFailure},
		{Input{AgentDiff: "x", Output: "panic: runtime error: index out of range [3]"}, MissedEdgeCase},
		{Input{AgentDiff: "x", Output: "Sum(3) got 5 want 6"}, OffByOne},
		{Input{AgentDiff: "x", Output: "expected 6, received 7"}, OffByOne},
		{Input{AgentDiff: "x", Output: "Sum(3) got 1 want 6"}, WrongLogic},
	}
	for _, c := range cases {
		if got := Heuristic(c.in); got != c.want {
			t.Errorf("%+v: got %s want %s", c.in, got, c.want)
		}
	}
}

func TestLLMSkipsWithoutKey(t *testing.T) {
	os.Unsetenv("ANTHROPIC_API_KEY")
	if m, note := LLM(Input{}); m != "" || note != "skipped: no API key" {
		t.Fatal(m, note)
	}
	if Classify(Input{}, true) != NoChange {
		t.Fatal("fallback")
	}
}
