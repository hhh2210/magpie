package gateway

import (
	"fmt"
	"github.com/yetone/magpie/internal/provider"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGLMAutoResetRequest(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := provider.Provider{ID: "glm-reset", Name: "GLM reset", Key: "glm-test", Chat: "https://open.bigmodel.cn/api/coding/paas/v4", Models: []string{"glm-5.1"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetGLMAutoReset(p, true); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	spent, tried := 0, 0
	fake := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		code, body := 200, ""
		switch r.URL.Path {
		case "/api/coding/paas/v4/chat/completions":
			tried++
			if spent == 0 {
				code = 429
				body = `{"error":{"code":"1308","message":"Usage limit reached for 5 hour"}}`
			} else {
				body = `{"id":"c","object":"chat.completion","model":"glm-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`
			}
		case "/api/monitor/usage/quota/limit":
			used := 100
			if spent > 0 {
				used = 0
			}
			body = fmt.Sprintf(`{"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":%d,"nextResetTime":%d}]}}`, used, time.Now().Add(time.Hour).UnixMilli())
		case "/api/biz/customer-package-reset/list":
			body = `{"code":200,"data":{"fiveHourResets":[{"recordId":12,"grantType":"GLOBAL","available":true,"expireTime":"2099-12-22 23:59:59"}]}}`
		case "/api/biz/customer-package-reset/use":
			spent++
			body = `{"code":200,"success":true,"data":12}`
		default:
			code = 404
			body = `{}`
		}
		return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = fake
	defer func() { http.DefaultClient.Transport = old }()
	s := New()
	s.client = &http.Client{Transport: fake}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-reset/glm-5.1","messages":[{"role":"user","content":"ping"}],"stream":false}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") || spent != 1 || tried != 2 {
		t.Fatalf("status=%d body=%s spent=%d tried=%d", rec.Code, rec.Body.String(), spent, tried)
	}
	if got := resetsTraced(s); len(got) != 1 || !strings.Contains(got[0], "GLM reset") {
		t.Fatalf("trace: %v", got)
	}
}
