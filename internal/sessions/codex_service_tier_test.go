package sessions

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// These are the metadata shapes Codex writes, with user content omitted:
// current feedback_tags, the WebSocket request CodexBar reads, and the
// older submission debug record. A request's tier is not a served tier.
func TestServiceTierCodexLogShapes(t *testing.T) {
	for _, tt := range []struct {
		body, thread, turn, tier string
		ok                       bool
	}{
		{`session_loop{thread_id=s}:submission_dispatch{submission.id="t"}:turn{thread.id=s turn.id=t}: feedback tags auth_mode=Some(Chatgpt) tags_json={"service_tier":"priority"}`, "s", "t", "priority", true},
		{`session_loop{thread_id=s}:turn{turn_id="t"}: tags_json={"service_tier":"default","feature.fast_mode":true}`, "s", "t", "default", true},
		{`session_loop{thread_id=s}:turn{turn.id=t}: websocket request: {"type":"response.create","model":"gpt-6.1-sol","service_tier":"fast","input":[]}`, "s", "t", "priority", true},
		{`session_loop{thread_id=s}: Submission sub=Submission { id: "t", op: UserTurn { service_tier: Some(Some("priority")) } }`, "s", "t", "priority", true},
		{`turn{turn_id=t}: tags_json={"service_tier":"ultrafast"}`, "s", "t", "ultrafast", true},
		{`turn{turn_id=t}: tags_json={"service_tier":"auto"}`, "s", "", "", false},
		{`turn{turn_id=t}: tags_json={"feature.fast_mode":true}`, "s", "", "", false},
		{`turn{turn_id=t}: tags_json={"service_tier":`, "s", "", "", false},
		{`turn{turn_id=t}: websocket event: {"type":"response.completed","response":{"service_tier":"priority"}}`, "s", "", "", false},
		{`turn{thread_id=other turn_id=t}: tags_json={"service_tier":"priority"}`, "s", "", "", false},
		{`tags_json={"service_tier":"priority"}`, "s", "", "", false},
	} {
		got, ok := parseCodexTier(tt.body, tt.thread)
		if ok != tt.ok || ok && (got.Thread != tt.thread || got.Turn != tt.turn || got.Tier != tt.tier) {
			t.Fatalf("shape %q: %+v %v", tt.body, got, ok)
		}
	}
}

func TestServiceTierCodexConcurrentReads(t *testing.T) {
	db, path := tierDB(t, t.TempDir())
	insertTier(t, db, "s", "a", "priority")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				s := codexTiersAt(path)
				if s == nil || s.turns["s"]["a"] != "priority" {
					t.Error("concurrent reader lost the known turn")
				}
			}
		}()
	}
	insertTier(t, db, "s", "b", "default")
	wg.Wait()
	if s := codexTiersAt(path); s == nil || s.turns["s"]["b"] != "default" {
		t.Fatal("append lost after concurrent reads")
	}
}

func tierDB(t *testing.T, home string) (*sql.DB, string) {
	t.Helper()
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "logs_2.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE logs (id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER, target TEXT, thread_id TEXT, feedback_log_body TEXT); CREATE INDEX idx_logs_thread_id ON logs(thread_id);`)
	if err != nil {
		t.Fatal(err)
	}
	return db, path
}

func insertTier(t *testing.T, db *sql.DB, thread, turn, tier string) {
	t.Helper()
	body := fmt.Sprintf(`session_loop{thread_id=%s}:turn{turn.id=%s}: tags_json={"service_tier":%q}`, thread, turn, tier)
	if _, err := db.Exec(`INSERT INTO logs(ts,target,thread_id,feedback_log_body) VALUES(1,'feedback_tags',?,?)`, thread, body); err != nil {
		t.Fatal(err)
	}
}

func TestServiceTierCodexRetainedEvidence(t *testing.T) {
	db, path := tierDB(t, t.TempDir())
	insertTier(t, db, "s", "a", "priority")
	first := codexTiersAt(path)
	if first == nil || first.turns["s"]["a"] != "priority" {
		t.Fatal("Fast turn was not read")
	}
	insertTier(t, db, "s", "b", "default")
	next := codexTiersAt(path)
	if next == nil || next.turns["s"]["a"] != "priority" || next.turns["s"]["b"] != "default" || next.threadRevision("s") == first.threadRevision("s") {
		t.Fatal("WAL append did not refresh tier evidence")
	}
	if first.turns["s"]["b"] != "" {
		t.Fatal("published snapshot was mutated")
	}
	// Conflicting requests in one turn cannot safely classify its calls.
	insertTier(t, db, "s", "a", "default")
	if got := codexTiersAt(path); got.turns["s"]["a"] != "" {
		t.Fatal("conflicting turn claimed one tier")
	}
	if _, err := db.Exec(`DELETE FROM logs WHERE id IN (1,3)`); err != nil {
		t.Fatal(err)
	}
	if got := codexTiersAt(path); len(got.rows) != 1 || got.turns["s"]["a"] != "" || got.turns["s"]["b"] != "default" {
		t.Fatal("pruned trace evidence survived")
	}
	// Rewriting retained evidence at the same row count must refresh too.
	if _, err := db.Exec(`UPDATE logs SET feedback_log_body='turn{turn.id=b}: tags_json={"service_tier":"priority"}'`); err != nil {
		t.Fatal(err)
	}
	if got := codexTiersAt(path); got.turns["s"]["b"] != "priority" {
		t.Fatal("rewritten evidence was cached")
	}
}

func TestServiceTierCodexCallsAndLateLog(t *testing.T) {
	d := setupCalls(t)
	thread := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	// A copied history can contain a different actor than the filename.
	// Its real actor's late metadata must invalidate that source too.
	path := filepath.Join(d.codex, "sessions", "2026", "10", "16", "rollout-2026-10-16T12-00-00-ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
	writeLines(t, path,
		`{"timestamp":"2026-10-16T12:00:00Z","type":"session_meta","payload":{"id":"`+thread+`","model_provider":"openai"}}`,
		`{"timestamp":"2026-10-16T12:00:01Z","type":"turn_context","payload":{"turn_id":"a","model":"gpt-6.1-sol","effort":"high"}}`,
		`{"timestamp":"2026-10-16T12:00:02Z","type":"token_usage_record","payload":{"turn_id":"a","thread_id":"`+thread+`","response_id":"resp-a","usage":{"input_tokens":1000,"output_tokens":100,"cached_input_tokens":100}}}`)
	before := CallSources()
	if len(before) != 1 {
		t.Fatalf("sources=%+v", before)
	}
	if calls := ReadCallSource(before[0]); len(calls) != 1 || calls[0].ServiceTier != "" || calls[0].TurnID != "a" {
		t.Fatalf("unknown call: %+v", calls)
	}
	db, _ := tierDB(t, d.codex)
	insertTier(t, db, thread, "a", "priority")
	after := CallSources()
	if len(after) != 1 || before[0].TierRevision == after[0].TierRevision || before[0].Modified != after[0].Modified {
		t.Fatal("late trace must change only the tier revision")
	}
	for _, calls := range [][]Call{ReadCallSource(after[0]), AgentCalls(time.Time{}, "codex")} {
		if len(calls) != 1 || calls[0].ServiceTier != "priority" || calls[0].Input != 900 {
			t.Fatalf("enriched calls: %+v", calls)
		}
	}
	Reset()
	if calls := AgentCalls(time.Time{}, "codex"); len(calls) != 1 || calls[0].ServiceTier != "priority" {
		t.Fatalf("cached call lost turn identity: %+v", calls)
	}
}
