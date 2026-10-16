package usage

import (
	"database/sql"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
)

func tierPrices(t *testing.T) {
	t.Helper()
	pageHome(t)
	s := settings.Load()
	s.ModelPrices = map[string]settings.ModelPrice{
		"*/gpt-6.1-sol": {Input: new(2.0), Output: new(10.0), CacheRead: new(.1), CacheWrite: new(2.5)},
		"*/gpt-5.4":     {Input: new(2.5), Output: new(15.0), CacheRead: new(.25), CacheWrite: new(0.0)},
	}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

func TestServiceTierNativePageRefresh(t *testing.T) {
	tierPrices(t)
	now := time.Now().Add(-time.Minute)
	path := filepath.Join(sessions.CodexDir(), "sessions", "2026", "10", "16", "rollout-2026-10-16T12-00-00-tier-main.jsonl")
	writeCodexCall(t, path, "tier-main", "tier-main", `"cli"`, "tier-response", "gpt-6.1-sol", "high", now, 1000000, 0)
	check := func(want string, cost float64) {
		t.Helper()
		page := QueryPage(All, Filter{}, 0, 10)
		if len(page.Rows) != 1 || page.Rows[0].ServiceTier() != want || page.Rows[0].Cost != cost {
			t.Fatalf("native request: %+v", page.Rows)
		}
		parts := CodexModelEffort(All)["tier-main"]
		if len(parts) != 1 || parts[0].ServiceTier != want || parts[0].Cost != cost {
			t.Fatalf("native session shares: %+v", parts)
		}
	}
	check("", 2)
	db, err := sql.Open("sqlite", filepath.Join(sessions.CodexDir(), "logs_2.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE logs(id INTEGER PRIMARY KEY, target TEXT, thread_id TEXT, feedback_log_body TEXT);
	INSERT INTO logs VALUES (1,'feedback_tags','tier-main','turn{turn.id=turn-tier-response}: tags_json={"service_tier":"priority"}')`)
	if err != nil {
		t.Fatal(err)
	}
	check("priority", 4)
	if _, err = db.Exec("DELETE FROM logs"); err != nil {
		t.Fatal(err)
	}
	check("", 2)
}

func TestServiceTierEvidenceAndPrice(t *testing.T) {
	tierPrices(t)
	price := Pricer()
	for _, tt := range []struct {
		name, provider, asked, sent, served, tier string
		factor                                    float64
	}{
		{"default", "openai", "default", "default", "default", "default", 1},
		{"fast alias", "openai", "fast", "fast", "priority", "priority", 2},
		{"API downgrade", "openai", "priority", "priority", "default", "default", 1},
		{"ChatGPT echo", "codex", "priority", "priority", "default", "priority", 2},
		{"account fallback", "codex", "ultrafast", "priority", "default", "priority", 2},
		{"relay strips", "relay", "priority", "default", "", "default", 1},
		{"relay removes parameter", "relay", "priority", "", "", "", 1},
		{"native requested", UnknownProvider, "priority", "", "", "priority", 2},
		{"unknown historical", "codex", "", "", "", "", 1},
		{"unknown premium", "openai", "turbo", "turbo", "", "turbo", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := Record{Provider: tt.provider, Model: "gpt-6.1-sol", TierAsked: tt.asked, TierSent: tt.sent, TierServed: tt.served, Input: 1000000}
			if r.ServiceTier() != tt.tier {
				t.Fatalf("tier=%q, want %q", r.ServiceTier(), tt.tier)
			}
			p := price(r)
			if tt.factor == 0 {
				if p != nil {
					t.Fatal("unsupported premium was priced as Standard")
				}
				return
			}
			if p == nil || r.CostAt(*p) != 2*tt.factor {
				t.Fatalf("cost should be %g; tariff=%+v", 2*tt.factor, p)
			}
			if r.TierAsked != tt.asked || r.TierServed != tt.served {
				t.Fatal("raw evidence rewritten")
			}
		})
	}
	r := Record{Model: "gpt-6.1-sol", Input: 100, TierAsked: "ultrafast", OwnCost: new(0.03)}
	if p := price(r); p == nil || r.CostAt(*p) != .03 {
		t.Fatal("reported cost was multiplied")
	}
}

func TestServiceTierPackedLedgerAndSummary(t *testing.T) {
	tierPrices(t)
	now := time.Now()
	// Enough rows to pass through the compressed block path, interleaved
	// so caching by model alone gives the wrong cost for one of the tiers.
	for i := 0; i < 300; i++ {
		tier := "default"
		if i%2 == 0 {
			tier = "priority"
		}
		Append(Record{Time: now.Add(time.Duration(-i) * time.Second), Agent: "codex", Provider: "codex", Model: "gpt-6.1-sol", Input: 1000000, Status: 200, TierAsked: tier, TierSent: tier, TierServed: "default"})
	}
	for _, tier := range []string{"default", "priority"} {
		page := QueryPage(All, Filter{Tier: tier}, 0, 500)
		want := 2.0
		if tier == "priority" {
			want = 4
		}
		if page.Total != 150 || len(page.Rows) != 150 || math.Abs(page.Sum.Cost-150*want) > 1e-9 {
			t.Fatalf("%s: total=%d rows=%d cost=%g", tier, page.Total, len(page.Rows), page.Sum.Cost)
		}
		for _, row := range page.Rows {
			if row.Cost != want || row.TierServed != "default" {
				t.Fatalf("wrong packed row: %+v", row)
			}
		}
		var csv strings.Builder
		if err := WriteCSV(&csv, LedgerOf(All, Filter{Tier: tier}).Rows); err != nil {
			t.Fatal(err)
		}
		if strings.Count(csv.String(), "\n") != 151 {
			t.Fatal("CSV ignored tier filter")
		}
	}
	if sum := Summarize(All); sum.Cost != 900 {
		t.Fatalf("summary cost=%g, want 900", sum.Cost)
	}
	if page := QueryPage(All, Filter{Tier: "unknown"}, 0, 10); page.Total != 0 {
		t.Fatal("known defaults became unknown")
	}
}

func TestServiceTierContextBoundaryDoesNotSharePrice(t *testing.T) {
	tierPrices(t)
	for i, input := range []int{272000, 272001, 272000} {
		Append(Record{Time: time.Now().Add(-time.Duration(i) * time.Second), Agent: "codex", Provider: "openai", Model: "gpt-5.4", Input: input, Status: 200, TierSent: "priority"})
	}
	p := QueryPage(All, Filter{}, 0, 10)
	if len(p.Rows) != 3 || !p.Rows[0].Priced || p.Rows[1].Priced || !p.Rows[2].Priced {
		t.Fatalf("context boundaries: %+v", p.Rows)
	}
}
