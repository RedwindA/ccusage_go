package pricing

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// hasExactPricing reports whether model as written names an entry of its own
// (exactEntry): upstream's find_exact_with_fallback, which decides whether
// Hermes prices a provider-qualified id.
func (s *Service) hasExactPricing(ctx context.Context, model string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshFor(ctx, model, model)
	_, ok := s.exactEntry(model)
	return ok
}

// hasPrimaryPricing reports whether LiteLLM, a built-in rate or an override
// prices model as written: upstream's find_exact, which decides whether Kilo
// prices a provider-qualified id.
func (s *Service) hasPrimaryPricing(ctx context.Context, model string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh(ctx)
	return hasKey(s.primary, model)
}

// GetSourcePricing keeps source-specific naming and vendor billing policy out of
// the adapters' token parsing. Overrides always take priority: first one the
// "[agent] model" label names, directly or through its alias as upstream's
// has_user_override reads it, then one on the raw model.
func (s *Service) GetSourcePricing(ctx context.Context, model, agent string, provider any, at time.Time) (ModelPricing, error) {
	p, err := s.sourcePricing(ctx, model, agent, provider, at)
	if agent == "codex" && !p.cacheReadExplicit {
		// Like upstream's calculate_codex_bucket_cost, Codex bills cached
		// input at the input rate when the price names no cache-read rate.
		p.CacheReadInputTokenCost = p.InputCostPerToken
		p.CacheReadAbove = p.InputAbove
	}
	return p, err
}

// googlePrefixes are the providers upstream's Gemini and Antigravity
// adapters qualify a Google model with to price it.
var googlePrefixes = []string{"google", "gemini", "vertex_ai", "openrouter/google"}

func (s *Service) sourcePricing(ctx context.Context, model, agent string, provider any, at time.Time) (ModelPricing, error) {
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
	case "gemini":
		candidates = nil
		for _, prefix := range googlePrefixes {
			candidates = append(candidates, prefix+"/"+model)
		}
		candidates = append(candidates, model)
	case "droid", "codebuff", "goose":
		candidates = append(candidates, QualifiedModels(model, agent, provider)...)
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
				for _, prefix := range googlePrefixes {
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

// QualifiedModels are the provider-qualified ids upstream's Droid, Codebuff
// and Goose adapters price model under, in order, when the raw model has no
// rate or bills nothing; other agents have none.
func QualifiedModels(model, agent string, provider any) []string {
	name, _ := provider.(string)
	name = strings.TrimSpace(name)
	switch agent {
	case "droid":
		// droid_model_candidates, after normalize_droid_provider and
		// infer_droid_provider_from_model.
		name = strings.ReplaceAll(strings.ToLower(name), "-", "_")
		switch name {
		case "claude":
			name = "anthropic"
		case "google_ai", "gemini", "vertex", "vertex_ai":
			name = "google"
		case "x_ai", "grok":
			name = "xai"
		case "", "unknown":
			name = droidModelProvider(model)
		}
		switch name {
		case "google":
			return []string{"google/" + model, "vertex_ai/" + model, "openrouter/google/" + model}
		case "xai":
			return []string{"xai/" + model, "openrouter/x-ai/" + model}
		case "unknown":
			return nil
		}
		return []string{name + "/" + model, "openrouter/" + name + "/" + model}
	case "codebuff":
		// Codebuff's infer_provider reads only the model.
		if name = codebuffModelProvider(model); name != "unknown" && !strings.HasPrefix(model, name+"/") {
			return []string{name + "/" + model}
		}
	case "goose":
		// Goose's normalize_provider.
		name = strings.ReplaceAll(name, "-", "_")
		if name == "" {
			name = gooseModelProvider(model)
		}
		if name != "goose" {
			return []string{name + "/" + model}
		}
	}
	return nil
}

func droidModelProvider(model string) string {
	switch {
	case strings.Contains(model, "claude") || strings.Contains(model, "opus") || strings.Contains(model, "sonnet") || strings.Contains(model, "haiku"):
		return "anthropic"
	case strings.HasPrefix(model, "gpt-") || strings.Contains(model, "-gpt-") || strings.Contains(model, "chatgpt") || len(model) > 1 && model[0] == 'o' && isDigit(model[1]):
		return "openai"
	case strings.Contains(model, "gemini"):
		return "google"
	case strings.Contains(model, "grok"):
		return "xai"
	}
	return "unknown"
}

func codebuffModelProvider(model string) string {
	model = strings.ToLower(model)
	hasPrefix := func(prefixes ...string) bool {
		return slices.ContainsFunc(prefixes, func(prefix string) bool { return strings.HasPrefix(model, prefix) })
	}
	switch {
	case hasPrefix("claude-", "anthropic/", "anthropic."):
		return "anthropic"
	case hasPrefix("gpt-", "o1", "o3", "o4", "openai/"):
		return "openai"
	case hasPrefix("gemini", "google/"):
		return "google"
	case hasPrefix("grok", "xai/"):
		return "xai"
	case hasPrefix("openrouter/"):
		return "openrouter"
	}
	return "unknown"
}

func gooseModelProvider(model string) string {
	switch {
	case strings.HasPrefix(model, "claude-"):
		return "anthropic"
	case strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "chatgpt-") || strings.HasPrefix(model, "o"):
		return "openai"
	case strings.HasPrefix(model, "gemini-"):
		return "google"
	case strings.HasPrefix(strings.ToLower(model), "qwen"):
		return "openrouter"
	}
	return "goose"
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
