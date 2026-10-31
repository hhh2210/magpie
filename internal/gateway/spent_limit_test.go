package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A key whose answer says its rate limit is spent (OpenAI's
// x-ratelimit-remaining-requests: 0) sits out until the limit's reset,
// so the next request goes to the next key at once instead of being
// refused by the spent one first. Its headers aren't ones the agent is
// given (PassedReplyHeaders), so they are read from the vendor's reply.
func TestSpentLimitRestsBeforeRefusal(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "json"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			clineRestsCleared(t)
			var mu sync.Mutex
			asked := map[string]int{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				mu.Lock()
				asked[key]++
				mu.Unlock()
				if key == "k-spent" {
					w.Header().Set("X-Ratelimit-Limit-Requests", "500")
					w.Header().Set("X-Ratelimit-Remaining-Requests", "0")
					w.Header().Set("X-Ratelimit-Reset-Requests", "6m0s")
					w.Header().Set("X-Ratelimit-Remaining-Tokens", "29000")
					w.Header().Set("X-Ratelimit-Reset-Tokens", "2s")
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
			}))
			defer srv.Close()
			to, _ := url.Parse(srv.URL)
			p := provider.Provider{ID: "sp", Name: "SP", Chat: "https://api.openai.com/v1", Models: []string{"m1"},
				Key: "k-spent", KeyName: "Spent", Keys: []provider.KeyAccount{{Name: "Fresh", Key: "k-fresh"}}}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			s := New()
			s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				r.URL.Scheme, r.URL.Host = to.Scheme, to.Host
				return http.DefaultTransport.RoundTrip(r)
			})}
			body := `{"model":"sp/m1","messages":[{"role":"user","content":"hi"}]}`
			if stream {
				body = `{"model":"sp/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
			}
			for i := range 2 {
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
				if rec.Code != 200 {
					t.Fatalf("request %d: %d %s", i, rec.Code, rec.Body)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if asked["k-spent"] != 1 || asked["k-fresh"] != 1 {
				t.Fatalf("asked %v, want the spent key once and the next request on the fresh one", asked)
			}
			var rested *Rest
			restingUntil.Lock()
			for k, r := range restingUntil.note {
				if strings.HasSuffix(k, "/m1") && time.Now().Before(restingUntil.m[k]) {
					r := r
					rested = &r
				}
			}
			restingUntil.Unlock()
			if rested == nil || rested.Why != failRate || time.Until(rested.Until).Round(time.Minute) != 6*time.Minute {
				t.Fatalf("rest %+v, want the spent key's model resting the 6m its reset said", rested)
			}
		})
	}
}

// What rests and what doesn't: a limit at 0 with its reset, Anthropic's
// own names and RFC 3339 time, the longest of two spent limits. A limit
// with something left, one with no reset, a subscription's (its windows
// are read from its allowance), Anthropic's unified headers, a provider
// whose rests are off, a relay passing an upstream key's headers on, and
// one vendor's headers from the other's API rest nothing.
func TestSpentRest(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	key := candidate{p: provider.Provider{ID: "k", Key: "x", Chat: "https://api.openai.com/v1"}, model: "m", rest: "k"}
	ant := candidate{p: provider.Provider{ID: "a", Key: "x", Anthropic: "https://api.anthropic.com"}, model: "m", rest: "a"}
	relay := candidate{p: provider.Provider{ID: "r", Key: "x", Chat: "https://relay.example/v1"}, model: "m", rest: "r"}
	for _, tc := range []struct {
		name string
		c    candidate
		h    http.Header
		want time.Duration
	}{
		{"openai requests", key, http.Header{"X-Ratelimit-Remaining-Requests": {"0"}, "X-Ratelimit-Reset-Requests": {"6m0s"}}, 6 * time.Minute},
		{"openai tokens, the longer", key, http.Header{
			"X-Ratelimit-Remaining-Requests": {"0"}, "X-Ratelimit-Reset-Requests": {"20ms"},
			"X-Ratelimit-Remaining-Tokens": {"0"}, "X-Ratelimit-Reset-Tokens": {"1m30s"}}, 90 * time.Second},
		{"anthropic", ant, http.Header{"Anthropic-Ratelimit-Input-Tokens-Remaining": {"0"},
			"Anthropic-Ratelimit-Input-Tokens-Reset": {"2026-10-31T12:00:45Z"}}, 45 * time.Second},
		{"anthropic's from openai", key, http.Header{"Anthropic-Ratelimit-Input-Tokens-Remaining": {"0"},
			"Anthropic-Ratelimit-Input-Tokens-Reset": {"2026-10-31T12:00:45Z"}}, 0},
		{"openai's from anthropic", ant, http.Header{"X-Ratelimit-Remaining-Requests": {"0"}, "X-Ratelimit-Reset-Requests": {"6m0s"}}, 0},
		{"relay", relay, http.Header{"X-Ratelimit-Remaining-Requests": {"0"}, "X-Ratelimit-Reset-Requests": {"6m0s"}}, 0},
		{"left", key, http.Header{"X-Ratelimit-Remaining-Requests": {"3"}, "X-Ratelimit-Reset-Requests": {"6m0s"}}, 0},
		{"no reset", key, http.Header{"X-Ratelimit-Remaining-Requests": {"0"}}, 0},
		{"unified", ant, http.Header{"Anthropic-Ratelimit-Unified-Remaining": {"0"},
			"Anthropic-Ratelimit-Unified-Reset": {"2026-10-31T13:00:00Z"}}, 0},
		{"subscription", candidate{p: provider.Provider{ID: "a", Account: &provider.Account{User: "u"}}, model: "m", rest: "a"},
			http.Header{"X-Ratelimit-Remaining-Requests": {"0"}, "X-Ratelimit-Reset-Requests": {"6m0s"}}, 0},
		{"rests off", candidate{p: provider.Provider{ID: "o", Key: "x", Chat: "https://api.openai.com/v1", LimitRestOff: true}, model: "m", rest: "o"},
			http.Header{"X-Ratelimit-Remaining-Requests": {"0"}, "X-Ratelimit-Reset-Requests": {"6m0s"}}, 0},
	} {
		r, ok := spentRest(tc.c, tc.h, now)
		if got := r.Until.Sub(now); ok != (tc.want > 0) || ok && got != tc.want {
			t.Errorf("%s: rests %v (%v), want %v", tc.name, got, ok, tc.want)
		}
		if ok && r.Key != tc.c.restID() {
			t.Errorf("%s: rests %q, want the model alone (%q)", tc.name, r.Key, tc.c.restID())
		}
	}
}

// A Gemini API key out of a quota counted by the day rests until midnight
// Pacific, when the day's quota comes back, not the 33s its RetryInfo says
// or the quarter hour of a quota that says nothing. A time of day the user
// set for the provider still wins, a quota per minute is still a rate
// limit, and another vendor's PerDay-looking body is as before.
func TestGeminiDayQuotaRestsUntilMidnightPacific(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	allowances = func(string) map[string]provider.Allowance { return nil }
	body := []byte(`{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details.\n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 50, model: gemini-2.5-pro\nPlease retry in 33.517s.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_requests","quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaDimensions":{"location":"global","model":"gemini-2.5-pro"},"quotaValue":"50"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"33s"}]}}`)
	h := http.Header{"Retry-After": {"33"}}
	pt, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	n := time.Now().In(pt)
	midnight := time.Date(n.Year(), n.Month(), n.Day()+1, 0, 0, 0, 0, pt)
	google := provider.Provider{ID: "google", Chat: "https://generativelanguage.googleapis.com/v1beta/openai", Key: "k"}
	s := &Server{}

	clineRestsCleared(t)
	r := s.restAfter(candidate{p: google, model: "gemini-2.5-pro", rest: google.ID}, 429, h, body)
	if r.Why != failQuota || r.Until.Sub(midnight).Abs() > 5*time.Second {
		t.Errorf("google: rests until %v as %s by %s, want midnight Pacific %v", r.Until, r.Why, r.By, midnight)
	}

	clineRestsCleared(t)
	own := google
	own.ID, own.QuotaResetAt = "google-own", "08:00Z"
	r = s.restAfter(candidate{p: own, model: "gemini-2.5-pro", rest: own.ID}, 429, h, body)
	if want, _ := provider.NextResetAt(own.QuotaResetAt, time.Now()); r.Until.Sub(want).Abs() > 5*time.Second {
		t.Errorf("own reset: rests until %v, want the user's %v", r.Until, want)
	}

	clineRestsCleared(t)
	other := provider.Provider{ID: "relay", Chat: "https://relay.example/v1", Key: "k"}
	r = s.restAfter(candidate{p: other, model: "gemini-2.5-pro", rest: other.ID}, 429, h, body)
	if left := time.Until(r.Until); r.Why != failQuota || left > quotaRest+time.Minute {
		t.Errorf("relay: rests %v by %s, want the quota's rest as before", left, r.By)
	}

	clineRestsCleared(t)
	minute := []byte(strings.Replace(string(body), "GenerateRequestsPerDayPerProjectPerModel-FreeTier", "GenerateRequestsPerMinutePerProjectPerModel-FreeTier", 1))
	r = s.restAfter(candidate{p: google, model: "gemini-2.5-pro", rest: google.ID}, 429, h, minute)
	if left := time.Until(r.Until); r.Why != failRate || left > 2*time.Minute {
		t.Errorf("per minute: rests %v as %s, want a rate limit's minute", left, r.Why)
	}
}
