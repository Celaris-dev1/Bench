// Package report renders leaderboards with drift versus previous runs.
package report

import (
	"fmt"
	"html/template"
	"io"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/Celaris-dev1/Bench/internal/stats"
	"github.com/Celaris-dev1/Bench/internal/task"
)

// bootN is the resample count for report-level bootstrap CIs. Deterministic
// seeding keeps a given pair of runs' report byte-for-byte reproducible.
const bootN = 2000

// Entry is one leaderboard row: latest run for an agent/model pair.
type Entry struct {
	Agent, Model string
	RunID        string
	At           time.Time
	Tasks        int
	Passed       int
	PassRate     float64
	CILo, CIHi   float64 // Wilson 95% CI on PassRate
	PassAt1      float64 // pass@1, averaged per task (== PassRate when every task has equal trial counts)
	PassAt5      float64 // pass@5 when any task ran >=2 trials, else equal to PassAt1
	MaxTrials    int
	Flaky        []string // task ids that passed on some trials and failed on others
	PrevPassRate *float64
	Delta        *float64
	DeltaCILo    float64 // paired-bootstrap 95% CI on the drift, over tasks present in both runs
	DeltaCIHi    float64
	DeltaSig     bool     // true only when DeltaCI excludes zero
	Regressed    []string // passed in previous run, failed now
	Fixed        []string
	Modes        map[string]int
	AvgRisk      *float64
	History      []float64 // pass rates oldest→newest
}

// Build computes leaderboard entries from runs (any order).
func Build(runs []task.Run) []Entry {
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].StartedAt.Before(runs[j].StartedAt) })
	groups := map[string][]task.Run{}
	var keys []string
	for _, r := range runs {
		k := r.Agent + "\x00" + r.Model
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], r)
	}
	var out []Entry
	for _, k := range keys {
		g := groups[k]
		last := g[len(g)-1]
		e := Entry{Agent: last.Agent, Model: last.Model, RunID: last.ID, At: last.StartedAt, Tasks: len(last.Results), PassRate: last.PassRate(), Modes: map[string]int{}}
		var risk float64
		nr := 0
		for _, x := range last.Results {
			if x.Passed {
				e.Passed++
			} else if x.FailureMode != "" {
				e.Modes[x.FailureMode]++
			}
			if x.RiskScore != nil {
				risk += *x.RiskScore
				nr++
			}
		}
		if nr > 0 {
			a := risk / float64(nr)
			e.AvgRisk = &a
		}
		for _, r := range g {
			e.History = append(e.History, r.PassRate())
		}
		e.CILo, e.CIHi = stats.WilsonInterval(e.Passed, e.Tasks, 0.95)

		var trials []stats.TaskTrials
		for id, rs := range last.PerTask() {
			c := 0
			for _, x := range rs {
				if x.Passed {
					c++
				}
			}
			trials = append(trials, stats.TaskTrials{TaskID: id, N: len(rs), C: c})
			if len(rs) > e.MaxTrials {
				e.MaxTrials = len(rs)
			}
		}
		sort.Slice(trials, func(i, j int) bool { return trials[i].TaskID < trials[j].TaskID })
		e.PassAt1 = stats.PassAtKMean(trials, 1)
		e.PassAt5 = stats.PassAtKMean(trials, 5)
		for _, tt := range trials {
			if tt.Flaky() {
				e.Flaky = append(e.Flaky, tt.TaskID)
			}
		}
		sort.Strings(e.Flaky)

		if len(g) > 1 {
			prev := g[len(g)-2]
			pr := prev.PassRate()
			d := e.PassRate - pr
			e.PrevPassRate, e.Delta = &pr, &d
			was := map[string]bool{}
			for _, x := range prev.Results {
				was[x.TaskID] = x.Passed
			}
			for _, x := range last.Results {
				p, ok := was[x.TaskID]
				if ok && p && !x.Passed {
					e.Regressed = append(e.Regressed, x.TaskID)
				}
				if ok && !p && x.Passed {
					e.Fixed = append(e.Fixed, x.TaskID)
				}
			}
			e.DeltaCILo, e.DeltaCIHi, e.DeltaSig = drift(prev, last)
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].PassRate > out[j].PassRate })
	return out
}

