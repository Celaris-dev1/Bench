// Package store persists tasks and runs in Postgres or a JSON directory.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Celaris-dev1/Bench/internal/task"
)

// Store persists Bench data.
type Store interface {
	SaveTasks(ctx context.Context, ts []task.Task) (added int, err error)
	Tasks(ctx context.Context, repo string) ([]task.Task, error)
	SaveRun(ctx context.Context, r task.Run) error
	Runs(ctx context.Context, repo string) ([]task.Run, error) // oldest first
	Close()
}

// Open picks Postgres when dsn starts with postgres:// or postgresql://, else a JSON dir.
func Open(ctx context.Context, dsn, dir string) (Store, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return OpenPG(ctx, dsn)
	}
	return OpenFile(dir)
}

// File is a JSON-file store: <dir>/tasks.json and <dir>/runs/<id>.json.
type File struct{ Dir string }

// OpenFile creates the directory if needed.
func OpenFile(dir string) (*File, error) {
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		return nil, err
	}
	return &File{Dir: dir}, nil
}

func (f *File) Close() {}

func (f *File) load() ([]task.Task, error) {
	b, err := os.ReadFile(filepath.Join(f.Dir, "tasks.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ts []task.Task
	return ts, json.Unmarshal(b, &ts)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (f *File) SaveTasks(_ context.Context, ts []task.Task) (int, error) {
	cur, err := f.load()
	if err != nil {
		return 0, err
	}
	idx := map[string]int{}
	for i, t := range cur {
		idx[t.ID] = i
	}
	added := 0
	for _, t := range ts {
		if i, ok := idx[t.ID]; ok {
			cur[i] = t
			continue
		}
		idx[t.ID] = len(cur)
		cur = append(cur, t)
		added++
	}
	return added, writeJSON(filepath.Join(f.Dir, "tasks.json"), cur)
}

func (f *File) Tasks(_ context.Context, repo string) ([]task.Task, error) {
	all, err := f.load()
	if err != nil || repo == "" {
		return all, err
	}
	var out []task.Task
	for _, t := range all {
		if t.Repo == repo {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *File) SaveRun(_ context.Context, r task.Run) error {
	return writeJSON(filepath.Join(f.Dir, "runs", strings.ReplaceAll(r.ID, "/", "_")+".json"), r)
}

func (f *File) Runs(_ context.Context, repo string) ([]task.Run, error) {
	ents, err := os.ReadDir(filepath.Join(f.Dir, "runs"))
	if err != nil {
		return nil, err
	}
	var runs []task.Run
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(f.Dir, "runs", e.Name()))
		if err != nil {
			return nil, err
		}
		var r task.Run
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		if repo == "" || r.Repo == repo {
			runs = append(runs, r)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt.Before(runs[j].StartedAt) })
	return runs, nil
}
