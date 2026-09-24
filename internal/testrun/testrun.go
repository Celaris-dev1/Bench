// Package testrun executes test commands, locally or inside Docker.
package testrun

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Celaris-dev1/Bench/internal/dockerx"
)

// Options controls how tests run.
type Options struct {
	Timeout     time.Duration
	DockerImage string // if set, tests run in `docker run --rm --network none`
}

// Outcome is the result of a test invocation.
type Outcome struct {
	Passed   bool
	TimedOut bool
	Output   string
}

// verifyNoNetwork is overridable in tests so they don't need a real docker
// daemon to exercise the caching/refusal logic.
var verifyNoNetwork = dockerx.VerifyNoNetwork

var (
	verifiedMu    sync.Mutex
	verifiedCache = map[string]error{}
)

// ensureNoNetwork verifies (once per image, cached for the process
// lifetime) that DockerImage really has no network access, per
// dockerx.VerifyNoNetwork, and refuses to proceed otherwise.
func ensureNoNetwork(ctx context.Context, image string) error {
	verifiedMu.Lock()
	if err, ok := verifiedCache[image]; ok {
		verifiedMu.Unlock()
		return err
	}
	verifiedMu.Unlock()
	_, err := verifyNoNetwork(ctx, image)
	verifiedMu.Lock()
	verifiedCache[image] = err
	verifiedMu.Unlock()
	return err
}

// ResetVerifyCache clears the per-image no-network verification cache; used
// by tests.
func ResetVerifyCache() {
	verifiedMu.Lock()
	verifiedCache = map[string]error{}
	verifiedMu.Unlock()
}

// Run executes argv inside dir. When o.DockerImage is set, Bench first
// verifies (see internal/dockerx) that a --network none container from that
// image genuinely cannot reach the network, and refuses to run tests
// otherwise -- silently trusting a broken or misconfigured sandbox would
// defeat the point of running untrusted agent diffs in it.
func Run(dir string, argv []string, o Options) Outcome {
	if len(argv) == 0 {
		return Outcome{Output: "no test command for runner"}
	}
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), o.Timeout)
	defer cancel()
	if o.DockerImage != "" {
		if err := ensureNoNetwork(ctx, o.DockerImage); err != nil {
			return Outcome{Output: "[bench] " + err.Error()}
		}
		argv = append([]string{"docker"}, dockerx.Args(dockerx.RunOptions{
			Image: o.DockerImage, Dir: dir, Network: false, Command: argv,
		})...)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := buf.String()
	if len(out) > 20000 {
		out = out[:10000] + "\n...[truncated]...\n" + out[len(out)-10000:]
	}
	if ctx.Err() == context.DeadlineExceeded {
		return Outcome{TimedOut: true, Output: out + "\n[bench] timeout"}
	}
	if err != nil && !strings.Contains(err.Error(), "exit status") {
		out += "\n[bench] " + err.Error()
	}
	return Outcome{Passed: err == nil, Output: out}
}
