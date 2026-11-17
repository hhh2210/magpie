package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func TestPluginPriceRefreshRepricesHistory(t *testing.T) {
	pageHome(t)
	t.Cleanup(func() { plugin.UseCached(nil); provider.Changed() })
	now := holdClock(t, time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local))
	write := func(name string, value any) {
		t.Helper()
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(settings.Dir(), name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("plugins.json", map[string]any{"plugins": []map[string]string{{"spec": "test-prices"}}})
	write("plugin-auth.json", map[string]any{"price-test": map[string]string{"type": "api", "key": "fixture-only"}})
	Append(Record{Time: now, Agent: "agy", Provider: "price-test", Model: "m", Input: 200001, Output: 1000, Status: 200})
	for _, step := range []struct {
		preferred        bool
		rate, tier, want float64
	}{
		{false, 3, 6, 200001*2/1e6 + 1000*8/1e6},
		{true, 3, 6, 200001*6/1e6 + 1000*12/1e6},
		{true, 3, 7, 200001*7/1e6 + 1000*12/1e6},
	} {
		cost := &plugin.Cost{Input: step.rate, Output: 12, Preferred: step.preferred, Tiers: []catalog.Tier{{Above: 200000, Input: step.tier, Output: 12}}}
		plugin.UseCached([]plugin.Provider{{ID: "price-test", Spec: "test-prices", Methods: []plugin.Method{{Type: "api"}}, Accounts: []plugin.Account{{Key: "price-test", Type: "api"}}, Models: []plugin.Model{{ID: "m", Cost: cost}}}})
		provider.Changed()
		if got := Summarize(Today); got.Cost != step.want || got.Unpriced != 0 {
			t.Fatalf("preferred=%v tier=%g: summary=%+v, want cost=%g", step.preferred, step.tier, got.Totals, step.want)
		}
		if got := QueryPage(Today, Filter{}, 0, 10); got.Sum.Cost != step.want || len(got.Rows) != 1 || !got.Rows[0].Priced {
			t.Fatalf("preferred=%v tier=%g: page=%+v, want cost=%g", step.preferred, step.tier, got.Sum, step.want)
		}
	}
}
