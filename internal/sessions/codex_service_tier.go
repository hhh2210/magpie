package sessions

// Codex's rollouts do not always retain service_tier. Its local trace log
// can establish the requested tier for an exact thread/turn. This follows
// CodexBar's use of request/submission evidence; feedback_tags is the newer
// Codex shape. Only tier metadata is retained, never request bodies.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

type codexTierEvidence struct {
	Thread, Turn, Tier string
}

type codexTierSnapshot struct {
	stamp       string
	file        os.FileInfo
	last, count int64
	rows        map[int64]codexTierEvidence
	turns       map[string]map[string]string
	revisions   map[string]string
}

func (s *codexTierSnapshot) threadRevision(thread string) string {
	if s == nil {
		return ""
	}
	return s.revisions[thread]
}

var codexTierCache = struct {
	sync.Mutex
	values  map[string]*codexTierSnapshot
	flights map[string]chan struct{}
}{values: map[string]*codexTierSnapshot{}, flights: map[string]chan struct{}{}}

func codexTiersForHome(home string) *codexTierSnapshot {
	if home == "" {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(codexSQLiteDir(home), "logs_*.sqlite"))
	if len(paths) == 0 {
		return nil
	}
	// Codex uses the highest schema version, never a previous version's
	// database left beside it by an upgrade.
	slices.SortFunc(paths, func(a, b string) int {
		var av, bv int
		fmt.Sscanf(filepath.Base(a), "logs_%d.sqlite", &av)
		fmt.Sscanf(filepath.Base(b), "logs_%d.sqlite", &bv)
		return av - bv
	})
	return codexTiersAt(paths[len(paths)-1])
}

func codexTiersAt(path string) *codexTierSnapshot {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	stamp := fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
	if wal, err := os.Stat(path + "-wal"); err == nil {
		stamp += fmt.Sprintf(":%d:%d", wal.Size(), wal.ModTime().UnixNano())
	}
	codexTierCache.Lock()
	old := codexTierCache.values[path]
	if old != nil && old.stamp == stamp && os.SameFile(old.file, info) {
		codexTierCache.Unlock()
		return old
	}
	if done := codexTierCache.flights[path]; done != nil {
		codexTierCache.Unlock()
		<-done
		codexTierCache.Lock()
		result := codexTierCache.values[path]
		codexTierCache.Unlock()
		return result
	}
	done := make(chan struct{})
	codexTierCache.flights[path] = done
	codexTierCache.Unlock()
	next := readCodexTiers(path, info, stamp, old)
	codexTierCache.Lock()
	if next != nil {
		// Different Codex homes have independent evidence. Bound the memo
		// without keeping a deleted or replaced database's answer forever.
		if len(codexTierCache.values) >= 8 {
			for key := range codexTierCache.values {
				if key != path {
					delete(codexTierCache.values, key)
					break
				}
			}
		}
		codexTierCache.values[path] = next
	} else if old != nil && !os.SameFile(old.file, info) {
		delete(codexTierCache.values, path)
	}
	delete(codexTierCache.flights, path)
	close(done)
	codexTierCache.Unlock()
	// A busy/unreadable source does not erase evidence from the same
	// database. Replacement never inherits the previous file's evidence.
	if next == nil && old != nil && os.SameFile(old.file, info) {
		return old
	}
	return next
}

