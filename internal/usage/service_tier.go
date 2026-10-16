package usage

import "github.com/yetone/magpie/internal/catalog"

// ServiceTier is the evidence used for the cost estimate, not a claim that
// the upstream verified Fast. ChatGPT's backend may echo default even for a
// Fast request, so its sent tier takes precedence; an API reply wins elsewhere.
// The raw asked/sent/served fields are never rewritten.
func (r Record) ServiceTier() string {
	if r.Provider == "codex" {
		if tier := catalog.ServiceTier(r.TierSent); tier != "" {
			return tier
		}
	}
	for _, tier := range []string{r.TierServed, r.TierSent} {
		if tier = catalog.ServiceTier(tier); tier != "" {
			return tier
		}
	}
	// A native log records the turn's request, not an HTTP attempt. For
	// gateway rows an empty sent tier may mean the field was stripped on
	// retry, so the original request must never restore its premium.
	if r.Provider == UnknownProvider {
		return catalog.ServiceTier(r.TierAsked)
	}
	return ""
}

func servicePrice(r Record, base *catalog.Price) *catalog.Price {
	if base == nil || r.OwnCost != nil {
		return base
	}
	if tier := r.ServiceTier(); tier == "" || tier == "default" {
		return base
	}
	p, ok := catalog.ServicePrice(r.Model, r.ServiceTier(), *base, r.Input+r.CacheRead+r.CacheWrite)
	if !ok {
		return nil
	}
	return &p
}
