package report

import (
	"bytes"
	"fmt"
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

func TestBuildPassAtKAndFlaky(t *testing.T) {
	t0 := time.Now()
	// One task run 4 trials: 1 pass. Another run 4 trials: all pass.
	run := task.Run{ID: "r1", Agent: "a", Model: "m", StartedAt: t0, Results: []task.Result{
		{TaskID: "flaky", Passed: false}, {TaskID: "flaky", Passed: true}, {TaskID: "flaky", Passed: false}, {TaskID: "flaky", Passed: false},
		{TaskID: "solid", Passed: true}, {TaskID: "solid", Passed: true}, {TaskID: "solid", Passed: true}, {TaskID: "solid", Passed: true},
	}}
	es := Build([]task.Run{run})
	if len(es) != 1 {
		t.Fatalf("%+v", es)
	}
	e := es[0]
	if e.MaxTrials != 4 {
		t.Fatalf("MaxTrials = %d, want 4", e.MaxTrials)
	}
	if len(e.Flaky) != 1 || e.Flaky[0] != "flaky" {
		t.Fatalf("Flaky = %v, want [flaky]", e.Flaky)
	}
	// pass@1 averaged over tasks: flaky=1/4=0.25, solid=1 -> mean 0.625.
	if e.PassAt1 < 0.6 || e.PassAt1 > 0.65 {
		t.Fatalf("PassAt1 = %v, want ~0.625", e.PassAt1)
	}
	if e.CILo < 0 || e.CIHi > 1 || e.CILo > e.CIHi {
		t.Fatalf("bad CI [%v,%v]", e.CILo, e.CIHi)
	}
}

func TestBuildDriftSignificance(t *testing.T) {
	t0 := time.Now()
	// 20 tasks; agent regresses on all of them between the two runs. This
	// should be flagged significant. A single flipped task among many
	// should not.
	var prevResults, lastResultsBig, lastResultsSmall []task.Result
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("t%d", i)
		prevResults = append(prevResults, task.Result{TaskID: id, Passed: true})
		lastResultsBig = append(lastResultsBig, task.Result{TaskID: id, Passed: false})
		pass := i != 0 // only the first task flips
		lastResultsSmall = append(lastResultsSmall, task.Result{TaskID: id, Passed: pass})
	}
	prev := task.Run{ID: "p", Agent: "a", Model: "m", StartedAt: t0, Results: prevResults}

	bigRegression := task.Run{ID: "b", Agent: "a", Model: "m", StartedAt: t0.Add(time.Hour), Results: lastResultsBig}
	es := Build([]task.Run{prev, bigRegression})
	if !es[0].DeltaSig {
		t.Fatalf("expected a full 20/20 -> 0/20 regression to be flagged significant: %+v", es[0])
	}

	smallFlip := task.Run{ID: "s", Agent: "a", Model: "m", StartedAt: t0.Add(time.Hour), Results: lastResultsSmall}
	es2 := Build([]task.Run{prev, smallFlip})
	if es2[0].DeltaSig {
		t.Fatalf("expected a single flipped task out of 20 to NOT be flagged significant: %+v", es2[0])
	}
}

func TestCompare(t *testing.T) {
	t0 := time.Now()
	var ra, rb []task.Result
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("t%d", i)
		ra = append(ra, task.Result{TaskID: id, Passed: i < 27}) // 27/30
		rb = append(rb, task.Result{TaskID: id, Passed: i < 6})  // 6/30
	}
	a := task.Run{ID: "ra", Agent: "agentA", Model: "m1", StartedAt: t0, Results: ra}
	b := task.Run{ID: "rb", Agent: "agentB", Model: "m2", StartedAt: t0, Results: rb}
	c := Compare(a, b)
	if c.TasksCompared != 30 {
		t.Fatalf("TasksCompared = %d, want 30", c.TasksCompared)
	}
	if !c.Significant {
		t.Fatalf("expected a clear 27/30 vs 6/30 difference to be significant: %+v", c)
	}
	if c.McNemarP >= 0.01 {
		t.Fatalf("expected a tiny McNemar p-value, got %v", c.McNemarP)
	}
	if c.DiffEstimate < 0.69 || c.DiffEstimate > 0.71 {
		t.Fatalf("DiffEstimate = %v, want ~0.7", c.DiffEstimate)
	}
}

func TestCompareNoCommonTasks(t *testing.T) {
	a := task.Run{Agent: "a", Results: []task.Result{{TaskID: "x", Passed: true}}}
	b := task.Run{Agent: "b", Results: []task.Result{{TaskID: "y", Passed: true}}}
	c := Compare(a, b)
	if c.TasksCompared != 0 || c.Significant {
		t.Fatalf("expected no comparison possible: %+v", c)
	}
}
