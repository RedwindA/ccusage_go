package pricing

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// hasExactPricing reports whether model as written names an entry of its own
// (exactEntry) or an override: upstream's find_exact_with_fallback, which
// decides whether Hermes prices a provider-qualified id.
func (s *Service) hasExactPricing(ctx context.Context, model string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshFor(ctx, model, model)
	_, ok := s.exactEntry(model)
	return ok || hasKey(s.overrides, model)
}

// hasPrimaryPricing reports whether LiteLLM, a built-in rate or an override
// prices model as written: upstream's find_exact, which decides whether Kilo
// prices a provider-qualified id.
func (s *Service) hasPrimaryPricing(ctx context.Context, model string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh(ctx)
	return hasKey(s.primary, model) || hasKey(s.overrides, model)
}

// GetSourcePricing keeps source-specific naming and vendor billing policy out of
// the adapters' token parsing. Overrides always take priority: first one the
// "[agent] model" label names, directly or through its alias as upstream's
// has_user_override reads it, then one on the raw model.
func (s *Service) GetSourcePricing(ctx context.Context, model, agent string, provider any, at time.Time) (ModelPricing, error) {
	qualified := "[" + agent + "] " + model
	if s.HasOverride(qualified) || s.HasOverride(ResolveModelName(qualified)) {
		p, err := s.GetPricing(ctx, qualified, at)
		if agent == "zcode" && zcodeIsZai(model, provider) {
			p = zcodePricing(p)
		}
		return p, err
	}
	if s.HasOverride(model) {
		p, err := s.GetPricing(ctx, model, at)
		if agent == "zcode" && zcodeIsZai(model, provider) {
			p = zcodePricing(p)
		}
		return p, err
	}
	providerName, _ := provider.(string)
	candidates := []string{model}
	exactOnly := false
	switch agent {
	case "hermes":
		providerName = hermesProvider(providerName, model)
		if id := providerName + "/" + model; providerName != "hermes" && s.hasExactPricing(ctx, id) {
			candidates = []string{id, model}
		}
	case "kilo":
		providerName = strings.ReplaceAll(providerName, "-", "_")
		if id := providerName + "/" + model; providerName != "" && providerName != "unknown" && providerName != "kilo" && s.hasPrimaryPricing(ctx, id) {
			candidates = []string{id, model}
		}
	case "kimi":
		candidates = []string{"moonshot/" + model, "kimi/" + model, model}
		if model == "kimi-for-coding" {
			alias := "moonshot/kimi-k2.6"
			if at.UnixMilli() < 1776698890072 {
				alias = "moonshot/kimi-k2.5"
			}
			candidates = append([]string{alias}, candidates...)
		}
	case "opencode":
		switch model {
		case "k2p6":
			model = "kimi-k2.6"
		case "gemini-3-pro-high":
			model = "gemini-3-pro-preview"
		}
		normalized := model
		for _, family := range []string{"claude-haiku-", "claude-opus-", "claude-sonnet-"} {
			if strings.HasPrefix(model, family) {
				rest := strings.TrimPrefix(model, family)
				if len(rest) >= 2 && rest[0] >= '0' && rest[0] <= '9' && rest[1] >= '0' && rest[1] <= '9' {
					normalized = family + rest[:1] + "-" + rest[1:]
				} else {
					normalized = strings.ReplaceAll(model, ".", "-")
				}
			}
		}
		candidates = []string{model, normalized}
		if providerName != "" && providerName != "unknown" {
			providerName = strings.ReplaceAll(providerName, "-", "_")
			candidates = append(candidates, providerName+"/"+model, providerName+"/"+normalized)
		}
	case "zcode":
		lower := strings.ToLower(strings.TrimSpace(model))
		providerName = strings.ToLower(strings.TrimSpace(providerName))
		allowed := zcodeZaiProvider(providerName)
		candidates = []string{model, lower}
		if allowed || (providerName == "" && (strings.HasPrefix(lower, "glm-") || strings.HasPrefix(lower, "glm/"))) {
			candidates = append(candidates, "zai/"+model, "zai/"+lower)
		}
		for _, candidate := range candidates {
			if s.HasOverride(candidate) {
				p, err := s.GetPricing(ctx, candidate, at)
				if zcodeIsZai(model, provider) {
					p = zcodePricing(p)
				}
				return p, err
			}
		}
		if providerName != "" && !allowed {
			return ModelPricing{}, fmt.Errorf("zcode provider %q has no supported pricing", providerName)
		}
		for _, candidate := range candidates {
			if strings.HasPrefix(candidate, "zai/") {
				if p, err := s.GetPricing(ctx, candidate, at); err == nil {
					return zcodePricing(p), nil
				}
			}
		}
	case "antigravity":
		bases := []string{model}
		for _, version := range []string{"3.6", "3.7", "3.8"} {
			for _, effort := range []string{"high", "medium", "low"} {
				if model == "gemini-"+version+"-flash-"+effort {
					bases = append(bases, "gemini-"+version+"-flash")
					exactOnly = true
				}
			}
		}
		candidates = nil
		google := false
		switch v := provider.(type) {
		case float64:
			google = v == 3 || v == 24 || v == 30
		case int:
			google = v == 3 || v == 24 || v == 30
		case int64:
			google = v == 3 || v == 24 || v == 30
		}
		for _, base := range bases {
			candidates = append(candidates, base)
			if google {
				for _, prefix := range []string{"google", "gemini", "vertex_ai", "openrouter/google"} {
					candidates = append(candidates, prefix+"/"+base)
				}
			}
		}
	}
	var last error
	for _, candidate := range candidates {
		var p ModelPricing
		var err error
		if exactOnly {
			p, err = s.exactPricing(ctx, candidate, at)
		} else {
			p, err = s.GetPricing(ctx, candidate, at)
		}
		if err == nil {
			if agent == "zcode" && zcodeIsZai(model, provider) {
				p = zcodePricing(p)
			}
			return p, nil
		}
		last = err
	}
	return ModelPricing{}, last
}
func zcodePricing(p ModelPricing) ModelPricing {
	p.CacheCreationAsInput = true
	p.CacheCreationInputTokenCost = p.InputCostPerToken
	p.CacheCreateAbove = p.InputAbove
	return p
}

