// Package testrun executes test commands, locally or inside Docker.
package testrun

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
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

// Run executes argv inside dir.
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
		argv = append([]string{"docker", "run", "--rm", "--network", "none", "-v", dir + ":/work", "-w", "/work", o.DockerImage}, argv...)
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
