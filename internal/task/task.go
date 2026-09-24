// Package task defines Bench's core data types.
package task

import "time"

// Task is one mined benchmark task.
type Task struct {
	ID          string    `json:"id"`
	Repo        string    `json:"repo"`
	Commit      string    `json:"commit"`
	Parent      string    `json:"parent"`
	Prompt      string    `json:"prompt"`
	IssueRefs   []string  `json:"issue_refs,omitempty"`
	Language    string    `json:"language"`
	Runner      string    `json:"runner"`
	TestFiles   []string  `json:"test_files"`
	SourceFiles []string  `json:"source_files"`
	GoldDiff    string    `json:"gold_diff"`
	TestDiff    string    `json:"test_diff"`
	DiffLines   int       `json:"diff_lines"`
	Score       float64   `json:"score"`
	Verified    bool      `json:"verified"`
	MinedAt     time.Time `json:"mined_at"`
}

// Result is one task outcome inside a run.
type Result struct {
	TaskID      string   `json:"task_id"`
	Passed      bool     `json:"passed"`
	FailureMode string   `json:"failure_mode,omitempty"`
	RiskScore   *float64 `json:"risk_score,omitempty"`
	AgentDiff   string   `json:"agent_diff,omitempty"`
	Output      string   `json:"output,omitempty"`
	DurationMS  int64    `json:"duration_ms"`
	Error       string   `json:"error,omitempty"`
	Transcript  string   `json:"transcript,omitempty"`
	TokensIn    int      `json:"tokens_in,omitempty"`
	TokensOut   int      `json:"tokens_out,omitempty"`
	CostUSD     *float64 `json:"cost_usd,omitempty"`
}

// Run is one evaluation of an agent over a task set.
type Run struct {
	ID        string    `json:"id"`
	Repo      string    `json:"repo"`
	Agent     string    `json:"agent"`
	Model     string    `json:"model"`
	StartedAt time.Time `json:"started_at"`
	Results   []Result  `json:"results"`
}

// PerTask groups a run's results by task id, preserving trial order.
func (r Run) PerTask() map[string][]Result {
	m := map[string][]Result{}
	for _, x := range r.Results {
		m[x.TaskID] = append(m[x.TaskID], x)
	}
	return m
}

// PassRate returns fraction of passed results.
func (r Run) PassRate() float64 {
	if len(r.Results) == 0 {
		return 0
	}
	n := 0
	for _, x := range r.Results {
		if x.Passed {
			n++
		}
	}
	return float64(n) / float64(len(r.Results))
}
