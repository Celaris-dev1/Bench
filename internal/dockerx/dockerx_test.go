package dockerx

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestArgsNoNetworkNoDir(t *testing.T) {
	got := Args(RunOptions{Image: "golang:1.24", Network: false, Command: []string{"go", "test", "./..."}})
	want := []string{"run", "--rm", "--network", "none", "golang:1.24", "go", "test", "./..."}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestArgsNetworkAndDirAndEnv(t *testing.T) {
	got := Args(RunOptions{
		Image: "myagent:latest", Dir: "/tmp/ws", Network: true,
		Env:     []string{"A=1", "B=2"},
		Command: []string{"sh", "-c", "echo hi"},
	})
	want := []string{"run", "--rm", "-e", "A=1", "-e", "B=2", "-v", "/tmp/ws:/work", "-w", "/work", "myagent:latest", "sh", "-c", "echo hi"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestArgsNetworkOmitsNetworkNoneFlag(t *testing.T) {
	got := Args(RunOptions{Image: "x", Network: true})
	for i, a := range got {
		if a == "--network" {
			t.Fatalf("expected no --network flag when Network=true, got %v at %d", got, i)
		}
	}
}

func TestProbeScriptCoversCommonTools(t *testing.T) {
	s := ProbeScript()
	for _, tool := range []string{"wget", "curl", "python3", "nc"} {
		if !strings.Contains(s, tool) {
			t.Errorf("probe script does not try %s", tool)
		}
	}
	for _, marker := range []string{"NETWORK_UP", "NETWORK_DOWN", "NO_TOOL"} {
		if !strings.Contains(s, marker) {
			t.Errorf("probe script never emits %s", marker)
		}
	}
}

// TestVerifyNoNetworkAgainstDocker exercises the real thing end to end. It
// skips cleanly whenever a docker daemon isn't reachable, so it never fails
// CI environments without Docker.
func TestVerifyNoNetworkAgainstDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !Available(ctx) {
		t.Skip("docker daemon not reachable")
	}
	out, err := VerifyNoNetwork(context.Background(), "alpine:latest")
	if err != nil {
		t.Fatalf("VerifyNoNetwork: %v (output: %s)", err, out)
	}
	if !strings.Contains(out, "NETWORK_DOWN") {
		t.Fatalf("expected NETWORK_DOWN, got %q", out)
	}
}
