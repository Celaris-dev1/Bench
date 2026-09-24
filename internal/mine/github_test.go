package mine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Celaris-dev1/Bench/internal/fixture"
	"github.com/Celaris-dev1/Bench/internal/gitx"
)

// fixShas returns the sha of the fixture's "off-by-one" fix commit and its
// parent, so a fake GitHub server can describe a PR that really exists in
// the fixture repo (needed for gitx.Run diff computation against LocalRepo).
func fixShas(t *testing.T, repo string) (sha, parent string) {
	t.Helper()
	out, err := gitx.Run(repo, "log", "--format=%H%x00%P%x00%s")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		p := strings.SplitN(line, "\x00", 3)
		if len(p) == 3 && strings.Contains(p[2], "off-by-one") {
			return p[0], p[1]
		}
	}
	t.Fatal("fix commit not found")
	return "", ""
}

// fakeGitHub serves a single merged, issue-linked PR (matching the
// fixture's real fix commit) across paginated /pulls and /pulls/N/files
// endpoints, plus an /issues/1 endpoint whose body becomes the task prompt.
// Never contacts real GitHub.
func fakeGitHub(t *testing.T, fixSHA, parentSHA string, extraHandler func(w http.ResponseWriter, r *http.Request) bool) *httptest.Server {
	t.Helper()
	mergedAt := time.Now().UTC()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		if extraHandler != nil && extraHandler(w, r) {
			return
		}
		if r.URL.Query().Get("page") == "2" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]any{})
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/pulls?state=closed&page=2>; rel="next"`, "http://"+r.Host))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]ghPR{{
			Number: 42, Title: "Fix off-by-one in Sum", Body: "Fixes #1",
			MergedAt: &mergedAt, MergeCommitSHA: fixSHA,
			Base: struct {
				SHA string `json:"sha"`
			}{SHA: parentSHA},
		}})
	})
	mux.HandleFunc("/repos/o/r/pulls/42/files", func(w http.ResponseWriter, r *http.Request) {
		if extraHandler != nil && extraHandler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			json.NewEncoder(w).Encode([]ghFile{{Filename: "calc/sum_test.go", Status: "added"}})
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/pulls/42/files?page=2>; rel="next"`, "http://"+r.Host))
		json.NewEncoder(w).Encode([]ghFile{{Filename: "calc/sum.go", Status: "modified"}})
	})
	mux.HandleFunc("/repos/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		if extraHandler != nil && extraHandler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ghIssue{Number: 1, Title: "Sum is off by one", Body: "Sum(3) should be 6 but returns 3."})
	})
	return httptest.NewServer(mux)
}

func TestMineGitHubBasicFlow(t *testing.T) {
	repo := fixture.Repo(t)
	fixSHA, parentSHA := fixShas(t, repo)
	srv := fakeGitHub(t, fixSHA, parentSHA, nil)
	defer srv.Close()

	tasks, cands, err := MineGitHub(context.Background(), GitHubOptions{
		Owner: "o", Repo: "r", BaseURL: srv.URL, LocalRepo: repo, Verify: true,
	})
	if err != nil {
		t.Fatalf("MineGitHub: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %d: %+v", len(cands), cands)
	}
	if len(tasks) != 1 {
		t.Fatalf("want 1 task, got %d (reject=%q)", len(tasks), cands[0].Reject)
	}
	tk := tasks[0]
	if !tk.Verified {
		t.Fatalf("expected task to verify: %+v", tk)
	}
	if !strings.Contains(tk.Prompt, "off by one") {
		t.Fatalf("expected issue body as prompt, got %q", tk.Prompt)
	}
	if len(tk.TestFiles) != 1 || tk.TestFiles[0] != "calc/sum_test.go" {
		t.Fatalf("bad test files: %v", tk.TestFiles)
	}
	if len(tk.SourceFiles) != 1 || tk.SourceFiles[0] != "calc/sum.go" {
		t.Fatalf("bad source files: %v", tk.SourceFiles)
	}
	if tk.GoldDiff == "" || strings.Contains(tk.GoldDiff, "sum_test") {
		t.Fatalf("gold diff should cover only sum.go: %q", tk.GoldDiff)
	}
}

