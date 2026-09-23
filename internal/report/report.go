// Package report renders leaderboards with drift versus previous runs.
package report

import (
	"fmt"
	"html/template"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/Celaris-dev1/Bench/internal/task"
)

// Entry is one leaderboard row: latest run for an agent/model pair.
type Entry struct {
	Agent, Model string
	RunID        string
	At           time.Time
	Tasks        int
	Passed       int
	PassRate     float64
	PrevPassRate *float64
	Delta        *float64
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
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].PassRate > out[j].PassRate })
	return out
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
	fmt.Fprintln(w, "| # | Agent | Model | Pass | Rate | Drift | Regressed | Failure modes | Avg risk |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|")
	for i, e := range es {
		risk := "–"
		if e.AvgRisk != nil {
			risk = fmt.Sprintf("%.2f", *e.AvgRisk)
		}
		fmt.Fprintf(w, "| %d | %s | %s | %d/%d | %s | %s | %s | %s | %s |\n", i+1, e.Agent, e.Model, e.Passed, e.Tasks, pct(e.PassRate), deltaStr(e.Delta), strings.Join(e.Regressed, " "), modesStr(e.Modes), risk)
	}
	for _, e := range es {
		if len(e.Regressed) > 0 {
			fmt.Fprintf(w, "\n**Regression alert:** %s/%s now fails %d task(s) it previously passed: %s\n", e.Agent, e.Model, len(e.Regressed), strings.Join(e.Regressed, ", "))
		}
	}
}

var tmpl = template.Must(template.New("r").Funcs(template.FuncMap{"pct": pct, "delta": deltaStr, "modes": modesStr, "join": strings.Join,
	"neg": func(d *float64) bool { return d != nil && *d < 0 }, "add1": func(i int) int { return i + 1 },
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
<h1>Bench leaderboard</h1><p class="muted">Generated {{.Now}} · {{len .E}} agent/model pairs</p>
<div class="wrap"><table><tr><th>#</th><th>Agent</th><th>Model</th><th>Pass</th><th>Rate</th><th>Drift</th><th>History</th><th>Failure modes</th><th>Avg risk</th></tr>
{{range $i,$e := .E}}<tr><td>{{add1 $i}}</td><td>{{$e.Agent}}</td><td>{{$e.Model}}</td><td>{{$e.Passed}}/{{$e.Tasks}}</td><td>{{pct $e.PassRate}}</td>
<td class="{{if neg $e.Delta}}bad{{else}}good{{end}}">{{delta $e.Delta}}{{if $e.Regressed}}<br><small>regressed: {{join $e.Regressed ", "}}</small>{{end}}</td>
<td>{{spark $e.History}}</td><td>{{modes $e.Modes}}</td><td>{{risk $e.AvgRisk}}</td></tr>{{end}}
</table></div></body></html>`))

// HTML writes an HTML leaderboard.
func HTML(w io.Writer, es []Entry) error {
	return tmpl.Execute(w, map[string]any{"E": es, "Now": time.Now().UTC().Format(time.RFC3339)})
}
