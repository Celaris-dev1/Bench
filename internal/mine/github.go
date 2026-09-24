package mine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Celaris-dev1/Bench/internal/detect"
	"github.com/Celaris-dev1/Bench/internal/gitx"
	"github.com/Celaris-dev1/Bench/internal/task"
	"github.com/Celaris-dev1/Bench/internal/testrun"
)

// GitHubOptions configures mining from a GitHub repository's merged pull
// requests via the REST API (https://docs.github.com/en/rest), instead of
// local git history.
type GitHubOptions struct {
	Owner, Repo string
	Token       string // GITHUB_TOKEN; optional, but anonymous requests are rate-limited much lower
	BaseURL     string // override for tests (an httptest server); defaults to https://api.github.com
	HTTPClient  *http.Client
	MaxPRs      int // 0 = no limit (subject to GitHub's rate limits)

	// LocalRepo, when set, is a local clone used to compute each task's gold
	// and test diffs (via `git diff`) and, if Verify is set, to verify it the
	// same way local history mining does. A PR whose commits aren't present
	// in LocalRepo yet (e.g. not fetched) is rejected with a clear reason
	// rather than silently skipped. Without LocalRepo, tasks are returned
	// with GitHub metadata (prompt, files, shas) but no diffs and
	// Verified=false -- useful for a quick survey of what mining would find.
	LocalRepo    string
	Verify       bool
	Test         testrun.Options
	MaxDiffLines int
	Log          func(format string, a ...any)
}

// ghClient is a minimal GitHub REST client: pagination via the Link header,
// and basic rate-limit handling (a 403/429 with Retry-After is waited out
// once; a hard "remaining=0" rate limit is reported as an error rather than
// blocking indefinitely).
type ghClient struct {
	base, token string
	http        *http.Client
	sleep       func(time.Duration) // overridable in tests
}

func (c *ghClient) url(pathOrURL string) string {
	if strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://") {
		return pathOrURL
	}
	return c.base + pathOrURL
}

// get fetches pathOrURL, decodes JSON into out (if non-nil), and returns the
// next page's URL from the Link header, if any.
func (c *ghClient) get(ctx context.Context, pathOrURL string, out any) (next string, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(pathOrURL), nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return "", err
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return "", readErr
		}
		if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) && attempt == 0 {
			if wait, ok := retryAfter(resp.Header); ok {
				c.sleepFor(wait)
				continue
			}
			if resp.Header.Get("X-RateLimit-Remaining") == "0" {
				return "", fmt.Errorf("github: rate limit exceeded (resets at unix %s)", resp.Header.Get("X-RateLimit-Reset"))
			}
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("github: GET %s: %d: %s", pathOrURL, resp.StatusCode, strings.TrimSpace(string(body)))
		}
		if out != nil && len(body) > 0 {
			if err := json.Unmarshal(body, out); err != nil {
				return "", fmt.Errorf("github: decode %s: %w", pathOrURL, err)
			}
		}
		return parseNextLink(resp.Header.Get("Link")), nil
	}
	return "", fmt.Errorf("github: GET %s: still rate limited after waiting", pathOrURL)
}

func (c *ghClient) sleepFor(d time.Duration) {
	if c.sleep != nil {
		c.sleep(d)
		return
	}
	time.Sleep(d)
}

// retryAfter reads a Retry-After header (seconds), capped at 60s so a
// misbehaving or malicious server can't stall mining indefinitely.
func retryAfter(h http.Header) (time.Duration, bool) {
	v := h.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0, false
	}
	if secs > 60 {
		secs = 60
	}
	return time.Duration(secs) * time.Second, true
}

// parseNextLink extracts the rel="next" URL from a GitHub Link header.
func parseNextLink(link string) string {
	for _, part := range strings.Split(link, ",") {
		seg := strings.SplitN(part, ";", 2)
		if len(seg) < 2 || !strings.Contains(seg[1], `rel="next"`) {
			continue
		}
		u := strings.TrimSpace(seg[0])
		return strings.TrimSuffix(strings.TrimPrefix(u, "<"), ">")
	}
	return ""
}

