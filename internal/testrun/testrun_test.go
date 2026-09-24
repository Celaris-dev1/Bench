package testrun

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunLocalNoDocker(t *testing.T) {
	out := Run(t.TempDir(), []string{"sh", "-c", "echo hello"}, Options{})
	if !out.Passed || !strings.Contains(out.Output, "hello") {
		t.Fatalf("got %+v", out)
	}
}

func TestRunLocalTimeout(t *testing.T) {
	out := Run(t.TempDir(), []string{"sh", "-c", "sleep 5"}, Options{Timeout: 50 * time.Millisecond})
	if !out.TimedOut {
		t.Fatalf("expected timeout, got %+v", out)
	}
}

func TestRunRefusesDockerWithoutVerifiedNoNetwork(t *testing.T) {
	ResetVerifyCache()
	defer ResetVerifyCache()
	old := verifyNoNetwork
	defer func() { verifyNoNetwork = old }()

	calls := 0
	verifyNoNetwork = func(ctx context.Context, image string) (string, error) {
		calls++
		return "NETWORK_UP", errors.New("network access succeeded inside --network none")
	}
	out := Run(t.TempDir(), []string{"echo", "should not run"}, Options{DockerImage: "some/image"})
	if out.Passed {
		t.Fatalf("expected refusal, got pass: %+v", out)
	}
	if !strings.Contains(out.Output, "network access succeeded") {
		t.Fatalf("expected refusal reason in output, got %q", out.Output)
	}
	// A second Run against the same image must not re-probe (cached).
	Run(t.TempDir(), []string{"echo", "x"}, Options{DockerImage: "some/image"})
	if calls != 1 {
		t.Fatalf("expected verification to be cached (1 call), got %d", calls)
	}
}

func TestRunUsesDockerArgsWhenVerified(t *testing.T) {
	ResetVerifyCache()
	defer ResetVerifyCache()
	old := verifyNoNetwork
	defer func() { verifyNoNetwork = old }()
	verifyNoNetwork = func(ctx context.Context, image string) (string, error) { return "NETWORK_DOWN", nil }

	// docker itself won't exist/behave as a real sandbox here necessarily,
	// but we can at least confirm Bench attempts to invoke it (not the raw
	// test command) once verification passes, by using a fake `docker` on
	// PATH is out of scope for this unit test -- covered by dockerx.Args
	// tests for the exact command line, and by e2e for the real path.
	if err := ensureNoNetwork(context.Background(), "verified/image"); err != nil {
		t.Fatalf("expected verification to pass: %v", err)
	}
}
