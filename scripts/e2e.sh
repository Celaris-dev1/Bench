#!/usr/bin/env bash
# End-to-end smoke test: builds real bench/gold-agent binaries (never `go run`), mines a fixture
# repo's history, runs gold/noop/a fake shell agent (with --trials for pass@k), builds a report,
# and asserts the results make sense. Re-runnable: everything lives under a fresh temp dir and
# nothing is left behind on exit, including any background process this script starts (killed by
# PID -- never pkill/pgrep -f, which could reach unrelated processes on a shared host).
set -euo pipefail
cd "$(dirname "$0")/.."

WORK=$(mktemp -d -t bench-e2e-XXXXXX)
PIDS=()

cleanup() {
  local code=$?
  for pid in "${PIDS[@]:-}"; do
    [ -n "$pid" ] && kill "$pid" >/dev/null 2>&1 || true
  done
  for pid in "${PIDS[@]:-}"; do
    [ -n "$pid" ] && wait "$pid" 2>/dev/null || true
  done
  rm -rf "$WORK"
  exit "$code"
}
trap cleanup EXIT INT TERM

say() { printf '\033[1m[e2e]\033[0m %s\n' "$*"; }
die() { printf '\033[31m[e2e]\033[0m %s\n' "$*" >&2; exit 1; }

# 1. Build real binaries.
say "building bin/bench"
mkdir -p bin
go build -o bin/bench ./cmd/bench
BENCH="$(pwd)/bin/bench"

# 2. A tiny fixture repo with one clean bug-fix-with-test commit and one commit that should be
#    rejected (test already passes before the "fix").
REPO="$WORK/fixture-repo"
mkdir -p "$REPO"
git() { command git -C "$REPO" -c user.name=e2e -c user.email=e2e@example.com "$@"; }
git init -q -b main
cat > "$REPO/go.mod" <<'EOF'
module example.com/calc

go 1.21
EOF
mkdir -p "$REPO/calc"
cat > "$REPO/calc/sum.go" <<'EOF'
package calc

func Sum(n int) int {
	s := 0
	for i := 1; i < n; i++ {
		s += i
	}
	return s
}
EOF
git add -A
git commit -q -m "initial calc"

cat > "$REPO/calc/sum.go" <<'EOF'
package calc

// Sum adds 1..n.
func Sum(n int) int {
	s := 0
	for i := 1; i <= n; i++ {
		s += i
	}
	return s
}
EOF
cat > "$REPO/calc/sum_test.go" <<'EOF'
package calc

import "testing"

func TestSum(t *testing.T) {
	if got := Sum(3); got != 6 {
		t.Fatalf("Sum(3) = %d, want 6", got)
	}
}
EOF
git add -A
git commit -q -m "Fix off-by-one in Sum

Sum(n) should include n itself. Fixes #1"

# 3. Mine, verifying each task by actually running its tests.
DATA="$WORK/.bench"
say "mining"
"$BENCH" mine --repo "$REPO" --data "$DATA" | tee "$WORK/mine.out"
grep -q "accepted 1 tasks" "$WORK/mine.out" || die "expected exactly 1 accepted task"
TASKS=$("$BENCH" tasks --data "$DATA")
echo "$TASKS"
[ "$(echo "$TASKS" | wc -l)" -eq 1 ] || die "expected exactly 1 task listed"

# 4. gold (reference fix: must pass) and noop (no change: must fail) baselines.
say "running gold"
"$BENCH" run --agent gold --data "$DATA" --repo "$REPO" | tee "$WORK/gold.out"
grep -q "1/1 passed" "$WORK/gold.out" || die "gold agent should pass its own reference fix"

say "running noop"
"$BENCH" run --agent noop --data "$DATA" --repo "$REPO" | tee "$WORK/noop.out"
grep -q "0/1 passed" "$WORK/noop.out" || die "noop agent should pass nothing"

# 5. A fake shell agent that actually edits the worktree like a real CLI agent would, run with
#    --trials to exercise pass@k / variance reporting.
FAKE_AGENT="$WORK/fake-agent.sh"
cat > "$FAKE_AGENT" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
cat > "$BENCH_WORKTREE/calc/sum.go" <<'GO'
package calc

func Sum(n int) int {
	return n * (n + 1) / 2
}
GO
EOF
chmod +x "$FAKE_AGENT"
say "running fake shell agent (--trials 3)"
"$BENCH" run --agent "shell:$FAKE_AGENT" --name fake-agent --data "$DATA" --repo "$REPO" --trials 3 | tee "$WORK/fake.out"
grep -q "3/3 passed" "$WORK/fake.out" || die "fake agent should pass all 3 trials (deterministic fix)"

# 6. Report with confidence intervals, and a paired comparison.
say "report"
"$BENCH" report --data "$DATA" --repo "$REPO" | tee "$WORK/report.md"
grep -qi "gold" "$WORK/report.md" || die "report missing gold agent"
grep -qi "fake-agent" "$WORK/report.md" || die "report missing fake-agent"

say "compare"
"$BENCH" compare --a gold --b noop --data "$DATA" --repo "$REPO" | tee "$WORK/compare.out"
grep -q "McNemar" "$WORK/compare.out" || die "compare output missing McNemar stats"

# 7. `bench watch --once` (background, killed by PID -- exercises the same code path as a long-
#    running watcher without actually looping forever in CI).
say "watch --once"
"$BENCH" watch --repo "$REPO" --data "$DATA" --agent gold --once &
WATCH_PID=$!
PIDS+=("$WATCH_PID")
wait "$WATCH_PID"
PIDS=()

say "all checks passed"
