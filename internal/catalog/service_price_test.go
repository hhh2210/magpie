package catalog

import (
	"math"
	"testing"
)

func TestServiceTierPrices(t *testing.T) {
	// Published GPT-6.1 Sol Standard prices; cache reads/writes are separate
	// and the whole prompt switches rates above 272K.
	p := Price{Input: 2, Output: 10, CacheRead: .1, CacheWrite: 2.5,
		Tiers: []Tier{{Above: 272000, Input: 4, Output: 15, CacheRead: .2, CacheWrite: 5}}}
	for _, tt := range []struct {
		model, tier string
		in          int
		factor      float64
		known       bool
	}{
		{"gpt-6.1-sol", "default", 1000, 1, true},
		{"gpt-6.1-sol", "priority", 1000, 2, true},
		{"gpt-6.1-sol", "fast", 1000, 2, true},
		{"gpt-6.1-sol", "ultrafast", 1000, 6, true},
		{"gpt-6.1-sol", "flex", 1000, .5, true},
		{"gpt-6.1-sol", "priority", 272001, 2, true},
		{"gpt-6-astra", "ultrafast", 272001, 6, true},
		{"openai/gpt-5.5-2026-04-23", "priority", 1000, 2.5, true},
		{"gpt-5.4", "priority", 272000, 2, true},
		{"gpt-5.4", "priority", 272001, 0, false},
		{"gpt-6-sol", "ultrafast", 1000, 0, false},
		{"gpt-6-astra", "scale", 1000, 1, true},
		{"claude-sonnet-4-5", "standard", 1000, 1, true},
		{"llama-3.3-70b", "on_demand", 1000, 1, true},
		{"old-model", "vendor-specific", 272001, 1, true},
		{"gpt-4.1", "priority", 1000, 0, false},
		{"claude-opus-4-6", "fast", 1000, 0, false},
		{"unknown/gpt-6-astra", "priority", 1000, 0, false},
		{"gpt-6-astra-lookalike", "priority", 1000, 0, false},
		{"old-model", "", 1000, 1, true},
	} {
		t.Run(tt.model+"/"+tt.tier, func(t *testing.T) {
			got, ok := ServicePrice(tt.model, tt.tier, p, tt.in)
			if ok != tt.known {
				t.Fatalf("known=%v, want %v", ok, tt.known)
			}
			if !ok {
				return
			}
			want := p.Cost(tt.in-600, 200, 500, 100) * tt.factor
			if cost := got.Cost(tt.in-600, 200, 500, 100); math.Abs(cost-want) > 1e-10 {
				t.Fatalf("tier cost %g, want %g", cost, want)
			}
		})
	}
	if p.Input != 2 || p.Tiers[0].Input != 4 {
		t.Fatal("tier pricing mutated the shared Standard tariff")
	}
}
