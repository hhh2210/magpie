package provider

// GLM plan keys can spend their own reset cards, independently of ZCode
// sign-ins. The opt-in and write-ahead reservation live together so a
// restart or uncertain response cannot spend a second card in that window.
import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
)

type glmResetState struct {
	Enabled   bool      `json:"enabled"`
	Until     time.Time `json:"until,omitempty"`
	RequestID string    `json:"requestId,omitempty"`
	RecordID  int64     `json:"recordId,omitempty"`
	Window    string    `json:"window,omitempty"`
}

func glmResetLocation(p Provider) (root, path string, err error) {
	src, ok := planQuotaSourceOf(p)
	if !ok || !src.sure || !zhipuHost(hostOf(src.url)) || p.Account != nil || p.Key == "" || p.ZhipuTeam != nil {
		return "", "", fmt.Errorf("auto-reset requires a personal GLM Coding Plan API key")
	}
	root = strings.TrimSuffix(src.url, "/api/monitor/usage/quota/limit")
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(root+"\x00"+p.Key)))
	return root, filepath.Join(settings.Dir(), "glm-auto-reset", id+".json"), nil
}

func readGLMReset(path string) (glmResetState, error) {
	var s glmResetState
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if len(b) == 0 || string(b) == "null" {
		return s, errors.New("unknown GLM auto-reset state")
	}
	err = json.Unmarshal(b, &s)
	return s, err
}
func writeGLMReset(path string, s glmResetState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return steady.WriteFile(path, b, 0600)
}

// GLMAutoReset reports the opt-in for this exact key; a failed read is an error.
func GLMAutoReset(p Provider) (bool, error) {
	_, path, err := glmResetLocation(p)
	if err != nil {
		return false, err
	}
	s, err := readGLMReset(path)
	return s.Enabled, err
}

// SetGLMAutoReset keeps an outstanding reservation even when toggled off/on.
func SetGLMAutoReset(p Provider, enabled bool) error {
	_, path, err := glmResetLocation(p)
	if err != nil {
		return err
	}
	unlock, held := filelock.Lock(path+".lock", time.Second)
	defer unlock()
	if !held {
		return errors.New("GLM auto-reset state is locked")
	}
	s, err := readGLMReset(path)
	if err != nil {
		return err
	}
	s.Enabled = enabled
	return writeGLMReset(path, s)
}

type glmResetCard struct {
	RecordID   int64  `json:"recordId"`
	GrantType  string `json:"grantType"`
	ExpireTime string `json:"expireTime"`
	Available  bool   `json:"available"`
}

// AutoUseGLMReset is called only after all candidates are out of quota.
// It re-reads the personal quota, never spends for MCP limits, and never
// substitutes a weekly card for a five-hour card. At most one attempt is
// made before the exhausted window's original deadline, across processes.
func AutoUseGLMReset(ctx context.Context, p Provider) (ResetOutcome, error) {
	root, path, err := glmResetLocation(p)
	if err != nil {
		return ResetOutcome{}, nil
	}
	s, err := readGLMReset(path)
	if err != nil || !s.Enabled {
		return ResetOutcome{}, err
	}
	unlock, held := filelock.Lock(path+".lock", time.Second)
	defer unlock()
	if !held {
		return ResetOutcome{}, errors.New("GLM auto-reset state is locked")
	}
	s, err = readGLMReset(path)
	if err != nil || !s.Enabled || time.Now().Before(s.Until) {
		return ResetOutcome{}, err
	}
	ctx = p.Via(ctx)
	src, _ := planQuotaSourceOf(p)
	_, ws, err := planWindows(ctx, src, p.Key)
	if err != nil {
		return ResetOutcome{}, err
	}
	var full *QuotaWindow
	for i := range ws {
		w := &ws[i]
		if !w.Aside && w.Used >= 100 && (w.Span == 5*time.Hour || w.Span == 7*24*time.Hour) {
			if w.ResetsAt == nil || !w.ResetsAt.After(time.Now()) {
				return ResetOutcome{}, nil
			}
			if full == nil || w.Span > full.Span {
				full = w
			}
		}
	}
	if full == nil {
		return ResetOutcome{}, nil
	}
	var list struct {
		FiveHour []glmResetCard `json:"fiveHourResets"`
		Week     []glmResetCard `json:"weekResets"`
	}
	if err = zcodeGet(ctx, root+"/api/biz/customer-package-reset/list?targetType=PERSONAL", p.Key, &list); err != nil {
		return ResetOutcome{}, err
	}
	cards, kind, windows := list.FiveHour, "FIVE_HOUR", 1
	if full.Span == 7*24*time.Hour {
		cards, kind, windows = list.Week, "WEEK", 2
	}
	sort.SliceStable(cards, func(i, j int) bool { return cards[i].ExpireTime < cards[j].ExpireTime })
	var selected *glmResetCard
	for i := range cards {
		c := &cards[i]
		until, e := time.ParseInLocation("2006-01-02 15:04:05", c.ExpireTime, time.FixedZone("UTC+8", 8*3600))
		if e == nil && c.Available && c.RecordID > 0 && c.GrantType != "" && until.After(time.Now()) {
			selected = c
			break
		}
	}
	if selected == nil {
		return ResetOutcome{}, nil
	}
	s.Until, s.RequestID, s.RecordID, s.Window = *full.ResetsAt, newRedeemID(), selected.RecordID, kind
	// Reserve before sending. Even a timeout or crash keeps the reservation:
	// the vendor may already have spent the card. No automatic second POST.
	if err = writeGLMReset(path, s); err != nil {
		return ResetOutcome{}, err
	}
	body := map[string]any{"targetType": "PERSONAL", "resetType": kind, "recordId": s.RecordID, "grantType": selected.GrantType, "requestId": s.RequestID}
	if err = zcodeCall(ctx, http.MethodPost, root+"/api/biz/customer-package-reset/use", p.Key, body, nil); err != nil {
		return ResetOutcome{}, err
	}
	_, ws, err = planWindows(ctx, src, p.Key)
	if err != nil {
		return ResetOutcome{}, err
	}
	verified := false
	for _, w := range ws {
		if w.Span == full.Span && !w.Aside {
			verified = w.Used < 100
		}
	}
	if !verified {
		return ResetOutcome{}, errors.New("GLM reset accepted but quota recovery is unconfirmed")
	}
	noteKeyAllowance(p, ws, time.Now())
	ForgetPlanQuotas()
	return ResetOutcome{Code: "reset", Windows: windows}, nil
}
