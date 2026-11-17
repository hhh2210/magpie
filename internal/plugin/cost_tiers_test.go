package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Both configured models and a models hook carry context tiers through the host.
func TestPluginCostTiers(t *testing.T) {
	sandbox(t)
	file := filepath.Join(t.TempDir(), "price.mjs")
	code := `export default async input => {
 if (input.magpie?.capabilities?.modelCost !== 1) throw new Error("complete model pricing capability missing")
 return ({
 config: async c => { c.provider ??= {}; c.provider.prices = {name:"Prices",models:{configured:{id:"configured",cost:{input:2,output:12,cache_read:0.2,preferred:true,tiers:[{above:200000,input:4,output:18,cache_read:0.4}]}}}} },
 auth: {provider:"prices", methods:[{type:"api",label:"Key"}]}, provider: {id:"prices", models: async (p) => ({...p.models, hooked:{id:"hooked",cost:{input:2,output:12,cache:{read:0.2},preferred:true,tiers:[{above:200000,input:4,output:18,cache_read:0.4},{above:400000,input:6,cache_read:0}]}}})}
}) }`
	if err := os.WriteFile(file, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := Add(ctx, file); err != nil {
		t.Fatal(err)
	}
	if _, err := APIKey(ctx, "prices", 0, nil, "test-key", NewAccount); err != nil {
		t.Fatal(err)
	}
	ps, err := Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || len(ps[0].Models) != 2 {
		t.Fatalf("models = %+v", ps)
	}
	for _, m := range ps[0].Models {
		n := 1
		if m.ID == "hooked" {
			n = 2
		}
		if m.Cost == nil || !m.Cost.Preferred || m.Cost.Cache.Read != 0.2 || len(m.Cost.Tiers) != n {
			t.Fatalf("%s lost price metadata: %+v", m.ID, m.Cost)
		}
		tier := m.Cost.Tiers[0]
		if tier.Above != 200000 || tier.Input != 4 || tier.Output != 18 || tier.CacheRead != 0.4 {
			t.Fatalf("%s tier = %+v", m.ID, tier)
		}
		if n == 2 {
			tier = m.Cost.Tiers[1]
			if tier.Above != 400000 || tier.Input != 6 || tier.Output != 12 || tier.CacheRead != 0 {
				t.Fatalf("tier inherits base output but keeps explicit free cache: %+v", tier)
			}
		}
	}
}
