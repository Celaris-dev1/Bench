#!/usr/bin/env bash
# One-shot local setup for Bench: checks prerequisites, builds, prepares Postgres
# and writes a .env with freshly generated secrets. Safe to re-run: existing
# .env files and databases are left untouched.
#
#   scripts/setup.sh            build + create the database on a local Postgres if reachable
#   scripts/setup.sh --docker   start Postgres with docker compose instead
#   scripts/setup.sh --no-db    build only (storage falls back to JSON files under .bench/)
set -euo pipefail
cd "$(dirname "$0")/.."

DB=bench
PG_ADMIN_URL=${PG_ADMIN_URL:-postgres://postgres:postgres@localhost:5432/postgres}
MODE=local
for a in "$@"; do
  case "$a" in
    --docker) MODE=docker ;;
    --no-db) MODE=none ;;
    -h|--help) sed -n '2,9p' "$0"; exit 0 ;;
    *) echo "unknown flag: $a" >&2; exit 2 ;;
  esac
done

say() { printf '\033[1m[setup]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[setup]\033[0m %s\n' "$*" >&2; }
die() { printf '\033[31m[setup]\033[0m %s\n' "$*" >&2; exit 1; }
version_ge() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" = "$2" ]; }
rand_hex() { head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'; }

# 1. prerequisites
command -v go >/dev/null || die "Go 1.24+ is required: https://go.dev/dl/"
GOV=$(go env GOVERSION); GOV=${GOV#go}
version_ge "$GOV" 1.24 || die "Go $GOV found, 1.24+ required"
say "Go $GOV"
command -v git >/dev/null || die "git is required"

# 2. build
say "building bin/bench"
mkdir -p bin
go build -o bin/bench ./cmd/bench
ls bin

# 3. database
DB_URL="${PG_ADMIN_URL%/*}/$DB"
case "$MODE" in
  docker)
    command -v docker >/dev/null || die "docker not found (drop --docker to use a local Postgres)"
    say "starting Postgres with docker compose"
    docker compose up -d postgres
    DB_URL="postgres://postgres:postgres@localhost:5432/$DB"
    for _ in $(seq 1 30); do
      docker compose exec -T postgres pg_isready -U postgres >/dev/null 2>&1 && break
      sleep 1
    done
    ;;
  local)
    if command -v psql >/dev/null && psql "$PG_ADMIN_URL" -tAc 'select 1' >/dev/null 2>&1; then
      if psql "$PG_ADMIN_URL" -tAc "select 1 from pg_database where datname='$DB'" | grep -q 1; then
        say "database '$DB' already exists"
      else
        say "creating database '$DB'"
        psql "$PG_ADMIN_URL" -qc "create database \"$DB\""
      fi
    else
      warn "no Postgres reachable at $PG_ADMIN_URL (set PG_ADMIN_URL, or re-run with --docker / --no-db)"
      DB_URL=""
    fi
    ;;
  none) DB_URL="" ;;
esac

# 4. .env (never overwritten; gitignored)
if [ -f .env ]; then
  say ".env exists, leaving it alone"
else
  say "writing .env"
  umask 077
  {
    echo "# Postgres storage for tasks/runs/results (optional; empty = JSON files under .bench/)"
    echo "BENCH_DATABASE_URL=$DB_URL"
    echo "# optional: tamper-evident + durable records of mining/scoring"
    echo "# LEDGER_URL=http://localhost:8410"
    echo "# LEDGER_TOKEN="
    echo "# optional: risk-score a diff during \`bench run --gate\` if \`gate\` is on PATH"
    echo "# (no config needed here -- BENCH_GATE_ARGS overrides the invocation if you need to)"
    echo "# optional: Claude-based failure classification (\`bench run --llm\`)"
    echo "# ANTHROPIC_API_KEY="
    echo "# optional: mine merged PRs via the GitHub API (\`bench mine --github\`) at a higher rate limit"
    echo "# GITHUB_TOKEN="
  } > .env
fi

cat <<NEXT

$(say "done")
Next steps:
  set -a; . ./.env; set +a
  bin/bench mine --repo .                                   # mine this repo's own history
  bin/bench run --agent gold                                # sanity check: the reference fix
  bin/bench report                                           # leaderboard
  scripts/e2e.sh                                             # full end-to-end check
NEXT
