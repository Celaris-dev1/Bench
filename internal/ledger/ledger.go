// Package ledger is a client for the Ledger records API. FromEnv builds a durable, spooling
// Recorder (see spool.go, mirrored from Harbour's internal/ledger/spool.go): every record is
// fsync'd to a local file before Emit returns, so a down or slow Ledger never blocks or drops a
// caller's write, and a background goroutine retries delivery with backoff and survives process
// restarts. A Recorder built by hand (as tests do) talks to Ledger directly and synchronously,
// with no spool, for the simple cases that don't need that durability.
package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Actor is one element of an actor chain.
type Actor struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Model        string `json:"model,omitempty"`
	ModelVersion string `json:"model_version,omitempty"`
}

// Record is a Ledger record request.
type Record struct {
	Chain         string         `json:"chain"`
	Type          string         `json:"type"`
	GoalID        string         `json:"goal_id,omitempty"`
	ActorChain    []Actor        `json:"actor_chain"`
	PolicyVersion string         `json:"policy_version,omitempty"`
	Payload       map[string]any `json:"payload"`
}

// backend is anything that can deliver one Record to Ledger. httpBackend does it directly over
// HTTP; *Spool wraps one durably (fsync then background retry).
type backend interface {
	Record(ctx context.Context, r Record) error
}

// httpBackend posts a Record to Ledger's HTTP API. It has no local knowledge of retries or
// durability; that's Spool's job.
type httpBackend struct {
	URL, Token string
	HTTP       *http.Client
}

func (h *httpBackend) Record(ctx context.Context, r Record) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL+"/v1/records", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	client := h.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ledger: status %d", resp.StatusCode)
	}
	return nil
}

// Recorder emits records; a zero-config Recorder is a no-op. Built by hand (URL/Token/HTTP set
// directly, as in tests) it posts synchronously with no local durability. Built by FromEnv it
// spools durably in the background instead -- see spool field and FromEnv.
type Recorder struct {
	URL, Token string
	HTTP       *http.Client

	spool *Spool // non-nil only for a FromEnv-built Recorder
}

// FromEnv builds a Recorder from LEDGER_URL / LEDGER_TOKEN. When LEDGER_URL is set, records are
// spooled durably to BENCH_LEDGER_SPOOL_DIR (default ".bench/ledger-spool") and a background
// retry loop is started against context.Background(); call Stop to end it (e.g. on a clean
// shutdown -- it is not required for correctness, since every record is already fsync'd to disk
// by the time Emit returns, and a fresh process picks the spool back up on its own).
func FromEnv() *Recorder {
	url := os.Getenv("LEDGER_URL")
	r := &Recorder{URL: url, Token: os.Getenv("LEDGER_TOKEN"), HTTP: &http.Client{Timeout: 10 * time.Second}}
	if url == "" {
		return r
	}
	dir := os.Getenv("BENCH_LEDGER_SPOOL_DIR")
	if dir == "" {
		dir = envOr("BENCH_DATA_DIR", ".bench") + "/ledger-spool"
	}
	sp := NewSpool(dir, &httpBackend{URL: r.URL, Token: r.Token, HTTP: r.HTTP})
	sp.Start(context.Background())
	r.spool = sp
	return r
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// Enabled reports whether records are sent (spooled or direct).
func (r *Recorder) Enabled() bool { return r != nil && r.URL != "" }

// Stop ends the background retry loop, if this Recorder has one (built by FromEnv against a
// non-empty LEDGER_URL). It is safe to call on any Recorder, including a nil one.
func (r *Recorder) Stop() {
	if r != nil && r.spool != nil {
		r.spool.Stop()
	}
}

// HumanActor returns the originating human (BENCH_ACTOR or $USER).
func HumanActor() Actor {
	id := os.Getenv("BENCH_ACTOR")
	if id == "" {
		id = os.Getenv("USER")
	}
	if id == "" {
		id = "unknown"
	}
	return Actor{Kind: "human", ID: id}
}

// Emit builds and delivers a record under chain "bench". With a durable (FromEnv) Recorder, it
// only fails if the local filesystem write fails -- delivery itself is retried in the background
// and a down Ledger never surfaces here. Built by hand, it posts synchronously and returns
// Ledger's own error, matching the pre-durability behavior tests rely on.
func (r *Recorder) Emit(typ, goal string, actors []Actor, payload map[string]any) error {
	if !r.Enabled() {
		return nil
	}
	chain := append([]Actor{HumanActor()}, actors...)
	chain = append(chain, Actor{Kind: "service", ID: "bench"})
	rec := Record{Chain: "bench", Type: typ, GoalID: goal, ActorChain: chain, Payload: payload}
	var be backend
	if r.spool != nil {
		be = r.spool
	} else {
		be = &httpBackend{URL: r.URL, Token: r.Token, HTTP: r.HTTP}
	}
	return be.Record(context.Background(), rec)
}
