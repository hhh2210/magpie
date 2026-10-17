package usage

import (
	"math"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

func TestServiceTierCodexSessionCosts(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)
	rows := []Row{
		{Record: Record{Time: now, Agent: "codex", Provider: UnknownProvider, Model: "gpt-6.1-sol", Requested: "gpt-6.1-sol", Effort: "high", TierAsked: "default", Input: 1000}, codexActor: "s", Cost: .002, Priced: true},
		{Record: Record{Time: now, Agent: "codex", Provider: UnknownProvider, Model: "gpt-6.1-sol", Requested: "gpt-6.1-sol", Effort: "high", TierAsked: "priority", Input: 1000}, codexActor: "s", Cost: .004, Priced: true},
	}
	parts := codexModelEffort(rows)
	if len(parts["s"]) != 2 || parts["s"][0].ServiceTier != "default" || parts["s"][1].ServiceTier != "priority" {
		t.Fatalf("tier shares collapsed: %+v", parts)
	}
	s := sessions.Session{Agent: "codex", ID: "s", Cost: .004, Models: []sessions.Model{{Model: "gpt-6.1-sol", Tokens: sessions.Tokens{Input: 2000}, Cost: .004, Priced: true}}}
	PriceCodexSession(&s, parts["s"])
	if math.Abs(s.Cost-.006) > 1e-12 || len(s.ModelEffort) != 2 || s.Models[0].Cost != .006 {
		t.Fatalf("session totals: %+v", s)
	}
	st := sessions.Stats{From: "2026-10-16", To: "2026-10-16",
		Sessions: []sessions.Summary{{Agent: "codex", ID: "s", Cwd: "/work", Cost: .004, Priced: true}},
		Days:     []sessions.Day{{Date: "2026-10-16", Usage: []sessions.Usage{{Agent: "codex", Cwd: "/work", Model: "gpt-6.1-sol", Tokens: sessions.Tokens{Input: 2000}, Cost: .004, Priced: true}}}}}
	got := priceCodexStats(st, rows)
	if got.Sessions[0].Cost != .006 || got.Days[0].Usage[0].Cost != .006 {
		t.Fatalf("daily and ranked costs disagree: %+v %+v", got.Sessions, got.Days)
	}
	if st.Sessions[0].Cost != .004 || st.Days[0].Usage[0].Cost != .004 {
		t.Fatal("pricing mutated a published stats snapshot")
	}
	partial := priceCodexStats(st, rows[:1])
	if partial.Days[0].Usage[0].Cost != .004 {
		t.Fatal("partial identity replaced the whole directory's cost")
	}
}
