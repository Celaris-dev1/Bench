package store

import (
	"context"
	"encoding/json"

	"github.com/Celaris-dev1/Bench/internal/task"
	"github.com/jackc/pgx/v5/pgxpool"
)

const schema = `
CREATE TABLE IF NOT EXISTS bench_tasks (
  id TEXT PRIMARY KEY, repo TEXT NOT NULL, commit_sha TEXT NOT NULL,
  language TEXT, verified BOOLEAN, score DOUBLE PRECISION,
  data JSONB NOT NULL, mined_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS bench_runs (
  id TEXT PRIMARY KEY, repo TEXT, agent TEXT NOT NULL, model TEXT,
  started_at TIMESTAMPTZ NOT NULL, pass_rate DOUBLE PRECISION);
CREATE TABLE IF NOT EXISTS bench_results (
  run_id TEXT REFERENCES bench_runs(id) ON DELETE CASCADE, task_id TEXT NOT NULL,
  passed BOOLEAN NOT NULL, failure_mode TEXT, risk_score DOUBLE PRECISION,
  data JSONB NOT NULL, PRIMARY KEY (run_id, task_id));
`

// PG is a Postgres store.
type PG struct{ pool *pgxpool.Pool }

// OpenPG connects and migrates.
func OpenPG(ctx context.Context, dsn string) (*PG, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, err
	}
	return &PG{pool: pool}, nil
}

func (p *PG) Close() { p.pool.Close() }

func (p *PG) SaveTasks(ctx context.Context, ts []task.Task) (int, error) {
	added := 0
	for _, t := range ts {
		b, _ := json.Marshal(t)
		var inserted bool
		err := p.pool.QueryRow(ctx, `INSERT INTO bench_tasks (id, repo, commit_sha, language, verified, score, data, mined_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (id) DO UPDATE SET data=EXCLUDED.data, verified=EXCLUDED.verified, score=EXCLUDED.score
			RETURNING (xmax = 0)`, t.ID, t.Repo, t.Commit, t.Language, t.Verified, t.Score, b, t.MinedAt).Scan(&inserted)
		if err != nil {
			return added, err
		}
		if inserted {
			added++
		}
	}
	return added, nil
}

func (p *PG) Tasks(ctx context.Context, repo string) ([]task.Task, error) {
	rows, err := p.pool.Query(ctx, `SELECT data FROM bench_tasks WHERE ($1='' OR repo=$1) ORDER BY score DESC, id`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []task.Task
	for rows.Next() {
		var b []byte
		var t task.Task
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (p *PG) SaveRun(ctx context.Context, r task.Run) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO bench_runs (id, repo, agent, model, started_at, pass_rate) VALUES ($1,$2,$3,$4,$5,$6)`,
		r.ID, r.Repo, r.Agent, r.Model, r.StartedAt, r.PassRate()); err != nil {
		return err
	}
	for _, x := range r.Results {
		b, _ := json.Marshal(x)
		if _, err := tx.Exec(ctx, `INSERT INTO bench_results (run_id, task_id, passed, failure_mode, risk_score, data) VALUES ($1,$2,$3,$4,$5,$6)`,
			r.ID, x.TaskID, x.Passed, x.FailureMode, x.RiskScore, b); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (p *PG) Runs(ctx context.Context, repo string) ([]task.Run, error) {
	rows, err := p.pool.Query(ctx, `SELECT r.id, coalesce(r.repo,''), r.agent, coalesce(r.model,''), r.started_at,
		coalesce(json_agg(x.data ORDER BY x.task_id) FILTER (WHERE x.run_id IS NOT NULL), '[]')
		FROM bench_runs r LEFT JOIN bench_results x ON x.run_id = r.id
		WHERE ($1='' OR r.repo=$1) GROUP BY r.id ORDER BY r.started_at`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []task.Run
	for rows.Next() {
		var r task.Run
		var b []byte
		if err := rows.Scan(&r.ID, &r.Repo, &r.Agent, &r.Model, &r.StartedAt, &b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &r.Results); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
