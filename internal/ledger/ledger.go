// Package ledger is a minimal HTTP client for the Ledger records API.
package ledger

import (
	"bytes"
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

// Recorder emits records; a zero-config Recorder is a no-op.
type Recorder struct {
	URL, Token string
	HTTP       *http.Client
}

// FromEnv builds a Recorder from LEDGER_URL / LEDGER_TOKEN.
func FromEnv() *Recorder {
	return &Recorder{URL: os.Getenv("LEDGER_URL"), Token: os.Getenv("LEDGER_TOKEN"), HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Enabled reports whether records are sent.
func (r *Recorder) Enabled() bool { return r != nil && r.URL != "" }

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

// Emit posts a record under chain "bench". Errors are returned but callers typically log them.
func (r *Recorder) Emit(typ, goal string, actors []Actor, payload map[string]any) error {
	if !r.Enabled() {
		return nil
	}
	chain := append([]Actor{HumanActor()}, actors...)
	chain = append(chain, Actor{Kind: "service", ID: "bench"})
	body, _ := json.Marshal(Record{Chain: "bench", Type: typ, GoalID: goal, ActorChain: chain, Payload: payload})
	req, err := http.NewRequest("POST", r.URL+"/v1/records", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ledger: status %d", resp.StatusCode)
	}
	return nil
}
