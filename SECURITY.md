# Security policy

Bench runs coding agents against your repository's own history and scores their diffs, so it
sits in the path of two adversaries at once: the repository it mines, and the agent it evaluates.
This document states what Bench defends against, where the trust boundaries are, and how to
report a vulnerability.

## Reporting a vulnerability

Please report privately (e.g. via your GitHub repository's Security Advisories, "Report a
vulnerability") rather than a public issue. Include a reproduction (a fixture repo, a task ID, or
a `bench mine`/`bench run` invocation), the Bench version/commit, and the command affected. We
aim to acknowledge within 3 working days and to fix high/critical issues promptly, crediting
reporters who wish to be named.

## Threat model

Two adversaries, evaluated separately:

1. **A malicious repository.** `bench mine` walks arbitrary git history -- commit messages,
   file names, file contents, diffs -- and `--github`/`--from-gate`/`--from-ledger` pull in file
   lists and metadata from a GitHub API response, a Gate report, or a Ledger record. Any of these
   can be crafted by whoever controls the repo, the PR, or (if Gate/Ledger are shared services)
   the account that produced the record. Bench treats all of it as data, never as a path,
   command, or instruction to trust outright.
2. **A malicious or buggy agent.** `bench run` hands a task's prompt to an adapter (`gold`,
   `noop`, or `shell:<cmd>`, which can wrap any CLI agent) and lets it edit a worktree. The agent
   is assumed to be at least as adversarial as the repository: it can try to read files outside
   its task, phone home, or produce a diff crafted to fool Bench's own scoring.

Out of scope: a compromised Bench host or operator, a compromised Postgres instance, a
compromised Ledger/Gate server Bench is configured to trust, and denial of service by whoever
already has shell access to the host running `bench`.

## Trust boundaries

| Input | Trust | How Bench treats it |
|---|---|---|
| Commit messages, diffs, file names/paths from git history | **untrusted** | Read only via `git` subcommands (internal/gitx), never shelled out to directly. File paths taken from a commit's tree (internal/mine) are checked with `internal/pathsafe` before being kept as a task's `TestFiles`/`SourceFiles`, since they're later used as worktree/sandbox-relative paths. Diffs are size-capped (`--max-diff`) and scanned for network/secret/non-determinism hints before a task is accepted. |
| GitHub API responses (`--github`), Gate report JSON (`--from-gate`), Ledger records (`--from-ledger`) | **untrusted** | Same path-safety check applies to file lists from all three. Response/file bodies are capped at 32MiB (`internal/mine`) so a misbehaving or malicious server can't exhaust memory. JSON decoding never panics on malformed input (see fuzz targets below). `GITHUB_TOKEN`/`LEDGER_TOKEN` are sent only as request headers, never logged or written to a task/report. |
| Agent diffs (`bench run`) | **untrusted** | Captured from a fresh git worktree/sandbox (internal/gitx, internal/dockerx), never applied back onto the real repository. Bench re-applies only the non-test portion of the diff and drops in its own held-out tests, so an agent cannot pass by editing the tests that grade it (see README's harness stage). With `--gate`, the diff is additionally scored by `gate` if present on `PATH`. |
| Test/build execution | **untrusted code** | Runs in a throwaway git worktree or, with `--docker <image>`, inside a container Bench first verifies has no network (`internal/dockerx.VerifyNoNetwork`) before trusting it for a real run -- a broken or misconfigured sandbox is refused rather than silently used. |
| `shell:<cmd>` agent adapters | **operator-configured, but runs untrusted-repo content** | The command itself is chosen by whoever runs `bench run`; Bench does not fetch or execute an adapter command from task/repo data. It receives the task prompt/worktree path via env vars and can do anything its cwd/network access allow -- see Known limitations. |
| Ledger emission (`internal/ledger`) | outbound only | Bench never executes anything a Ledger response contains for `bench.task.mined`/`bench.run.scored` emission; `--from-ledger` reads records as data. Records are queued to a local, fsync'd spool (`internal/ledger/spool.go`) before an HTTP POST is attempted, so a compromised or unreachable Ledger can't block or corrupt a `bench run`. |
| Failure classification LLM calls (`--llm`) | untrusted output | The model only labels a failure mode from a fixed enum (`internal/classify.Modes`); its output is never executed or written back into a task. |

### Sandbox limits

* **Local worktrees** (the default) are *not* isolated from the host: tests run as the invoking
  user with full filesystem and network access, the same way `go test`/`pytest`/etc. would if you
  ran them yourself. Use `--docker <image>` for anything beyond a repository and agent you
  already trust.
* **`--docker`** disables the container's network (`--network none`) and Bench verifies that
  before trusting it, but does not otherwise restrict CPU/memory/PIDs, filesystem access within
  the container, or use gVisor/Firecracker-grade isolation. Treat it as isolating "this test suite
  can't reach the network," not as a hard multi-tenant sandbox.
* **`--agent-docker`** runs the agent adapter itself inside Docker, but with
  `--agent-docker-network` defaulting to `true` (most agent CLIs need to reach their own API);
  set it to `false` if your adapter doesn't need network access.
* Bench does not run untrusted repositories' build tooling (`node_modules/.bin`, custom linters,
  etc.) as part of mining -- only whatever test runner `internal/detect` picks (`go test`,
  `pytest`, `jest`/`vitest`, `cargo test`), invoked the same way a developer would.

### Secrets

`GITHUB_TOKEN`, `LEDGER_TOKEN`, `ANTHROPIC_API_KEY` and `BENCH_DATABASE_URL` are read from the
environment and sent only as request headers/connection parameters; Bench never writes them to a
task, run, report, or log line. Mined task prompts and diffs come from repository content the
operator already has access to, not from Bench's own credentials.

## Known limitations

* A `shell:<cmd>` adapter runs with the invoking user's full environment and network access
  unless the operator sandboxes it themselves (e.g. with `--agent-docker`); Bench does not vet or
  restrict what the adapter command does.
* `--docker`'s no-network verification (`internal/dockerx.VerifyNoNetwork`) is a real check at
  container-start time, not a standing guarantee against every container escape technique;
  treat it as raising the bar, not as air-gapping.
* Fuzz coverage (`go test -fuzz`) targets Bench's own parsers (commit messages, diff line
  counting, GitHub JSON, test-output classification); it does not cover `git` itself or the
  language-specific test runners Bench shells out to.
* `bench watch`'s polling loop and `bench run`'s agent invocations have no per-run resource caps
  beyond `--agent-timeout`/`--test-timeout`; a very large or slow-to-verify repository can make a
  mining/run pass take a long time, not fail fast.
* Postgres storage (`BENCH_DATABASE_URL`) uses the connection string's own TLS/auth settings;
  Bench does not add its own transport security on top.
