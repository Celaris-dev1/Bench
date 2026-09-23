package ledger

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmit(t *testing.T) {
	var got Record
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"x","seq":1}`))
	}))
	defer srv.Close()
	t.Setenv("BENCH_ACTOR", "alice")
	r := &Recorder{URL: srv.URL, Token: "tok", HTTP: srv.Client()}
	if err := r.Emit("bench.run.scored", "t1", []Actor{{Kind: "agent", ID: "a", Model: "m"}}, map[string]any{"passed": true}); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tok" || got.Chain != "bench" || got.Type != "bench.run.scored" || got.GoalID != "t1" {
		t.Fatalf("%+v %s", got, auth)
	}
	if len(got.ActorChain) != 3 || got.ActorChain[0].Kind != "human" || got.ActorChain[0].ID != "alice" || got.ActorChain[2].ID != "bench" {
		t.Fatalf("%+v", got.ActorChain)
	}
	if err := (&Recorder{}).Emit("x", "", nil, nil); err != nil {
		t.Fatal("noop recorder should not error")
	}
}