func zcodeIsZai(model string, provider any) bool {
	name, _ := provider.(string)
	name = strings.ToLower(strings.TrimSpace(name))
	if name != "" {
		return zcodeZaiProvider(name)
	}
	model = strings.ToLower(model)
	return strings.HasPrefix(model, "glm-") || strings.HasPrefix(model, "glm/")
}

// zcodeZaiProvider expects a trimmed, lowercased provider id. Z.ai's current
// plans are stored as account:zai-* and builtin:zai-* ids.
func zcodeZaiProvider(name string) bool {
	return contains([]string{"zai", "z.ai", "zai-coding-plan", "builtin:bigmodel-coding-plan"}, name) ||
		strings.HasPrefix(name, "account:zai-") || strings.HasPrefix(name, "builtin:zai-")
}

// hermesProvider is upstream's Hermes normalize_provider: billing providers
// compare case-insensitively with '_' for '-', a few spellings name one
// vendor, and a missing provider is inferred from the model.
func hermesProvider(provider, model string) string {
	provider = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(provider)), "-", "_")
	switch provider {
	case "":
		model = strings.ToLower(model)
		switch {
		case strings.HasPrefix(model, "claude-") || strings.HasPrefix(model, "claude/"):
			return "anthropic"
		case strings.HasPrefix(model, "gpt") || strings.HasPrefix(model, "chatgpt") || len(model) > 1 && model[0] == 'o' && isDigit(model[1]):
			return "openai"
		case strings.HasPrefix(model, "gemini-") || strings.HasPrefix(model, "gemini/"):
			return "google"
		}
		return "hermes"
	case "claude":
		return "anthropic"
	case "openai_codex":
		return "openai"
	case "google_ai", "gemini", "vertex", "vertex_ai":
		return "google"
	}
	return provider
}
