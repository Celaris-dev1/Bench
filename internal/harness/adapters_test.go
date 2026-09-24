package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Celaris-dev1/Bench/internal/task"
)

// fakeBinary writes a shell script called name onto a fresh PATH-only
// directory that also logs every argv it was called with (one JSON array
// per line) to logFile, and returns that directory plus a func to read the
// log. It never invokes a real agent binary or network call.
func fakeBinary(t *testing.T, name, body string) (dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake shell binaries assume a POSIX shell")
	}
	dir = t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func withPATH(t *testing.T, dir string) {
	t.Helper()
	old := os.Getenv("PATH")
	os.Setenv("PATH", dir+string(os.PathListSeparator)+old)
	t.Cleanup(func() { os.Setenv("PATH", old) })
}

type fakeWorkspace struct{ dir string }

func (w fakeWorkspace) Path() string       { return w.dir }
func (w fakeWorkspace) Apply(string) error { return nil }

func TestCommandAdapterCapturesTranscriptAndUsage(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "argv.log")
	dir := fakeBinary(t, "fake-agent", `
echo "$@" >> `+logFile+`
cat <<'EOF'
{"result":"done","usage":{"input_tokens":120,"output_tokens":45},"total_cost_usd":0.0031}
EOF
`)
	withPATH(t, dir)

	ca := CommandAdapter{
		AgentName: "fake",
		Usage:     genericUsage,
		Build: func(in BuildInput) ([]string, []string, error) {
			return []string{"fake-agent", "-p", in.Task.Prompt}, nil, nil
		},
	}
	ws := fakeWorkspace{dir: t.TempDir()}
	info, err := ca.Solve(context.Background(), ws, task.Task{ID: "t1", Prompt: "fix the bug"})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if !strings.Contains(info.Transcript, "done") {
		t.Fatalf("transcript missing output: %q", info.Transcript)
	}
	if info.TokensIn != 120 || info.TokensOut != 45 {
		t.Fatalf("usage not parsed: %+v", info)
	}
	if info.CostUSD == nil || *info.CostUSD != 0.0031 {
		t.Fatalf("cost not parsed: %+v", info)
	}
	logged, err := os.ReadFile(logFile)
	if err != nil || !strings.Contains(string(logged), "fix the bug") {
		t.Fatalf("prompt not passed to CLI: %v %q", err, logged)
	}
}

func TestCommandAdapterMissingBinaryIsClearError(t *testing.T) {
	withPATH(t, t.TempDir()) // empty PATH override, "definitely-not-a-real-binary" cannot be found
	ca := CommandAdapter{
		AgentName: "ghost",
		Build: func(in BuildInput) ([]string, []string, error) {
			return []string{"definitely-not-a-real-binary-xyz"}, nil, nil
		},
	}
	_, err := ca.Solve(context.Background(), fakeWorkspace{dir: t.TempDir()}, task.Task{})
	if err == nil {
		t.Fatal("expected an error for a missing binary")
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("expected a clear PATH-related error, got: %v", err)
	}
}

func TestGenericUsageParsesVariousKeyNames(t *testing.T) {
	tin, tout, cost := genericUsage(`{"prompt_tokens":10,"completion_tokens":20,"cost_usd":1.5}`)
	if tin != 10 || tout != 20 || cost == nil || *cost != 1.5 {
		t.Fatalf("got %d %d %v", tin, tout, cost)
	}
	tin, tout, cost = genericUsage("no usage info here")
	if tin != 0 || tout != 0 || cost != nil {
		t.Fatalf("expected zero values, got %d %d %v", tin, tout, cost)
	}
}

func TestBuiltinAgentBuilders(t *testing.T) {
	in := BuildInput{Task: task.Task{Prompt: "do the thing"}, WorkspaceDir: "/ws", PromptFile: "/ws/.bench-agent/prompt.md", PromptFileRel: ".bench-agent/prompt.md"}

	cc, _, err := claudeCodeBuild("sonnet")(in)
	if err != nil || cc[0] != "claude" || cc[1] != "-p" || cc[2] != "do the thing" || !contains(cc, "--model") || !contains(cc, "sonnet") {
		t.Fatalf("claude-code build: %v %v", cc, err)
	}

	cx, _, err := codexBuild("")(in)
	if err != nil || cx[0] != "codex" || cx[1] != "exec" || cx[2] != "do the thing" {
		t.Fatalf("codex build: %v %v", cx, err)
	}

	cu, _, err := cursorBuild("")(in)
	if err != nil || cu[0] != "cursor-agent" || cu[1] != "-p" {
		t.Fatalf("cursor build: %v %v", cu, err)
	}

	ai, _, err := aiderBuild("")(in)
	if err != nil || ai[0] != "aider" || !contains(ai, "--message-file") || !contains(ai, "/ws/.bench-agent/prompt.md") {
		t.Fatalf("aider build (host path): %v %v", ai, err)
	}

	inDocker := in
	inDocker.InDocker = true
	aiDocker, _, err := aiderBuild("")(inDocker)
	if err != nil || !contains(aiDocker, "/work/.bench-agent/prompt.md") {
		t.Fatalf("aider build (container path): %v %v", aiDocker, err)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestParseAdapterBuiltinAgents(t *testing.T) {
	for _, spec := range []string{"claude-code", "codex", "cursor", "aider", "claude-code:opus"} {
		a, err := ParseAdapter(spec, AdapterOptions{})
		if err != nil {
			t.Fatalf("ParseAdapter(%q): %v", spec, err)
		}
		if a.Name() == "" {
			t.Fatalf("ParseAdapter(%q) gave empty name", spec)
		}
	}
}

func TestParseAdapterUnknownIsClearError(t *testing.T) {
	_, err := ParseAdapter("some-random-thing", AdapterOptions{})
	if err == nil || !strings.Contains(err.Error(), "unknown adapter") {
		t.Fatalf("expected unknown adapter error, got %v", err)
	}
}

// TestCommandAdapterJSONOutputRoundtrip guards against the fake-CLI JSON
// shape drifting from what genericUsage expects.
func TestCommandAdapterJSONOutputRoundtrip(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 2}, "total_cost_usd": 0.01})
	tin, tout, cost := genericUsage(string(b))
	if tin != 1 || tout != 2 || cost == nil || *cost != 0.01 {
		t.Fatalf("roundtrip mismatch: %d %d %v", tin, tout, cost)
	}
}
