package harness

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// builtinAgents maps an adapter spec's name (before an optional ":model"
// suffix) to a constructor for it. Each one is a CommandAdapter: it builds
// an argv for the real agent CLI, inherits docker wrapping and prompt/
// transcript handling from CommandAdapter, and reports a clear error when
// the binary isn't installed (CommandAdapter.Solve detects *exec.Error).
var builtinAgents = map[string]func(model string) CommandAdapter{
	"claude-code": func(model string) CommandAdapter {
		return CommandAdapter{AgentName: "claude-code", Build: claudeCodeBuild(model), Usage: genericUsage}
	},
	"codex": func(model string) CommandAdapter {
		return CommandAdapter{AgentName: "codex", Build: codexBuild(model), Usage: genericUsage}
	},
	"cursor": func(model string) CommandAdapter {
		return CommandAdapter{AgentName: "cursor", Build: cursorBuild(model), Usage: genericUsage}
	},
	"aider": func(model string) CommandAdapter {
		return CommandAdapter{AgentName: "aider", Build: aiderBuild(model), Usage: genericUsage}
	},
}

func builtinAgentNames() string {
	names := make([]string, 0, len(builtinAgents))
	for n := range builtinAgents {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// claudeCodeBuild runs `claude -p <prompt> --output-format json`, Claude
// Code's non-interactive mode. --dangerously-skip-permissions is passed
// because the workspace is a disposable Sandbox (see internal/gitx) whose
// only purpose is to be edited by the agent under test; there is nothing in
// it to protect.
func claudeCodeBuild(model string) func(BuildInput) ([]string, []string, error) {
	return func(in BuildInput) ([]string, []string, error) {
		argv := []string{"claude", "-p", in.Task.Prompt, "--output-format", "json", "--dangerously-skip-permissions"}
		if model != "" {
			argv = append(argv, "--model", model)
		}
		return argv, nil, nil
	}
}

// codexBuild runs `codex exec <prompt>`, the Codex CLI's non-interactive
// mode, with --full-auto so it can edit files without prompting.
func codexBuild(model string) func(BuildInput) ([]string, []string, error) {
	return func(in BuildInput) ([]string, []string, error) {
		argv := []string{"codex", "exec", in.Task.Prompt, "--json", "--full-auto"}
		if model != "" {
			argv = append(argv, "--model", model)
		}
		return argv, nil, nil
	}
}

// cursorBuild runs `cursor-agent -p <prompt> --output-format json`, the
// Cursor CLI's non-interactive print mode.
func cursorBuild(model string) func(BuildInput) ([]string, []string, error) {
	return func(in BuildInput) ([]string, []string, error) {
		argv := []string{"cursor-agent", "-p", in.Task.Prompt, "--output-format", "json", "--force"}
		if model != "" {
			argv = append(argv, "--model", model)
		}
		return argv, nil, nil
	}
}

// aiderBuild runs `aider --message-file <file> --yes`, pointing at the
// prompt file CommandAdapter wrote into the workspace's scratch directory
// (rewritten to its in-container path when the agent runs under Docker).
func aiderBuild(model string) func(BuildInput) ([]string, []string, error) {
	return func(in BuildInput) ([]string, []string, error) {
		pf := in.PromptFile
		if in.InDocker {
			pf = "/work/" + in.PromptFileRel
		}
		argv := []string{"aider", "--message-file", pf, "--yes", "--no-stream", "--no-gitignore", "--no-check-update"}
		if model != "" {
			argv = append(argv, "--model", model)
		}
		return argv, nil, nil
	}
}

// genericUsage scans a CLI's combined output for common JSON usage/cost
// field names (each of the four built-in agents' CLIs report usage this
// way, under slightly different key names) and sums/extracts what it finds.
// It never fails: agents that report nothing simply yield zero values.
func genericUsage(output string) (tokensIn, tokensOut int, costUSD *float64) {
	for _, m := range inTokenRe.FindAllStringSubmatch(output, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			tokensIn += n
		}
	}
	for _, m := range outTokenRe.FindAllStringSubmatch(output, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			tokensOut += n
		}
	}
	if ms := costRe.FindStringSubmatch(output); ms != nil {
		if f, err := strconv.ParseFloat(ms[1], 64); err == nil {
			costUSD = &f
		}
	}
	return tokensIn, tokensOut, costUSD
}

var (
	inTokenRe  = regexp.MustCompile(`"(?:input_tokens|prompt_tokens|tokens_in)"\s*:\s*([0-9]+)`)
	outTokenRe = regexp.MustCompile(`"(?:output_tokens|completion_tokens|tokens_out)"\s*:\s*([0-9]+)`)
	costRe     = regexp.MustCompile(`"(?:total_cost_usd|cost_usd|total_cost|cost)"\s*:\s*([0-9]+\.?[0-9]*)`)
)
