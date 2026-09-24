package mine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Celaris-dev1/Bench/internal/fixture"
)

func ledgerServer(t *testing.T, wantToken string, recs []ledgerRecord) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/records" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if wantToken != "" && r.Header.Get("Authorization") != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		after := r.URL.Query().Get("after_seq")
		w.Header().Set("Content-Type", "application/json")
		if after != "0" {
			json.NewEncoder(w).Encode(ledgerListResponse{}) // second page: empty, ends pagination
			return
		}
		json.NewEncoder(w).Encode(ledgerListResponse{Records: recs})
	}))
}

func TestMineFromLedger(t *testing.T) {
	repo := fixture.Repo(t)
	root := rootSHA(t, repo)

	payload, _ := json.Marshal(incidentPayload{Commit: root, Files: []string{"calc/sum.go"}, Reason: "off-by-one in production"})
	recs := []ledgerRecord{
		{ID: "rec-1", Chain: "incidents", Seq: 1, Type: "bench.incident", Payload: payload},
		{ID: "rec-2", Chain: "incidents", Seq: 2, Type: "bench.incident", Payload: json.RawMessage(`{"reason":"no commit field, must be skipped"}`)},
	}
	srv := ledgerServer(t, "s3cr3t", recs)
	defer srv.Close()

	tasks, cands, err := MineFromLedger(repo, srv.URL, "s3cr3t", "incidents", Options{Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || len(tasks) != 1 {
		t.Fatalf("want 1 candidate/task, got %d/%d", len(cands), len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "off-by-one in production") {
		t.Fatalf("prompt missing incident annotation: %q", tasks[0].Prompt)
	}
	if !tasks[0].Verified {
		t.Fatalf("task not verified: %+v", tasks[0])
	}
}

func TestMineFromLedgerBadToken(t *testing.T) {
	repo := fixture.Repo(t)
	srv := ledgerServer(t, "right-token", nil)
	defer srv.Close()
	if _, _, err := MineFromLedger(repo, srv.URL, "wrong-token", "incidents", Options{}); err == nil {
		t.Fatal("expected an error for a bad token")
	}
}
