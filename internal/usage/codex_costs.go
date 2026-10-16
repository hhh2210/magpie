package usage

import (
	"slices"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// PriceCodexSession uses the same per-request estimates as Requests. Native
// summary files aggregate tokens before pricing and cannot distinguish Fast
// turns or long-context calls. Their identity and activity remain unchanged.
func PriceCodexSession(s *sessions.Session, parts []sessions.ModelEffort) {
	if s.Agent != "codex" || len(parts) == 0 {
		return
	}
	s.ModelEffort = slices.Clone(parts)
	s.Cost, s.Unpriced = 0, 0
	models := map[string]sessions.Model{}
	for _, p := range parts {
		s.Cost += p.Cost
		m, found := models[p.Model]
		if !found {
			m.Model, m.Priced = p.Model, true
		}
		m.Input += p.Input
		m.Output += p.Output
		m.CacheRead += p.CacheRead
		m.CacheWrite += p.CacheWrite
		m.CacheWrite1h += p.CacheWrite1h
		m.Cost += p.Cost
		m.Priced = m.Priced && p.Priced
		models[p.Model] = m
	}
	for _, m := range models {
		if !m.Priced {
			s.Unpriced++
		}
	}
	s.Models = slices.Clone(s.Models)
	for i := range s.Models {
		m, ok := models[s.Models[i].Model]
		if ok && m.Tokens == s.Models[i].Tokens {
			s.Models[i].Cost, s.Models[i].Priced = m.Cost, m.Priced
		} else {
			// A routed model may differ from the native model grouping. Its
			// cost is known in ModelEffort, not attributable to this old group.
			s.Models[i].Cost, s.Models[i].Priced = 0, false
		}
	}
}

func priceCodexSummary(s *sessions.Summary, parts []sessions.ModelEffort) {
	if s.Agent != "codex" || len(parts) == 0 {
		return
	}
	s.ModelEffort = slices.Clone(parts)
	s.Cost, s.Priced = 0, true
	for _, p := range parts {
		s.Cost += p.Cost
		s.Priced = s.Priced && p.Priced
	}
}

// PriceCodexStats corrects session ranking and the daily cost series from the
// canonical ledger before Overview sorts them. A day's source-token totals
// must match before its price is replaced: missing native identity must not
// turn a partial ledger into the whole directory's cost.
func PriceCodexStats(st sessions.Stats) sessions.Stats {
	wanted := false
	for _, s := range st.Sessions {
		wanted = wanted || s.Agent == "codex" && !s.Gateway
	}
	if !wanted {
		return st
	}
	period, ok := RangeOf(st.From, st.To)
	if !ok {
		return st
	}
	return priceCodexStats(st, LedgerOf(period, Filter{Agent: "codex"}).Rows)
}

func priceCodexStats(st sessions.Stats, rows []Row) sessions.Stats {
	parts := codexModelEffort(rows)
	cwds := map[string]string{}
	st.Sessions = slices.Clone(st.Sessions)
	for i := range st.Sessions {
		s := &st.Sessions[i]
		if s.Agent != "codex" || s.Gateway {
			continue
		}
		cwds[s.ID] = s.Cwd
		priceCodexSummary(s, parts[s.ID])
	}
	type key struct{ date, cwd, model string }
	type priced struct {
		tokens sessions.Tokens
		cost   float64
		known  bool
	}
	by := map[key]*priced{}
	for _, r := range rows {
		cwd, ok := cwds[r.codexActor]
		if !ok || r.IsRejected() {
			continue
		}
		model := r.Requested
		if model == "" {
			model = r.Model
		}
		k := key{r.Time.In(time.Local).Format(time.DateOnly), cwd, model}
		v := by[k]
		if v == nil {
			v = &priced{known: true}
			by[k] = v
		}
		v.tokens.Input += r.Input
		v.tokens.Output += r.Output
		v.tokens.CacheRead += r.CacheRead
		v.tokens.CacheWrite += r.CacheWrite
		v.tokens.CacheWrite1h += r.CacheWrite1h
		v.cost += r.Cost
		v.known = v.known && r.Priced
	}
	st.Days = slices.Clone(st.Days)
	for i := range st.Days {
		d := &st.Days[i]
		d.Usage = slices.Clone(d.Usage)
		for j := range d.Usage {
			u := &d.Usage[j]
			if u.Agent != "codex" {
				continue
			}
			if v := by[key{d.Date, u.Cwd, u.Model}]; v != nil && v.tokens == u.Tokens {
				u.Cost, u.Priced = v.cost, v.known
			}
		}
	}
	return st
}
