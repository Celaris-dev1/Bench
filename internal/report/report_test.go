package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Celaris-dev1/Bench/internal/task"
)

func TestDrift(t *testing.T) {
	t0 := time.Now()
	runs := []task.Run{
		{ID: "b", Agent: "a", Model: "m2", StartedAt: t0.Add(time.Hour), Results: []task.Result{{TaskID: "t1", Passed: false, FailureMode: "off_by_one"}, {TaskID: "t2", Passed: true}}},
		{ID: "a", Agent: "a", Model: "m2", StartedAt: t0, Results: []task.Result{{TaskID: "t1", Passed: true}, {TaskID: "t2", Passed: false}}},
		{ID: "g", Agent: "gold", StartedAt: t0, Results: []task.Result{{TaskID: "t1", Passed: true}, {TaskID: "t2", Passed: true}}},
	}
	es := Build(runs)
	if len(es) != 2 || es[0].Agent != "gold" {
		t.Fatalf("%+v", es)
	}
	e := es[1]
	if e.RunID != "b" || e.Delta == nil || *e.Delta != 0 || len(e.Regressed) != 1 || e.Regressed[0] != "t1" || len(e.Fixed) != 1 || e.Modes["off_by_one"] != 1 {
		t.Fatalf("%+v", e)
	}
	var md, html bytes.Buffer
	Markdown(&md, es)
	if !strings.Contains(md.String(), "Regression alert") || !strings.Contains(md.String(), "| gold |") {
		t.Fatal(md.String())
	}
	if err := HTML(&html, es); err != nil || !strings.Contains(html.String(), "<title>Bench leaderboard</title>") {
		t.Fatal(err)
	}
}
