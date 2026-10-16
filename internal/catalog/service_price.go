package catalog

import (
	"regexp"
	"strings"
)

// ServiceTier normalizes OpenAI's two names for Fast without changing the
// raw request/reply evidence. An absent or automatic tier stays unknown.
func ServiceTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "fast", "priority":
		return "priority"
	case "", "auto":
		return ""
	default:
		return strings.ToLower(strings.TrimSpace(tier))
	}
}

var serviceModelDate = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

// ServicePrice estimates a supported OpenAI service tier from its effective
// Standard tariff (including a user's price or provider discount). These are
// public API USD ratios, NOT ChatGPT subscription allowance multipliers.
// Source: https://developers.openai.com/api/docs/pricing (2026-10-16).
// Like CodexBar's codexPriorityCostUSD, apply the model-specific ratio after
// selecting context/cache rates. Unknown premium tariffs are not Standard.
func ServicePrice(model, tier string, standard Price, prompt int) (Price, bool) {
	tier = ServiceTier(tier)
	if tier == "" || tier == "default" {
		return standard, true
	}
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.TrimPrefix(model, "openai/")
	model = serviceModelDate.ReplaceAllString(model, "")
	var factor float64
	long := false
	switch model {
	case "gpt-6-astra", "gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		factor, long = 2, true
	case "gpt-5.5":
		factor = 2.5
	case "gpt-5.4", "gpt-5.4-mini", "gpt-5.2", "gpt-5.3-codex":
		factor = 2
	default:
		return Price{}, false
	}
	switch tier {
	case "priority":
		if !long && prompt > 272000 {
			return Price{}, false
		}
	case "ultrafast":
		if model != "gpt-6-astra" && model != "gpt-6.1-sol" {
			return Price{}, false
		}
		factor = 6
	case "flex":
		// Only models with published Flex rates in the same table.
		if !long && model != "gpt-5.5" && model != "gpt-5.4" && model != "gpt-5.4-mini" && model != "gpt-5.2" {
			return Price{}, false
		}
		factor = .5
	default:
		return Price{}, false
	}
	return standard.Times(factor), true
}
