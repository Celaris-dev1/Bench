// Package classify assigns failure modes to failed task results.
package classify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// Failure modes.
const (
	NoChange       = "no_change"
	BuildFailure   = "build_failure"
	WrongAPI       = "wrong_api_usage"
	OffByOne       = "off_by_one"
	MissedEdgeCase = "missed_edge_case"
	Timeout        = "timeout"
	SecurityRegr   = "security_regression"
	WrongLogic     = "wrong_logic"
	AgentError     = "agent_error"
)

// Modes lists all known modes.
var Modes = []string{NoChange, BuildFailure, WrongAPI, OffByOne, MissedEdgeCase, Timeout, SecurityRegr, WrongLogic, AgentError}

// Input is what the classifier sees.
type Input struct {
	Prompt, AgentDiff, GoldDiff, Output string
	TimedOut                            bool
	AgentErr                            string
	Risk                                *float64
}

var (
	wrongAPIRe = regexp.MustCompile(`(?i)(undefined:|has no (field or )?method|not enough arguments|too many arguments|cannot use .* as|AttributeError|TypeError: .* is not a function|is not a function|no method named|cannot find (function|value)|ImportError|ModuleNotFoundError|unresolved import)`)
	buildRe    = regexp.MustCompile(`(?i)(syntax error|SyntaxError|expected .*found|build failed|\[build failed\]|could not compile|error\[E\d+\])`)
	edgeRe     = regexp.MustCompile(`(?i)(nil pointer|index out of range|panic:|NoneType|null|undefined is not|KeyError|IndexError|ZeroDivision|division by zero|out of bounds|unwrap\(\) on)`)
	numsRe     = regexp.MustCompile(`(?i)(?:got|actual|received|left)\D{0,12}(-?\d+)[^\n]{0,80}?(?:want|expected|right)\D{0,12}(-?\d+)`)
	numsRe2    = regexp.MustCompile(`(?i)(?:want|expected|right)\D{0,12}(-?\d+)[^\n]{0,80}?(?:got|actual|received|left)\D{0,12}(-?\d+)`)
)

// Heuristic classifies with regexes over test output and diffs.
func Heuristic(in Input) string {
	switch {
	case in.AgentErr != "":
		return AgentError
	case in.TimedOut:
		return Timeout
	case strings.TrimSpace(in.AgentDiff) == "":
		return NoChange
	case in.Risk != nil && *in.Risk >= 0.7:
		return SecurityRegr
	case wrongAPIRe.MatchString(in.Output):
		return WrongAPI
	case buildRe.MatchString(in.Output):
		return BuildFailure
	case edgeRe.MatchString(in.Output):
		return MissedEdgeCase
	}
	if offByOne(in.Output) {
		return OffByOne
	}
	return WrongLogic
}

func offByOne(out string) bool {
	for _, re := range []*regexp.Regexp{numsRe, numsRe2} {
		for _, m := range re.FindAllStringSubmatch(out, -1) {
			var a, b int
			fmt.Sscan(m[1], &a)
			fmt.Sscan(m[2], &b)
			if a-b == 1 || b-a == 1 {
				return true
			}
		}
	}
	return false
}

// LLM classifies using the Claude API; returns "" and a note if unavailable.
func LLM(in Input) (string, string) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return "", "skipped: no API key"
	}
	model := os.Getenv("BENCH_MODEL")
	if model == "" {
		model = "claude-sonnet-5"
	}
	trim := func(s string, n int) string {
		if len(s) > n {
			return s[:n]
		}
		return s
	}
	prompt := fmt.Sprintf("Classify why this coding agent's change failed the held-out tests. Answer with exactly one label from: %s.\n\nTASK:\n%s\n\nAGENT DIFF:\n%s\n\nREFERENCE DIFF:\n%s\n\nTEST OUTPUT:\n%s",
		strings.Join(Modes, ", "), trim(in.Prompt, 2000), trim(in.AgentDiff, 6000), trim(in.GoldDiff, 4000), trim(in.Output, 4000))
	body, _ := json.Marshal(map[string]any{"model": model, "max_tokens": 20, "messages": []map[string]string{{"role": "user", "content": prompt}}})
	base := os.Getenv("ANTHROPIC_BASE_URL")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	req, _ := http.NewRequest("POST", base+"/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return "", "llm error: " + err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var r struct {
		Content []struct{ Text string } `json:"content"`
	}
	if resp.StatusCode != 200 || json.Unmarshal(b, &r) != nil || len(r.Content) == 0 {
		return "", fmt.Sprintf("llm error: status %d", resp.StatusCode)
	}
	ans := strings.ToLower(r.Content[0].Text)
	for _, m := range Modes {
		if strings.Contains(ans, m) {
			return m, ""
		}
	}
	return "", "llm returned unknown label"
}

// Classify uses the LLM when requested and available, otherwise heuristics.
func Classify(in Input, useLLM bool) string {
	if useLLM {
		if m, _ := LLM(in); m != "" {
			return m
		}
	}
	return Heuristic(in)
}