// drift compares prev and last on the tasks present in both, as paired
// per-task pass rates (averaging over trials, so this also works with
// --trials > 1), and returns a 95% paired-bootstrap CI on last-prev plus
// whether that CI excludes zero. Drift is only ever reported as
// "significant" -- a real change rather than run-to-run noise -- when it
// does.
func drift(prev, last task.Run) (lo, hi float64, sig bool) {
	prevByTask, lastByTask := prev.PerTask(), last.PerTask()
	var a, b []float64
	for id, pr := range prevByTask {
		lr, ok := lastByTask[id]
		if !ok {
			continue
		}
		a = append(a, passRateOf(lr))
		b = append(b, passRateOf(pr))
	}
	if len(a) == 0 {
		return 0, 0, false
	}
	rng := rand.New(rand.NewSource(int64(len(a))*1_000_003 + 7))
	_, lo, hi = stats.PairedBootstrapDiff(a, b, bootN, 0.95, rng)
	return lo, hi, stats.Significant(lo, hi)
}

func passRateOf(rs []task.Result) float64 {
	if len(rs) == 0 {
		return 0
	}
	c := 0
	for _, r := range rs {
		if r.Passed {
			c++
		}
	}
	return float64(c) / float64(len(rs))
}

// Comparison is a head-to-head, paired significance comparison between two
// agent runs on their common tasks.
type Comparison struct {
	AgentA, ModelA string
	AgentB, ModelB string
	TasksCompared  int
	PassRateA      float64
	PassRateB      float64
	// McNemar counts: AOnly = A passed, B failed; BOnly = B passed, A failed.
	AOnly, BOnly       int
	McNemarChi2        float64
	McNemarP           float64
	DiffEstimate       float64
	DiffCILo, DiffCIHi float64
	Significant        bool
}

// Compare runs a paired comparison of two agent runs on the tasks they have
// in common (matched by task id), using McNemar's test and a paired
// bootstrap CI on the pass-rate difference.
func Compare(a, b task.Run) Comparison {
	c := Comparison{AgentA: a.Agent, ModelA: a.Model, AgentB: b.Agent, ModelB: b.Model, PassRateA: a.PassRate(), PassRateB: b.PassRate()}
	byA, byB := a.PerTask(), b.PerTask()
	var pa, pb []float64
	for id, ra := range byA {
		rb, ok := byB[id]
		if !ok {
			continue
		}
		xa, xb := passRateOf(ra), passRateOf(rb)
		pa, pb = append(pa, xa), append(pb, xb)
		switch {
		case xa > xb:
			c.AOnly++
		case xb > xa:
			c.BOnly++
		}
	}
	c.TasksCompared = len(pa)
	if c.TasksCompared == 0 {
		return c
	}
	c.McNemarChi2, c.McNemarP = stats.McNemar(c.AOnly, c.BOnly)
	rng := rand.New(rand.NewSource(int64(c.TasksCompared)*1_000_033 + 11))
	c.DiffEstimate, c.DiffCILo, c.DiffCIHi = stats.PairedBootstrapDiff(pa, pb, bootN, 0.95, rng)
	c.Significant = stats.Significant(c.DiffCILo, c.DiffCIHi)
	return c
}

func pct(f float64) string { return fmt.Sprintf("%.1f%%", f*100) }

func deltaStr(d *float64) string {
	if d == nil {
		return "–"
	}
	return fmt.Sprintf("%+.1f pts", *d*100)
}

func modesStr(m map[string]int) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var p []string
	for _, k := range ks {
		p = append(p, fmt.Sprintf("%s×%d", k, m[k]))
	}
	return strings.Join(p, ", ")
}

// Markdown writes a markdown leaderboard.
func Markdown(w io.Writer, es []Entry) {
	fmt.Fprintln(w, "# Bench leaderboard")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| # | Agent | Model | Pass | Rate (95% CI) | pass@1 | pass@5 | Drift | Regressed | Failure modes | Avg risk |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|---|")
	for i, e := range es {
		risk := "–"
		if e.AvgRisk != nil {
			risk = fmt.Sprintf("%.2f", *e.AvgRisk)
		}
		ci := fmt.Sprintf("%s [%s–%s]", pct(e.PassRate), pct(e.CILo), pct(e.CIHi))
		fmt.Fprintf(w, "| %d | %s | %s | %d/%d | %s | %s | %s | %s | %s | %s | %s |\n", i+1, e.Agent, e.Model, e.Passed, e.Tasks, ci, pct(e.PassAt1), pct(e.PassAt5), driftStr(e), strings.Join(e.Regressed, " "), modesStr(e.Modes), risk)
	}
	for _, e := range es {
		if len(e.Regressed) > 0 {
			fmt.Fprintf(w, "\n**Regression alert:** %s/%s now fails %d task(s) it previously passed: %s\n", e.Agent, e.Model, len(e.Regressed), strings.Join(e.Regressed, ", "))
		}
		if len(e.Flaky) > 0 {
			fmt.Fprintf(w, "\n**Flaky:** %s/%s is inconsistent across trials on %d task(s): %s\n", e.Agent, e.Model, len(e.Flaky), strings.Join(e.Flaky, ", "))
		}
	}
}

