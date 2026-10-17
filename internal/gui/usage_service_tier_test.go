package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

func TestServiceTierUsageAPIs(t *testing.T) {
	sandboxHome(t)
	s := settings.Load()
	s.ModelPrices = map[string]settings.ModelPrice{"*/gpt-6.1-sol": {Input: new(2.0), Output: new(10.0), CacheRead: new(.1), CacheWrite: new(2.5)}}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	for i, tier := range []string{"default", "priority"} {
		usage.Append(usage.Record{Time: time.Now().Add(-time.Duration(i+1) * time.Second), Agent: "codex", Provider: "codex", Model: "gpt-6.1-sol", Input: 1000000, Status: 200, TierAsked: tier, TierSent: tier, TierServed: "default"})
	}
	gui := http.NewServeMux()
	usageRoutes(gui, folderOnly{})
	gw := gateway.New().Handler()
	for name, h := range map[string]http.Handler{"/api/usage/requests": gui, "/v1/magpie/usage/requests": gw} {
		req := httptest.NewRequest("GET", name+"?period=all&tier=priority", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		var page ledgerJSON
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.Total != 1 || page.Cost != 4 || len(page.Rows) != 1 {
			t.Fatalf("%s: %+v", name, page)
		}
		r := page.Rows[0]
		if r.ServiceTier != "priority" || r.TierAsked != "priority" || r.TierSent != "priority" || r.TierServed != "default" || r.Cost != 4 {
			t.Fatalf("%s: fields/cost lost at API: %+v", name, r)
		}
	}
}
