package pricing

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (s *Service) exactPricing(ctx context.Context, model string, at time.Time) (ModelPricing, error) {
	p, err := s.GetPricing(ctx, model, at)
	if err != nil {
		return p, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.overrides[model]; ok {
		return p, nil
	}
	for _, candidate := range []string{model, ResolveModelName(model)} {
		if _, ok := s.cache[candidate]; ok {
			return p, nil
		}
		for key := range s.cache {
			if normalize(candidate) == normalize(key) {
				return p, nil
			}
		}
	}
	return ModelPricing{}, fmt.Errorf("no exact pricing for %q", model)
}

// GetSourcePricing keeps source-specific naming and vendor billing policy out of
// the adapters' token parsing. Exact raw overrides always take priority.
func (s *Service) GetSourcePricing(ctx context.Context, model, agent string, provider any, at time.Time) (ModelPricing, error) {
	qualified := "[" + agent + "] " + model
	if s.HasOverride(qualified) {
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
	case "hermes", "kilo":
		if providerName != "" && providerName != "hermes" {
			if p, err := s.exactPricing(ctx, providerName+"/"+model, at); err == nil {
				return p, nil
			}
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
		allowed := contains([]string{"zai", "z.ai", "zai-coding-plan", "builtin:zai-coding-plan", "builtin:bigmodel-coding-plan"}, providerName)
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
		return contains([]string{"zai", "z.ai", "zai-coding-plan", "builtin:zai-coding-plan", "builtin:bigmodel-coding-plan"}, name)
	}
	model = strings.ToLower(model)
	return strings.HasPrefix(model, "glm-") || strings.HasPrefix(model, "glm/")
}