type ghPR struct {
	Number         int        `json:"number"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	MergedAt       *time.Time `json:"merged_at"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	Base           struct {
		SHA string `json:"sha"`
	} `json:"base"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

type ghFile struct {
	Filename string `json:"filename"`
	Status   string `json:"status"`
}

type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

// MineGitHub mines tasks from repo's merged pull requests: a PR is a
// candidate when it links an issue (fixes/closes/resolves #N in its title
// or body) and its changed files include both a test file and a source
// file (per internal/detect); the linked issue's body becomes the prompt
// (falling back to the PR body if the issue can't be fetched).
func MineGitHub(ctx context.Context, o GitHubOptions) ([]task.Task, []Candidate, error) {
	if o.Owner == "" || o.Repo == "" {
		return nil, nil, fmt.Errorf("github: owner and repo required")
	}
	if o.BaseURL == "" {
		o.BaseURL = "https://api.github.com"
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if o.MaxDiffLines == 0 {
		o.MaxDiffLines = 400
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	cl := &ghClient{base: o.BaseURL, token: o.Token, http: o.HTTPClient}

	var tasks []task.Task
	var cands []Candidate
	seen := 0
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=closed&sort=updated&direction=desc&per_page=100", o.Owner, o.Repo)
	for path != "" {
		var prs []ghPR
		next, err := cl.get(ctx, path, &prs)
		if err != nil {
			return tasks, cands, err
		}
		for _, pr := range prs {
			if pr.MergedAt == nil {
				continue // closed without merging
			}
			if o.MaxPRs > 0 && seen >= o.MaxPRs {
				return tasks, cands, nil
			}
			seen++
			cand, t, err := mineOnePR(ctx, cl, o, pr)
			if err != nil {
				return tasks, cands, err
			}
			if cand == nil {
				continue // no linked issue: not a candidate at all
			}
			cands = append(cands, *cand)
			if cand.Reject == "" {
				tasks = append(tasks, t)
				o.Log("accepted %s", t.ID)
			} else {
				o.Log("rejected PR #%d: %s", pr.Number, cand.Reject)
			}
		}
		path = next
	}
	return tasks, cands, nil
}

func mineOnePR(ctx context.Context, cl *ghClient, o GitHubOptions, pr ghPR) (*Candidate, task.Task, error) {
	text := pr.Title + "\n\n" + pr.Body
	refsFound := issueRefRe.FindAllString(text, -1)
	if len(refsFound) == 0 {
		return nil, task.Task{}, nil
	}

	var files []ghFile
	filePath := fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=100", o.Owner, o.Repo, pr.Number)
	for filePath != "" {
		var page []ghFile
		next, err := cl.get(ctx, filePath, &page)
		if err != nil {
			return nil, task.Task{}, err
		}
		files = append(files, page...)
		filePath = next
	}
	var tests, srcs []string
	for _, f := range files {
		switch {
		case detect.IsTest(f.Filename):
			tests = append(tests, f.Filename)
		case detect.IsSource(f.Filename):
			srcs = append(srcs, f.Filename)
		}
	}
	if len(tests) == 0 || len(srcs) == 0 {
		return nil, task.Task{}, nil // not a candidate: this PR isn't a test+fix change
	}

	prompt := strings.TrimSpace(text)
	issueNum := issueNumber(refsFound[0])
	if issueNum > 0 {
		var issue ghIssue
		if _, err := cl.get(ctx, fmt.Sprintf("/repos/%s/%s/issues/%d", o.Owner, o.Repo, issueNum), &issue); err == nil && strings.TrimSpace(issue.Body) != "" {
			prompt = strings.TrimSpace(issue.Title + "\n\n" + issue.Body)
		}
	}

	commit := pr.MergeCommitSHA
	if commit == "" {
		commit = pr.Head.SHA
	}
	t := task.Task{
		ID:          fmt.Sprintf("%s-%s-pr%d", o.Owner, o.Repo, pr.Number),
		Repo:        o.LocalRepo,
		Commit:      commit,
		Parent:      pr.Base.SHA,
		Prompt:      prompt,
		IssueRefs:   refs(text),
		Language:    detect.Language(srcs),
		TestFiles:   tests,
		SourceFiles: srcs,
		MinedAt:     time.Now().UTC(),
	}
	if o.LocalRepo == "" {
		t.Score = score(t)
		return &Candidate{Task: t, Reject: "no --repo given: diffs not computed, not verified"}, t, nil
	}
	t.Runner = detect.Runner(o.LocalRepo, t.Language)
	var err error
	t.GoldDiff, err = gitx.Run(o.LocalRepo, append([]string{"diff", "--binary", t.Parent, t.Commit, "--"}, srcs...)...)
	if err != nil {
		return &Candidate{Task: t, Reject: "commit not available locally (fetch it first): " + err.Error()}, t, nil
	}
	t.TestDiff, _ = gitx.Run(o.LocalRepo, append([]string{"diff", "--binary", t.Parent, t.Commit, "--"}, tests...)...)
	t.DiffLines = countChanged(t.GoldDiff)
	t.Score = score(t)

	reject := staticReject(t, Options{MaxDiffLines: o.MaxDiffLines})
	if reject == "" && o.Verify {
		reject = verify(o.LocalRepo, &t, Options{Test: o.Test})
	}
	return &Candidate{Task: t, Reject: reject}, t, nil
}

// issueNumber extracts the numeric #N from an issueRefRe match like
// "fixes #123" or "fixes owner/repo#123".
func issueNumber(ref string) int {
	m := hashRefRe.FindString(ref)
	n, _ := strconv.Atoi(strings.TrimPrefix(m, "#"))
	return n
}