func TestMineGitHubSkipsUnlinkedPR(t *testing.T) {
	mux := http.NewServeMux()
	mergedAt := time.Now().UTC()
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			json.NewEncoder(w).Encode([]any{})
			return
		}
		json.NewEncoder(w).Encode([]ghPR{{Number: 7, Title: "Refactor", Body: "cleanup, no issue", MergedAt: &mergedAt}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tasks, cands, err := MineGitHub(context.Background(), GitHubOptions{Owner: "o", Repo: "r", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 || len(cands) != 0 {
		t.Fatalf("PR with no linked issue must be skipped entirely, got tasks=%d cands=%d", len(tasks), len(cands))
	}
}

func TestMineGitHubWithoutLocalRepoStillReportsCandidate(t *testing.T) {
	repo := fixture.Repo(t)
	fixSHA, parentSHA := fixShas(t, repo)
	srv := fakeGitHub(t, fixSHA, parentSHA, nil)
	defer srv.Close()

	tasks, cands, err := MineGitHub(context.Background(), GitHubOptions{Owner: "o", Repo: "r", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("without --repo, nothing should be accepted (no diffs/verification): %+v", tasks)
	}
	if len(cands) != 1 || !strings.Contains(cands[0].Reject, "no --repo given") {
		t.Fatalf("expected a clear no-local-repo rejection, got %+v", cands)
	}
}

func TestMineGitHubCommitNotLocalIsRejected(t *testing.T) {
	repo := fixture.Repo(t) // has no commit matching a made-up sha
	srv := fakeGitHub(t, "0000000000000000000000000000000000dead", "0000000000000000000000000000000000beef", nil)
	defer srv.Close()

	tasks, cands, err := MineGitHub(context.Background(), GitHubOptions{Owner: "o", Repo: "r", BaseURL: srv.URL, LocalRepo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected rejection for a commit not present locally, got tasks=%+v", tasks)
	}
	if len(cands) != 1 || !strings.Contains(cands[0].Reject, "not available locally") {
		t.Fatalf("expected 'not available locally' rejection, got %+v", cands)
	}
}

func TestMineGitHubHandlesRetryAfter(t *testing.T) {
	repo := fixture.Repo(t)
	fixSHA, parentSHA := fixShas(t, repo)
	rateLimited := false
	srv := fakeGitHub(t, fixSHA, parentSHA, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/pulls") && !rateLimited && r.URL.Query().Get("page") != "2" {
			rateLimited = true
			w.Header().Set("Retry-After", "0") // don't actually slow the test down
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		return false
	})
	defer srv.Close()

	tasks, _, err := MineGitHub(context.Background(), GitHubOptions{Owner: "o", Repo: "r", BaseURL: srv.URL, LocalRepo: repo})
	if err != nil {
		t.Fatalf("expected the client to retry after Retry-After: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("want 1 task after retry, got %d", len(tasks))
	}
	if !rateLimited {
		t.Fatal("test setup bug: rate limit path never hit")
	}
}

func TestMineGitHubHardRateLimitIsAnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "9999999999")
		w.WriteHeader(http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, _, err := MineGitHub(context.Background(), GitHubOptions{Owner: "o", Repo: "r", BaseURL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("expected a rate-limit error, got %v", err)
	}
}

func TestMineGitHubMaxPRs(t *testing.T) {
	repo := fixture.Repo(t)
	fixSHA, parentSHA := fixShas(t, repo)
	srv := fakeGitHub(t, fixSHA, parentSHA, nil)
	defer srv.Close()

	tasks, cands, err := MineGitHub(context.Background(), GitHubOptions{Owner: "o", Repo: "r", BaseURL: srv.URL, LocalRepo: repo, MaxPRs: 0})
	if err != nil {
		t.Fatal(err)
	}
	_ = tasks
	if len(cands) != 1 {
		t.Fatalf("want 1 candidate scanned, got %d", len(cands))
	}
}

func TestParseNextLink(t *testing.T) {
	if got := parseNextLink(`<https://api.github.com/x?page=2>; rel="next", <https://api.github.com/x?page=5>; rel="last"`); got != "https://api.github.com/x?page=2" {
		t.Fatalf("got %q", got)
	}
	if got := parseNextLink(""); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := parseNextLink(`<https://api.github.com/x?page=5>; rel="last"`); got != "" {
		t.Fatalf("got %q, want empty (no next)", got)
	}
}
