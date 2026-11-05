package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"
)

type glmResetTransport struct {
	base *url.URL
	rt   http.RoundTripper
}

func (t glmResetTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	u := *r.URL
	c.URL = &u
	c.URL.Scheme = t.base.Scheme
	c.URL.Host = t.base.Host
	return t.rt.RoundTrip(c)
}

func TestGLMAutoReset(t *testing.T) {
	for _, mode := range []string{"success", "disabled", "not-full", "missing-id", "expired", "unknown-state", "failed-use", "five-hour", "no-matching-card"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			var mu sync.Mutex
			spent := 0
			full := true
			p := Provider{ID: "glm-" + mode, Name: "GLM", Chat: "https://open.bigmodel.cn/api/coding/paas/v4", Key: "test-" + mode}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Header.Get("Authorization") != p.Key {
					t.Error("wrong key")
				}
				switch r.URL.Path {
				case "/api/monitor/usage/quota/limit":
					used := 0
					if full && mode != "not-full" {
						used = 100
					}
					unit, number := 6, 1
					if mode == "five-hour" || mode == "no-matching-card" {
						unit, number = 3, 5
					}
					fmt.Fprintf(w, `{"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":%d,"number":%d,"percentage":%d,"nextResetTime":%d}]}}`, unit, number, used, time.Now().Add(time.Hour).UnixMilli())
				case "/api/biz/customer-package-reset/list":
					if r.URL.Query().Get("targetType") != "PERSONAL" {
						t.Error("not personal")
					}
					id := 12
					if mode == "missing-id" {
						id = 0
					}
					expiry := time.Now().Add(24 * time.Hour)
					if mode == "expired" {
						expiry = time.Now().Add(-time.Hour)
					}
					field := "weekResets"
					if mode == "five-hour" {
						field = "fiveHourResets"
					}
					fmt.Fprintf(w, `{"code":200,"data":{%q:[{"recordId":%d,"grantType":"GLOBAL","available":true,"expireTime":%q}]}}`, field, id, expiry.In(time.FixedZone("UTC+8", 28800)).Format("2006-01-02 15:04:05"))
				case "/api/biz/customer-package-reset/use":
					spent++
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					want := "WEEK"
					if mode == "five-hour" {
						want = "FIVE_HOUR"
					}
					if body["recordId"] != float64(12) || body["grantType"] != "GLOBAL" || body["resetType"] != want || body["targetType"] != "PERSONAL" || body["requestId"] == "" {
						t.Errorf("bad body: %v", body)
					}
					if mode == "failed-use" {
						w.WriteHeader(502)
						return
					}
					full = false
					fmt.Fprint(w, `{"code":200,"success":true,"data":2499212}`)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			u, _ := url.Parse(srv.URL)
			old := http.DefaultClient.Transport
			http.DefaultClient.Transport = glmResetTransport{u, http.DefaultTransport}
			defer func() { http.DefaultClient.Transport = old }()
			if err := SetGLMAutoReset(p, mode != "disabled"); err != nil {
				t.Fatal(err)
			}
			_, path, _ := glmResetLocation(p)
			if mode == "unknown-state" {
				os.WriteFile(path, []byte("{"), 0600)
			}
			out, err := AutoUseGLMReset(context.Background(), p)
			wantSpend := mode == "success" || mode == "five-hour" || mode == "failed-use"
			if (spent == 1) != wantSpend {
				t.Fatalf("spent %d, expected attempt %v (out=%+v err=%v)", spent, wantSpend, out, err)
			}
			if mode == "success" || mode == "five-hour" {
				if err != nil || out.Code != "reset" {
					t.Fatalf("not recovered: %+v %v", out, err)
				}
			}
			// A subsequent fresh exhaustion, restart (disk reread), or toggle cannot
			// spend another card before the original window ends.
			if wantSpend {
				mu.Lock()
				full = true
				mu.Unlock()
				if err := SetGLMAutoReset(p, false); err != nil {
					t.Fatal(err)
				}
				if err := SetGLMAutoReset(p, true); err != nil {
					t.Fatal(err)
				}
				var wg sync.WaitGroup
				for range 4 {
					wg.Add(1)
					go func() { defer wg.Done(); AutoUseGLMReset(context.Background(), p) }()
				}
				wg.Wait()
				if spent != 1 {
					t.Fatalf("duplicate spend: %d", spent)
				}
			}
		})
	}
}

func TestGLMResetOptInFollowsKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := Provider{ID: "glm", Name: "GLM", Chat: "https://open.bigmodel.cn/api/coding/paas/v4", Key: "identity-test"}
	if err := SetGLMAutoReset(p, true); err != nil {
		t.Fatal(err)
	}
	alias := p
	alias.ID = "renamed"
	if on, err := GLMAutoReset(alias); err != nil || !on {
		t.Fatalf("same key lost opt-in: %v %v", on, err)
	}
	alias.Key = "replacement-key"
	if on, err := GLMAutoReset(alias); err != nil || on {
		t.Fatalf("new key inherited opt-in: %v %v", on, err)
	}
	alias = p
	alias.Chat = "https://api.z.ai/api/coding/paas/v4"
	if on, err := GLMAutoReset(alias); err != nil || on {
		t.Fatalf("different vendor inherited opt-in: %v %v", on, err)
	}
	alias = p
	alias.Chat = "https://open.bigmodel.cn/api/paas/v4"
	if err := SetGLMAutoReset(alias, true); err == nil {
		t.Fatal("pay-as-you-go opted in")
	}
	alias = p
	alias.ZhipuTeam = &ZhipuTeam{Org: "team"}
	if err := SetGLMAutoReset(alias, true); err == nil {
		t.Fatal("team opted in")
	}
}
