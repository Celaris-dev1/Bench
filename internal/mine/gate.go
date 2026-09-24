package mine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Celaris-dev1/Bench/internal/task"
)

// gateVerdict mirrors the subset of Gate's score.Verdict we read. Kept local (not imported from
// Gate) so Bench has no build dependency on another repo; fields not present in a report are
// simply left zero.
type gateVerdict struct {
	Score    float64  `json:"score"`
	Decision string   `json:"decision"`
	Reasons  []string `json:"reasons"`
}

// gateReport mirrors the JSON `gate run --format json` writes (internal/pipeline.Report in Gate).
type gateReport struct {
	ID      string      `json:"id"`
	Repo    string      `json:"repo,omitempty"`
	Human   string       `json:"human"`
	Agent   string      `json:"agent,omitempty"`
	Files   []string    `json:"files"`
	Verdict gateVerdict `json:"verdict"`
	BaseSHA string      `json:"base_sha,omitempty"`
	HeadSHA string      `json:"head_sha,omitempty"`
}

// loadGateReports reads one Gate results JSON file, or every *.json file directly under a
// directory, each holding one gateReport (as `gate run --format json --out <file>` writes, one
// report per invocation).
func loadGateReports(path string) ([]gateReport, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var files []string
	if info.IsDir() {
		ents, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				files = append(files, filepath.Join(path, e.Name()))
			}
		}
		sort.Strings(files)
	} else {
		files = []string{path}
	}
	var reports []gateReport
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		// A file may hold one report object or a JSON array of them (e.g. an export).
		trimmed := strings.TrimSpace(string(b))
		if strings.HasPrefix(trimmed, "[") {
			var rs []gateReport
			if err := json.Unmarshal(b, &rs); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			reports = append(reports, rs...)
			continue
		}
		var r gateReport
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		reports = append(reports, r)
	}
	return reports, nil
}

// MineFromGate turns Gate-rejected changes that a later commit fixed into tasks: for every
// report at path (a results JSON file or a directory of them) whose verdict was "reject", it
// looks in repo for the first later commit touching the same files, and mines that as a task
// (its diff is the gold fix, its held-out tests are whatever tests it touched), with the prompt
// noting why Gate rejected the original attempt. Reports that never got a later fix, or whose
// head_sha isn't reachable in repo, are skipped (not an error).
func MineFromGate(repo, path string, o Options) ([]task.Task, []Candidate, error) {
	abs, err := filepath.Abs(repo)
	if err != nil {
		return nil, nil, err
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.MaxDiffLines == 0 {
		o.MaxDiffLines = 400
	}
	reports, err := loadGateReports(path)
	if err != nil {
		return nil, nil, err
	}
	var tasks []task.Task
	var cands []Candidate
	for _, r := range reports {
		if r.Verdict.Decision != "reject" || r.HeadSHA == "" {
			continue
		}
		note := fmt.Sprintf("Gate rejected an earlier attempt at this change (risk score %.2f).", r.Verdict.Score)
		if len(r.Verdict.Reasons) > 0 {
			note += " Reasons: " + strings.Join(r.Verdict.Reasons, "; ") + "."
		}
		note += " This is the fix that was eventually accepted."
		cand, ok, err := followupCandidate(abs, r.HeadSHA, note, r.Files, o)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			o.Log("from-gate: no fix found in repo for rejected head %s (report %s)", shortSHA(r.HeadSHA), r.ID)
			continue
		}
		cands = append(cands, cand)
		if cand.Reject == "" {
			tasks = append(tasks, cand.Task)
			o.Log("from-gate: accepted %s (fix of rejected %s)", cand.Task.ID, shortSHA(r.HeadSHA))
		} else {
			o.Log("from-gate: rejected %s: %s", cand.Task.ID, cand.Reject)
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].Score > tasks[j].Score })
	return tasks, cands, nil
}
