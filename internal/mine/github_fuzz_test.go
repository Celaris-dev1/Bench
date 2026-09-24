package mine

// Fuzzes decoding of GitHub's PR/file/issue JSON -- Bench's own trust boundary with the GitHub
// API, which is a third party and (through a compromised or malicious account) can be made to
// serve close to arbitrary JSON bodies in these same shapes.

import (
	"encoding/json"
	"testing"
)

func FuzzGitHubJSONDecode(f *testing.F) {
	seeds := []string{
		`{"number":1,"title":"fix: off by one","body":"fixes #2","merged_at":"2024-01-01T00:00:00Z","merge_commit_sha":"abc","base":{"sha":"a"},"head":{"sha":"b"}}`,
		`{}`,
		`[]`,
		`{"number":"not-a-number"}`,
		`{"merged_at":null}`,
		`null`,
		`{"base":null}`,
		`{"title":` + `"` + `` + `"}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		var pr ghPR
		_ = json.Unmarshal([]byte(body), &pr) // error is fine; panicking is not

		var files []ghFile
		_ = json.Unmarshal([]byte(body), &files)

		var issue ghIssue
		_ = json.Unmarshal([]byte(body), &issue)
	})
}
