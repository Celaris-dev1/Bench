package mine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"time"

	"github.com/Celaris-dev1/Bench/internal/task"
)

// ledgerRecord mirrors the subset of Ledger's store.Record (internal/store in Ledger) that
// `GET /v1/records` returns. Payload is left raw since incident payloads are producer-defined;
// we only look for a "commit" field naming the git sha the incident happened at.
type ledgerRecord struct {
	ID      string          `json:"id"`
	Chain   string          `json:"chain"`
	Seq     int64           `json:"seq"`
	Type    string          `json:"type"`
	GoalID  string          `json:"goal_id,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

type ledgerListResponse struct {
	Records []ledgerRecord `json:"records"`
}

// incidentPayload is the subset of an incident record's payload Bench understands. Producers
// (e.g. Gate reporting a caught regression, or a human filing one by hand) are expected to
// include at least "commit"; "files" and "reason" are optional but improve the mined task.
type incidentPayload struct {
	Commit string   `json:"commit"`
	Repo   string   `json:"repo,omitempty"`
	Files  []string `json:"files,omitempty"`
	Reason string   `json:"reason,omitempty"`
}

// FetchLedgerRecords lists every record on chain from a Ledger server at url, paginating with
// after_seq, authenticating with token (LEDGER_TOKEN) if set. It is its own function (rather than
// inlined in MineFromLedger) so tests can point it at an httptest server.
func FetchLedgerRecords(url, token, chain string) ([]ledgerRecord, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	var all []ledgerRecord
	afterSeq := int64(0)
	for {
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/records?chain=%s&after_seq=%d&limit=500", url, chain, afterSeq), nil)
		if err != nil {
			return nil, err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		var page ledgerListResponse
		err = json.NewDecoder(io.LimitReader(resp.Body, maxRemoteResponseBytes+1)).Decode(&page)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("ledger: GET /v1/records: status %d", resp.StatusCode)
		}
		if err != nil {
			return nil, fmt.Errorf("ledger: decode response: %w", err)
		}
		if len(page.Records) == 0 {
			return all, nil
		}
		all = append(all, page.Records...)
		afterSeq = page.Records[len(page.Records)-1].Seq
	}
}

// MineFromLedger turns incident records recorded on a Ledger chain into tasks: for each record
// whose payload names a "commit" (the sha an incident happened at), it looks in repo for the
// first later commit touching the same files and mines that as a task, noting the incident in
// the prompt. Records with no resolvable commit, or no later fix in repo, are skipped.
func MineFromLedger(repo, url, token, chain string, o Options) ([]task.Task, []Candidate, error) {
	abs, err := filepath.Abs(repo)
	if err != nil {
		return nil, nil, err
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.MaxDiffLines == 0 {
		o.MaxDiffLines = 400
	}
	recs, err := FetchLedgerRecords(url, token, chain)
	if err != nil {
		return nil, nil, err
	}
	var tasks []task.Task
	var cands []Candidate
	for _, r := range recs {
		var p incidentPayload
		if err := json.Unmarshal(r.Payload, &p); err != nil || p.Commit == "" {
			continue
		}
		note := fmt.Sprintf("Ledger recorded an incident (%s, record %s) at this commit.", r.Type, r.ID)
		if p.Reason != "" {
			note += " Reason: " + p.Reason + "."
		}
		note += " This is the fix that followed."
		cand, ok, err := followupCandidate(abs, p.Commit, note, p.Files, o)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			o.Log("from-ledger: no fix found in repo for incident commit %s (record %s)", shortSHA(p.Commit), r.ID)
			continue
		}
		cands = append(cands, cand)
		if cand.Reject == "" {
			tasks = append(tasks, cand.Task)
			o.Log("from-ledger: accepted %s (fix of incident %s)", cand.Task.ID, shortSHA(p.Commit))
		} else {
			o.Log("from-ledger: rejected %s: %s", cand.Task.ID, cand.Reject)
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].Score > tasks[j].Score })
	return tasks, cands, nil
}
