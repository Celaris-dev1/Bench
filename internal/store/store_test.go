package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Celaris-dev1/Bench/internal/task"
)

func exercise(t *testing.T, s Store) {
	ctx := context.Background()
	repo := "/r/" + time.Now().Format("150405.000000000")
	ts := []task.Task{{ID: repo + "-1", Repo: repo, Commit: "c", Score: 1, MinedAt: time.Now().UTC()}, {ID: repo + "-2", Repo: repo, Commit: "d", Score: 2, MinedAt: time.Now().UTC()}}
	if n, err := s.SaveTasks(ctx, ts); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if n, err := s.SaveTasks(ctx, ts[:1]); err != nil || n != 0 {
		t.Fatal("upsert", n, err)
	}
	got, err := s.Tasks(ctx, repo)
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	r := 0.5
	run := task.Run{ID: repo + "run", Repo: repo, Agent: "gold", StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		Results: []task.Result{{TaskID: ts[0].ID, Passed: true, RiskScore: &r}, {TaskID: ts[1].ID, FailureMode: "no_change"}}}
	if err := s.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	runs, err := s.Runs(ctx, repo)
	if err != nil || len(runs) != 1 || len(runs[0].Results) != 2 || runs[0].PassRate() != 0.5 {
		t.Fatalf("%+v %v", runs, err)
	}
}

func TestFile(t *testing.T) {
	s, err := Open(context.Background(), "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exercise(t, s)
}

func TestPG(t *testing.T) {
	dsn := os.Getenv("BENCH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BENCH_TEST_DATABASE_URL unset")
	}
	s, err := Open(context.Background(), dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	exercise(t, s)
}
