// Package dockerx builds `docker run` invocations and verifies that a
// container actually has no network access before Bench trusts it as a test
// sandbox.
package dockerx

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// RunOptions configures a `docker run` invocation.
type RunOptions struct {
	Image   string
	Dir     string   // host directory mounted at /work; omitted from the command if empty
	Network bool     // true = network allowed; false = `--network none`
	Env     []string // KEY=VALUE pairs passed with -e
	Command []string // argv exec'd inside the container
}

// Args builds the argv that follows "docker" for opts (i.e. `docker
// <Args(...)...>` is the full command line). It is a pure function so
// command construction can be unit tested without a docker daemon.
func Args(o RunOptions) []string {
	args := []string{"run", "--rm"}
	if !o.Network {
		args = append(args, "--network", "none")
	}
	for _, e := range o.Env {
		args = append(args, "-e", e)
	}
	if o.Dir != "" {
		args = append(args, "-v", o.Dir+":/work", "-w", "/work")
	}
	args = append(args, o.Image)
	args = append(args, o.Command...)
	return args
}

// Available reports whether the docker CLI can reach a daemon, within a
// short timeout.
func Available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "info").Run() == nil
}

// probeScript tries every network tool commonly present in a test image, in
// order, and prints exactly one of NETWORK_UP, NETWORK_DOWN or NO_TOOL. It
// targets the link-local metadata address (169.254.169.254) rather than a
// real host, so the probe needs no DNS and cannot itself leak a request
// anywhere interesting even if network access does turn out to be open.
const probeScript = `if command -v wget >/dev/null 2>&1; then
  wget -T 3 -t 1 -O- http://169.254.169.254/ >/dev/null 2>&1 && echo NETWORK_UP || echo NETWORK_DOWN
elif command -v curl >/dev/null 2>&1; then
  curl -m 3 -sf http://169.254.169.254/ >/dev/null 2>&1 && echo NETWORK_UP || echo NETWORK_DOWN
elif command -v python3 >/dev/null 2>&1; then
  python3 -c "import socket;socket.create_connection(('169.254.169.254',80),2)" >/dev/null 2>&1 && echo NETWORK_UP || echo NETWORK_DOWN
elif command -v nc >/dev/null 2>&1; then
  nc -w 2 -z 169.254.169.254 80 >/dev/null 2>&1 && echo NETWORK_UP || echo NETWORK_DOWN
else
  echo NO_TOOL
fi`

// ProbeScript returns the shell script VerifyNoNetwork runs inside the
// container, exported so tests can assert on its construction.
func ProbeScript() string { return probeScript }

// VerifyNoNetwork runs a throwaway `--network none` container from image
// and confirms that a connection attempt inside it fails, proving the
// sandbox really has no network access. It returns the raw probe output and
// a non-nil error if the probe could not run, could not verify one way or
// the other (no network tool found in the image), or -- critically -- if
// network access unexpectedly succeeded. Callers must refuse to run
// anything security-sensitive in image when this returns an error.
func VerifyNoNetwork(ctx context.Context, image string) (string, error) {
	args := Args(RunOptions{Image: image, Network: false, Command: []string{"sh", "-c", probeScript}})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("docker no-network probe failed to run on image %s: %w: %s", image, err, s)
	}
	switch {
	case strings.Contains(s, "NETWORK_UP"):
		return s, fmt.Errorf("docker no-network probe: network access SUCCEEDED inside a --network none container (image %s); refusing to trust this sandbox", image)
	case strings.Contains(s, "NETWORK_DOWN"):
		return s, nil
	default:
		return s, fmt.Errorf("docker no-network probe: could not verify isolation (no wget/curl/python3/nc found in image %s); refusing to run without proof of network isolation", image)
	}
}
