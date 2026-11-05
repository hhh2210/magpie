package cli

import (
	"fmt"
	"github.com/yetone/magpie/internal/provider"
)

func glmAutoResetCmd(args []string) error {
	if len(args) < 1 || len(args) > 2 || len(args) == 2 && args[1] != "on" && args[1] != "off" {
		return fmt.Errorf("magpie provider auto-reset <id> [on|off] (personal GLM Coding Plan, one key)")
	}
	p, err := provider.Find(args[0])
	if err != nil {
		return err
	}
	if len(p.KeysOn()) != 1 && !(len(args) == 2 && args[1] == "off") {
		return fmt.Errorf("select a provider with exactly one key before enabling auto-reset")
	}
	if len(args) == 2 {
		for _, key := range p.KeysOn() {
			copy := *p
			copy.Key = key.Key
			if err = provider.SetGLMAutoReset(copy, args[1] == "on"); err != nil {
				return err
			}
		}
	}
	on, err := provider.GLMAutoReset(*p)
	if err != nil {
		return err
	}
	state := "off"
	if on {
		state = "on: spend a matching reset only when quota is exhausted and no route remains"
	}
	fmt.Printf("%s auto-reset: %s\n", p.Name, state)
	return nil
}
