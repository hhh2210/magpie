package cli

import (
	"github.com/yetone/magpie/internal/provider"
	"testing"
)

func TestGLMAutoResetCLI(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := provider.Provider{ID: "glm-cli", Name: "GLM", Chat: "https://open.bigmodel.cn/api/coding/paas/v4", Key: "cli-test"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"on", "off"} {
		if err := glmAutoResetCmd([]string{p.ID, arg}); err != nil {
			t.Fatal(err)
		}
		on, err := provider.GLMAutoReset(p)
		if err != nil || on != (arg == "on") {
			t.Fatalf("%s: %v %v", arg, on, err)
		}
	}
	if err := glmAutoResetCmd([]string{p.ID, "invalid"}); err == nil {
		t.Fatal("invalid policy accepted")
	}
	p.Keys = []provider.KeyAccount{{Key: "second"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := glmAutoResetCmd([]string{p.ID, "on"}); err == nil {
		t.Fatal("ambiguous keys accepted")
	}
	if err := glmAutoResetCmd([]string{p.ID, "off"}); err != nil {
		t.Fatalf("cannot disable after adding a key: %v", err)
	}
}
