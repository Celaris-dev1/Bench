# Bench

Repo-native, self-generating evaluation harness. Bench mines your repository's own git history for
bug-fix commits that shipped with tests, turns each into a benchmark task, and scores any coding agent
against them — so "which agent should we use?" gets answered with evidence from *your* codebase.

## Quickstart

```sh
go build -o bench ./cmd/bench

# 1. Mine tasks (verifies each: held-out tests fail before the fix, pass after, in a git worktree)
./bench mine --repo ~/src/myrepo            # add --docker golang:1.24 to sandbox tests (network off)
./bench tasks

# 2. Baselines + your agent
./bench run --agent gold                    # reference fix: upper bound, sanity check
./bench run --agent noop                    # no change: lower bound
./bench run --agent 'shell:my-agent --prompt-file "$BENCH_PROMPT_FILE"' --name my-agent --model some-model --gate

# 3. Leaderboard with drift vs previous run of the same agent/model
./bench report                              # markdown
./bench report --format html --out bench.html

# 4. Keep growing: re-mine on new commits and re-run agents on the grown set
./bench watch --repo ~/src/myrepo --interval 5m --agent gold --agent 'shell:...'
```

Storage: set `BENCH_DATABASE_URL=postgres://...` for Postgres (tables `bench_tasks`, `bench_runs`,
`bench_results`, auto-migrated); otherwise JSON files under `--data` (default `.bench/`).
`docker compose up` runs Postgres plus `bench watch` on `$REPO`.

## How it works

| Stage | What happens |
|---|---|
| **Mine** (`internal/mine`) | `git log --no-merges`; keep commits whose message matches fix/bug/closes/resolves or `fixes #N`, and that touch both test files and source files. Task = parent sha, commit message as prompt (issue refs extracted), test files = held-out tests, source diff = gold diff. |
| **Detect** (`internal/detect`) | Test-file patterns for Go, Python, JS/TS, Rust. Runner: `go test` (per package), `pytest`, `jest`/`vitest` (from package.json), `cargo test`. |
| **Curate** | Reject: empty or oversized diff (`--max-diff`), too-short description, added lines hinting at network/secrets/non-determinism; then **verify** by actually running tests in a worktree (fail at parent+tests, pass at parent+gold+tests), optionally inside Docker with `--network none`. Accepted tasks are ranked by description clarity, issue references and diff size. |
| **Run** (`internal/harness`) | Fresh worktree at parent → adapter edits it → Bench captures the agent's diff **excluding test files**, resets the tree, re-applies that diff, drops in the held-out tests, runs them. Agents cannot pass by editing tests. |
| **Adapters** | `gold`, `noop`, `shell:<cmd>` — command runs with cwd = worktree and env `BENCH_PROMPT`, `BENCH_PROMPT_FILE`, `BENCH_WORKTREE`, `BENCH_TASK_ID`, `BENCH_LANGUAGE`. Any CLI agent can be wrapped this way. |
| **Gate** | `--gate`: if a `gate` binary is on PATH, runs `gate run --repo {repo} --diff {diff} --json` (override with `BENCH_GATE_ARGS`) and reads `risk_score`/`risk` (0–1 or 0–100). |
| **Failure modes** (`internal/classify`) | Heuristic: no_change, agent_error, timeout, security_regression (Gate risk ≥ 0.7), wrong_api_usage, build_failure, missed_edge_case, off_by_one (got/want differ by 1), wrong_logic. `--llm` asks Claude (`ANTHROPIC_API_KEY`, model from `BENCH_MODEL`, default `claude-sonnet-5`); without a key it is skipped and heuristics apply. |
| **Report** (`internal/report`) | Latest run per agent/model: pass rate, drift in points vs the previous run, tasks that regressed (passed before, fail now) with a regression alert, failure-mode breakdown, average Gate risk, pass-rate history sparkline. |
| **Ledger** (`internal/ledger`) | If `LEDGER_URL` is set, emits `bench.task.mined` and `bench.run.scored` on chain `bench`; actor chain = human (`BENCH_ACTOR`/`$USER`) → agent (with model) → service `bench`. No-op otherwise. |

## Development

```sh
go vet ./... && go test ./...
BENCH_TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/bench go test ./internal/store
```
Tests build a tiny fixture git repo in a temp dir (`internal/fixture`) and exercise mining,
verification, all adapters (including a test-tampering agent), reporting and `watch` end to end.

## Built vs roadmap

Built: mining, curation + execution verification, Go/Python/JS/TS/Rust runner detection, gold/noop/shell
adapters, optional Docker sandbox, Gate risk hook, heuristic + optional Claude failure classification,
Postgres/JSON storage, markdown/HTML leaderboard with drift and regression detection, polling `watch`,
Ledger records.

Not yet (from the fully built vision):
- Issue text is the commit message only — no GitHub/GitLab API fetch of the linked issue/PR body.
- `watch` polls; no webhook-on-merge trigger. No scheduled re-runs on new model releases or alerting (email/Slack) beyond the report's regression section.
- No first-class adapters for specific agents (Claude Code, Cline, Conductor) — wrap them with `shell:`.
- Test-level (per test function) scoring and flakiness detection via repeated runs; runner dependency install in Docker images is up to the user.
- No leaderboard UI service, cross-org anonymized aggregate leaderboard, or paid tracking tier.
- Did not reuse Prosper's L2 test-generation infrastructure (unavailable); Bench only uses tests that already exist in history.

License: Apache-2.0.
