package pricing

import (
	"context"
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Snapshot pinned by upstream flake.lock: BerriAI/litellm at
// 6f37808d44a3a39ddc9f857343d833537d3b94f5. Models/fields follow upstream build.rs.
//
//go:embed litellm-pricing.json
var liteSnapshot []byte

//go:embed models-dev-catalog-rules.json
var rulesSnapshot []byte

type catalogRules struct {
	Owners    []string `json:"owners"`
	Platforms []string `json:"platforms"`
	Authored  []string `json:"authoredModelIds"`
	Assets    []string `json:"assetPricedModelIds"`
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
func fastMultiplier(name string) float64 {
	var f struct {
		Exact  map[string]float64 `json:"exact"`
		Prefix map[string]float64 `json:"normalized_prefix"`
	}
	_ = json.Unmarshal(fastSnapshot, &f)
	for _, part := range strings.Split(name, "/") {
		if mult, ok := f.Exact[canonical(part)]; ok {
			return mult
		}
		n := normalize(part)
		for base, mult := range f.Prefix {
			if n == base || strings.HasPrefix(n, base+"-") {
				return mult
			}
		}
	}
	return 1
}
func litePricing(data []byte) map[string]ModelPricing {
	var raw map[string]json.RawMessage
	out := map[string]ModelPricing{}
	if json.Unmarshal(data, &raw) != nil {
		return out
	}
	for name, data := range raw {
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil || fields["input_cost_per_token"] == nil || fields["output_cost_per_token"] == nil {
			continue
		}
		var p ModelPricing
		if json.Unmarshal(data, &p) != nil || p.InputCostPerToken < 0 || p.OutputCostPerToken < 0 {
			continue
		}
		if fields["cache_creation_input_token_cost"] == nil {
			p.CacheCreationInputTokenCost = p.InputCostPerToken * 1.25
		}
		if fields["cache_read_input_token_cost"] == nil {
			p.CacheReadInputTokenCost = p.InputCostPerToken * .1
		}
		p.cacheReadExplicit = fields["cache_read_input_token_cost"] != nil
		p.cacheCreateExplicit = fields["cache_creation_input_token_cost"] != nil
		p.FastMultiplier = fastMultiplier(name)
		var provider struct {
			Fast *float64 `json:"fast"`
		}
		if json.Unmarshal(fields["provider_specific_entry"], &provider) == nil && provider.Fast != nil {
			p.FastMultiplier = *provider.Fast
		}
		p.WebSearchCostPerRequest = AnthropicWebSearchCostPerRequest
		out[name] = p
	}
	return out
}
func primaryPricing(fallback map[string]ModelPricing) map[string]ModelPricing {
	primary := litePricing(liteSnapshot)
	// Upstream built-ins are last-resort entries, except the three pinned Codex
	// aliases and GLM cache-provider facts; downloaded rates remain authoritative.
	builtin := map[string][4]float64{
		"claude-opus-4-5": {5, 25, 6.25, .5}, "claude-opus-4-6": {5, 25, 6.25, .5}, "claude-opus-4-7": {5, 25, 6.25, .5}, "claude-opus-4-8": {5, 25, 6.25, .5}, "claude-haiku-4-5": {1, 5, 1.25, .1}, "claude-opus-4": {15, 75, 18.75, 1.5}, "claude-sonnet-4-6": {3, 15, 3.75, .3}, "claude-sonnet-4": {3, 15, 3.75, .3}, "claude-3-5-haiku": {.8, 4, 1, .08}, "claude-3-5-haiku-20241022": {.8, 4, 1, .08}, "claude-3-opus": {15, 75, 18.75, 1.5}, "claude-3-sonnet": {3, 15, 3.75, .3}, "claude-3-haiku": {.25, 1.25, .3, .03},
		"gpt-5": {1.25, 10, 1.25, .125}, "gpt-5.1": {1.25, 10, 1.25, .125}, "gpt-5.1-codex": {1.25, 10, 1.25, .125}, "gpt-5.2": {1.75, 14, 1.75, .175}, "gpt-5.2-codex": {1.75, 14, 1.75, .175}, "gpt-5.3-codex": {1.75, 14, 1.75, .175}, "gpt-5.4": {2.5, 15, 2.5, .25}, "gpt-5.4-mini": {.75, 4.5, .75, .075}, "gpt-5.4-nano": {.2, 1.25, .2, .02}, "gpt-5.5": {5, 30, 5, .5}, "gpt-5.6-sol": {5, 30, 6.25, .5}, "gpt-5.6-terra": {2.5, 15, 3.125, .25}, "gpt-5.6-luna": {1, 6, 1.25, .1}, "grok-4.3": {1.25, 2.5, 1.25, .125}, "moonshot/kimi-k2.5": {.6, 3, .75, .1}, "moonshot/kimi-k2.6": {.95, 4, 1.1875, .16},
		"glm-4.5": {.6, 2.2, 0, .11}, "zai/glm-4.5": {.6, 2.2, 0, .11}, "zai/glm-4.5-x": {2.2, 8.9, 0, .45}, "zai/glm-4.5-air": {.2, 1.1, 0, .03}, "zai/glm-4.5-airx": {1.1, 4.5, 0, .22}, "zai/glm-4.5v": {.6, 1.8, 0, .11}, "zai/glm-4-32b-0414-128k": {.1, .1, 0, 0}, "zai/glm-4.5-flash": {0, 0, 0, 0}, "glm-4.6": {.6, 2.2, 0, .11}, "glm-4.7": {.6, 2.2, 0, .11}, "glm-5": {1, 3.2, 0, .2}, "glm-5-turbo": {1.2, 4, 0, .24}, "glm-5.1": {1.4, 4.4, 0, .26},
	}
	for name, rates := range builtin {
		p, exists := primary[name]
		force := name == "gpt-5.1-codex" || name == "gpt-5.2-codex" || name == "gpt-5.2"
		if !exists || force {
			p = ModelPricing{InputCostPerToken: rates[0] / 1e6, OutputCostPerToken: rates[1] / 1e6, CacheCreationInputTokenCost: rates[2] / 1e6, CacheReadInputTokenCost: rates[3] / 1e6, FastMultiplier: fastMultiplier(name), cacheReadExplicit: true, cacheCreateExplicit: true, WebSearchCostPerRequest: AnthropicWebSearchCostPerRequest}
			if name == "claude-sonnet-4" {
				i, o, c, r := 6e-6, 22.5e-6, 7.5e-6, .6e-6
				p.InputAbove = &i
				p.OutputAbove = &o
				p.CacheCreateAbove = &c
				p.CacheReadAbove = &r
			}
		}
		if strings.HasPrefix(name, "glm-4") || strings.HasPrefix(name, "zai/glm-4") {
			if !p.cacheReadExplicit {
				p.CacheReadInputTokenCost = rates[3] / 1e6
				p.cacheReadExplicit = true
			}
			if !p.cacheCreateExplicit {
				p.CacheCreationInputTokenCost = 0
				p.cacheCreateExplicit = true
			}
		}
		if p.MaxInputTokens == 0 {
			if strings.HasPrefix(name, "claude-") {
				p.MaxInputTokens = 200000
			}
			if strings.HasPrefix(name, "gpt-5.4") || strings.HasPrefix(name, "gpt-5.5") || strings.HasPrefix(name, "gpt-5.6") {
				p.MaxInputTokens = 1050000
			}
			if contains([]string{"claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-4-6"}, name) {
				p.MaxInputTokens = 1000000
			}
			if strings.HasPrefix(name, "moonshot/") {
				p.MaxInputTokens = 262144
			}
			if name == "grok-4.3" {
				p.MaxInputTokens = 1000000
			}
		}
		primary[name] = p
	}
	patchContextTiers(primary, fallback)
	return primary
}
func patchContextTiers(primary, fallback map[string]ModelPricing) {
	keys := make([]string, 0, len(fallback))
	for key := range fallback {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	normalized := map[string]ModelPricing{}
	for _, key := range keys {
		n := normalize(key)
		if _, ok := normalized[n]; !ok {
			normalized[n] = fallback[key]
		}
	}
	for name, p := range primary {
		base := canonical(dateSuffix.ReplaceAllString(name, ""))
		tier, ok := fallback[base]
		if !ok {
			tier, ok = normalized[normalize(base)]
		}
		if ok && tier.LongContextThreshold > 0 {
			p.LongContextThreshold = tier.LongContextThreshold
			p.InputAbove = tier.InputAbove
			p.OutputAbove = tier.OutputAbove
			p.CacheCreateAbove = tier.CacheCreateAbove
			p.CacheReadAbove = tier.CacheReadAbove
		}
		if p.MaxInputTokens == 0 && ok {
			p.MaxInputTokens = tier.MaxInputTokens
		}
		primary[name] = p
	}
}
func (s *Service) refreshModels(ctx context.Context) {
	live := parseModelsCatalog(s.fetch(ctx, "https://models.dev/api.json"))
	s.modelsFailed = len(live) == 0
	s.modelsRetryAt = time.Now().Add(time.Minute)
	spellings := map[string][]string{}
	for old := range s.cache {
		if _, primary := s.primary[old]; !primary {
			key := normalize(old)
			spellings[key] = append(spellings[key], old)
		}
	}
	for name, p := range live {
		if _, primary := s.primary[name]; !primary { // Replace an embedded spelling of the same logical model.
			key := normalize(name)
			for _, old := range spellings[key] {
				delete(s.cache, old)
			}
			spellings[key] = []string{name}
			s.cache[name] = p
		}
	}
	s.memo = map[string]lookupResult{}
}

// parseModelsCatalog reconciles owner, platform and reseller claims using the
// same trust/detail/tie ordering as the upstream snapshot generator.
func parseModelsCatalog(data []byte) map[string]ModelPricing {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	var rules catalogRules
	_ = json.Unmarshal(rulesSnapshot, &rules)
	type claim struct {
		score                int
		provider, source, id string
		value                map[string]any
	}
	claims := map[string]claim{}
	accept := func(provider, source string, value map[string]any, derive bool) {
		model := source
		if id, ok := value["id"].(string); ok && id != "" {
			model = id
		}
		n := normalize(source)
		if contains(rules.Assets, n) {
			return
		}
		if !contains(rules.Authored, n) {
			if modalities, ok := value["modalities"].(map[string]any); ok {
				if out, exists := modalities["output"]; exists {
					a, ok := out.([]any)
					if !ok || len(a) != 1 || a[0] != "text" {
						return
					}
				}
				if in, exists := modalities["input"]; exists {
					a, ok := in.([]any)
					text := false
					for _, v := range a {
						text = text || v == "text"
					}
					if !ok || !text {
						return
					}
				}
			}
		}
		cost, ok := value["cost"].(map[string]any)
		if !ok {
			return
		}
		input, iok := cost["input"].(float64)
		output, ook := cost["output"].(float64)
		if !iok || !ook || input < 0 || output < 0 || (input == 0 && output == 0) {
			return
		}
		trust := 1
		if contains(rules.Platforms, provider) {
			trust = 2
		}
		if contains(rules.Owners, provider) {
			trust = 3
		}
		score := trust << 4
		if tiers, ok := cost["tiers"].([]any); ok {
			for _, v := range tiers {
				if tier, ok := v.(map[string]any); ok {
					if bound, ok := tier["tier"].(map[string]any); ok {
						if size, ok := bound["size"].(float64); ok && size > 0 && bound["type"] == "context" {
							score |= 8
						}
					}
				}
			}
		}
		if _, ok := cost["cache_read"].(float64); ok {
			score |= 4
		}
		if _, ok := cost["cache_write"].(float64); ok {
			score |= 2
		}
		if limit, ok := value["limit"].(map[string]any); ok {
			if _, ok := limit["context"].(float64); ok {
				score |= 1
			}
		}
		if derive {
			exact := !strings.ContainsAny(model, "0123456789")
			if !strings.Contains(source, "/") {
				for _, authored := range rules.Authored {
					if strings.HasPrefix(n, normalize(authored)+"-") {
						exact = true
						break
					}
				}
			}
			value["exactOnly"] = exact
		}
		next := claim{score, provider, source, model, value}
		key := normalize(model)
		prior, exists := claims[key]
		if !exists || score > prior.score || (score == prior.score && (provider < prior.provider || (provider == prior.provider && source < prior.source))) {
			claims[key] = next
		}
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		var value map[string]any
		if json.Unmarshal(raw[key], &value) != nil {
			continue
		}
		if models, ok := value["models"].(map[string]any); ok {
			provider := key
			if id, ok := value["id"].(string); ok && id != "" {
				provider = id
			}
			for source, v := range models {
				if model, ok := v.(map[string]any); ok {
					accept(provider, source, model, true)
				}
			}
		} else {
			accept("", key, value, false)
		}
	}
	flat := map[string]any{}
	for _, c := range claims {
		flat[c.id] = c.value
	}
	encoded, _ := json.Marshal(flat)
	return modelsPricing(encoded)
}