func readCodexTiers(path string, info os.FileInfo, stamp string, old *codexTierSnapshot) *codexTierSnapshot {
	db, err := provider.OpenReadOnly(path)
	if err != nil {
		return nil
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil
	}
	defer tx.Rollback()
	var high, count int64
	// Keep max and count separate: SQLite can seek the largest rowid and
	// count B-tree entries without walking every log's payload.
	if tx.QueryRowContext(ctx, "SELECT coalesce(max(id),0) FROM logs").Scan(&high) != nil {
		return nil
	}
	if tx.QueryRowContext(ctx, "SELECT count(*) FROM logs").Scan(&count) != nil {
		return nil
	}
	next := &codexTierSnapshot{stamp: stamp, file: info, last: high, count: count, rows: map[int64]codexTierEvidence{}, turns: map[string]map[string]string{}, revisions: map[string]string{}}
	from := int64(0)
	if old != nil && os.SameFile(old.file, info) && high > old.last {
		var appended int64
		if tx.QueryRowContext(ctx, "SELECT count(*) FROM logs WHERE id > ?", old.last).Scan(&appended) != nil {
			return nil
		}
		// A deletion, including pruning with gaps, rebuilds from retained
		// evidence. Appends alone can reuse the prior parsed metadata.
		if count == old.count+appended {
			from, next.rows = old.last, maps.Clone(old.rows)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, coalesce(thread_id,''), feedback_log_body FROM logs
		WHERE id > ? AND length(feedback_log_body) <= 1048576 AND
		(target = 'feedback_tags' OR target LIKE 'codex_%') AND
		instr(feedback_log_body, 'service_tier') > 0 ORDER BY id`, from)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var thread, body string
		if rows.Scan(&id, &thread, &body) != nil {
			return nil
		}
		if evidence, ok := parseCodexTier(body, thread); ok {
			next.rows[id] = evidence
			if len(next.rows) > 100000 {
				return nil
			}
		}
	}
	if rows.Err() != nil {
		return nil
	}
	for _, e := range next.rows {
		turns := next.turns[e.Thread]
		if turns == nil {
			turns = map[string]string{}
			next.turns[e.Thread] = turns
		}
		if prior, ok := turns[e.Turn]; ok && prior != e.Tier {
			turns[e.Turn] = "" // a turn whose requests disagree is not one known tier
		} else {
			turns[e.Turn] = e.Tier
		}
	}
	for thread, turns := range next.turns {
		b, _ := json.Marshal(turns)
		next.revisions[thread] = fmt.Sprintf("%x", sha256.Sum256(b))
	}
	return next
}

var codexTierField = regexp.MustCompile(`(?:^|[\s{:,])(?:thread_id|thread\.id|turn_id|turn\.id|submission\.id)=(?:"([^"]+)"|([^\s},:]+))`)
var codexSubmissionID = regexp.MustCompile(`^\s*id:\s*"([^"]+)"`)
var codexSubmissionTier = regexp.MustCompile(`service_tier:\s*Some\(Some\("([a-z]+)"\)\)`)

func parseCodexTier(body, thread string) (codexTierEvidence, bool) {
	e := codexTierEvidence{Thread: thread}
	var prefix string
	if before, raw, ok := strings.Cut(body, "tags_json="); ok {
		prefix = before
		var tags struct {
			Tier string `json:"service_tier"`
		}
		if json.Unmarshal([]byte(raw), &tags) != nil {
			return e, false
		}
		e.Tier = tags.Tier
	} else if before, raw, ok := strings.Cut(body, "websocket request:"); ok {
		prefix = before
		var request struct {
			Type string `json:"type"`
			Tier string `json:"service_tier"`
			Turn string `json:"turn_id"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(raw)), &request) != nil || request.Type != "response.create" {
			return e, false
		}
		e.Tier, e.Turn = request.Tier, request.Turn
	} else if before, raw, ok := strings.Cut(body, "Submission sub=Submission {"); ok {
		prefix = before
		id, tier := codexSubmissionID.FindStringSubmatch(raw), codexSubmissionTier.FindStringSubmatch(raw)
		if len(id) == 0 || len(tier) == 0 {
			return e, false
		}
		e.Turn, e.Tier = id[1], tier[1]
	} else {
		return e, false
	}
	for _, match := range codexTierField.FindAllStringSubmatch(prefix, -1) {
		name, _, _ := strings.Cut(strings.TrimLeft(match[0], " \t\n{:,"), "=")
		value := match[1]
		if value == "" {
			value = match[2]
		}
		switch name {
		case "thread_id", "thread.id":
			if e.Thread != "" && e.Thread != value {
				return e, false
			}
			e.Thread = value
		case "turn_id", "turn.id":
			if e.Turn != "" && e.Turn != value {
				return e, false
			}
			e.Turn = value
		}
	}
	switch e.Tier {
	case "fast":
		e.Tier = "priority"
	case "priority", "default", "ultrafast", "flex":
	default:
		return e, false
	}
	if e.Thread == "" || e.Turn == "" || len(e.Thread) > 128 || len(e.Turn) > 128 {
		return e, false
	}
	// Regex captures are substrings of body. Copy the short metadata so a
	// cached ID cannot keep an entire request (and its prompt) alive.
	e.Thread, e.Turn, e.Tier = strings.Clone(e.Thread), strings.Clone(e.Turn), strings.Clone(e.Tier)
	return e, true
}

func codexCallsWithTiers(calls []Call, path string) []Call {
	if len(calls) == 0 || calls[0].Agent != "codex" {
		return calls
	}
	actors := map[string]bool{}
	for _, c := range calls {
		if c.ThreadID != "" {
			actors[c.ThreadID] = true
		}
	}
	ids := make([]string, 0, len(actors))
	for id := range actors {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	callsMu.Lock()
	callTierThreads[path] = ids
	callsMu.Unlock()
	snapshot := codexTiersForHome(codexHomeOf(path))
	if snapshot == nil {
		return calls
	}
	var out []Call
	for i, c := range calls {
		if c.ServiceTier != "" {
			continue
		}
		if tier := snapshot.turns[c.ThreadID][c.TurnID]; tier != "" {
			if out == nil {
				out = slices.Clone(calls)
			}
			out[i].ServiceTier = tier
		}
	}
	if out != nil {
		return out
	}
	return calls
}