// driftStr renders an entry's drift, marking it "(significant)" only when
// its paired-bootstrap CI excludes zero -- run-to-run noise on small task
// sets is common and should not read as a real regression or improvement.
func driftStr(e Entry) string {
	if e.Delta == nil {
		return "–"
	}
	s := fmt.Sprintf("%s [%s, %s]", deltaStr(e.Delta), deltaStr(&e.DeltaCILo), deltaStr(&e.DeltaCIHi))
	if e.DeltaSig {
		s += " (significant)"
	}
	return s
}

var tmpl = template.Must(template.New("r").Funcs(template.FuncMap{"pct": pct, "delta": deltaStr, "modes": modesStr, "join": strings.Join,
	"neg": func(d *float64) bool { return d != nil && *d < 0 }, "add1": func(i int) int { return i + 1 },
	"ci":    func(lo, hi float64) string { return fmt.Sprintf("[%s–%s]", pct(lo), pct(hi)) },
	"sig":   func(e Entry) bool { return e.DeltaSig },
	"drift": driftStr,
	"risk": func(r *float64) string {
		if r == nil {
			return "–"
		}
		return fmt.Sprintf("%.2f", *r)
	},
	"spark": func(h []float64) string {
		bars := []rune("▁▂▃▄▅▆▇█")
		var s []rune
		for _, v := range h {
			s = append(s, bars[int(v*float64(len(bars)-1)+0.5)])
		}
		return string(s)
	}}).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Bench leaderboard</title><style>
:root{--bg:#fff;--fg:#1b1d21;--muted:#6b7280;--line:#e5e7eb;--bad:#b42318;--good:#067647}
@media (prefers-color-scheme:dark){:root{--bg:#141518;--fg:#e8e9eb;--muted:#9ca3af;--line:#2d3036;--bad:#f97066;--good:#47cd89}}
body{background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,sans-serif;margin:0;padding:24px 16px;max-width:1100px;margin:auto}
table{border-collapse:collapse;width:100%}th,td{text-align:left;padding:8px;border-bottom:1px solid var(--line);vertical-align:top}
th{color:var(--muted);font-weight:600;font-size:13px}.bad{color:var(--bad)}.good{color:var(--good)}.wrap{overflow-x:auto}
.muted{color:var(--muted)}code{font-size:13px}</style></head><body>
<h1>Bench leaderboard</h1><p class="muted">Generated {{.Now}} · {{len .E}} agent/model pairs · drift is significant only when its 95% CI excludes zero</p>
<div class="wrap"><table><tr><th>#</th><th>Agent</th><th>Model</th><th>Pass</th><th>Rate (95% CI)</th><th>pass@1</th><th>pass@5</th><th>Drift</th><th>History</th><th>Failure modes</th><th>Avg risk</th></tr>
{{range $i,$e := .E}}<tr><td>{{add1 $i}}</td><td>{{$e.Agent}}</td><td>{{$e.Model}}</td><td>{{$e.Passed}}/{{$e.Tasks}}</td><td>{{pct $e.PassRate}} {{ci $e.CILo $e.CIHi}}</td>
<td>{{pct $e.PassAt1}}</td><td>{{pct $e.PassAt5}}</td>
<td class="{{if neg $e.Delta}}bad{{else}}good{{end}}">{{delta $e.Delta}}{{if sig $e}}<strong> (significant)</strong>{{end}}{{if $e.Regressed}}<br><small>regressed: {{join $e.Regressed ", "}}</small>{{end}}{{if $e.Flaky}}<br><small class="muted">flaky: {{join $e.Flaky ", "}}</small>{{end}}</td>
<td>{{spark $e.History}}</td><td>{{modes $e.Modes}}</td><td>{{risk $e.AvgRisk}}</td></tr>{{end}}
</table></div></body></html>`))

// HTML writes an HTML leaderboard.
func HTML(w io.Writer, es []Entry) error {
	return tmpl.Execute(w, map[string]any{"E": es, "Now": time.Now().UTC().Format(time.RFC3339)})
}
