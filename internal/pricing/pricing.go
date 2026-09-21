// Package pricing resolves vendor model identifiers against an embedded pricing
// snapshot, optionally refreshed from LiteLLM. All rates are dollars per token.
package pricing

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed models-dev-pricing.json
var snapshot []byte

//go:embed fast-multiplier-overrides.json
var fastSnapshot []byte

const AnthropicWebSearchCostPerRequest = 0.01
const AnthropicWebFetchCostPerRequest = 0.0

type ModelPricing struct {
	CacheCreationAsInput        bool     `json:"-"`
	InputCostPerToken           float64  `json:"input_cost_per_token"`
	OutputCostPerToken          float64  `json:"output_cost_per_token"`
	CacheCreationInputTokenCost float64  `json:"cache_creation_input_token_cost"`
	CacheReadInputTokenCost     float64  `json:"cache_read_input_token_cost"`
	InputAbove                  *float64 `json:"input_cost_per_token_above_200k_tokens,omitempty"`
	OutputAbove                 *float64 `json:"output_cost_per_token_above_200k_tokens,omitempty"`
	CacheCreateAbove            *float64 `json:"cache_creation_input_token_cost_above_200k_tokens,omitempty"`
	CacheReadAbove              *float64 `json:"cache_read_input_token_cost_above_200k_tokens,omitempty"`
	LongContextThreshold        int      `json:"long_context_threshold,omitempty"`
	FastMultiplier              float64  `json:"fast_multiplier,omitempty"`
	MaxInputTokens              int      `json:"max_input_tokens,omitempty"`
	WebSearchCostPerRequest     float64  `json:"web_search_cost_per_request,omitempty"`
	WebFetchCostPerRequest      float64  `json:"web_fetch_cost_per_request,omitempty"`
	ExactOnly                   bool     `json:"-"`
	cacheReadExplicit           bool
	cacheCreateExplicit         bool
}

// Override uses pointers so an explicit zero price remains authoritative.
type Override struct {
	InputCostPerToken           *float64 `json:"inputCostPerToken,omitempty"`
	OutputCostPerToken          *float64 `json:"outputCostPerToken,omitempty"`
	CacheCreationInputTokenCost *float64 `json:"cacheCreationInputTokenCost,omitempty"`
	CacheReadInputTokenCost     *float64 `json:"cacheReadInputTokenCost,omitempty"`
	InputAbove                  *float64 `json:"inputCostPerTokenAbove200kTokens,omitempty"`
	OutputAbove                 *float64 `json:"outputCostPerTokenAbove200kTokens,omitempty"`
	CacheCreateAbove            *float64 `json:"cacheCreationInputTokenCostAbove200kTokens,omitempty"`
	CacheReadAbove              *float64 `json:"cacheReadInputTokenCostAbove200kTokens,omitempty"`
	FastMultiplier              *float64 `json:"fastMultiplier,omitempty"`
	MaxInputTokens              *int     `json:"maxInputTokens,omitempty"`
}
type LiteLLMResponse map[string]ModelPricing
type lookupResult struct {
	price ModelPricing
	found bool
}
type Service struct {
	primary         map[string]ModelPricing
	modelsAttempted bool
	modelsFailed    bool
	modelsRetryAt   time.Time
	memo            map[string]lookupResult
	client          *http.Client
	mu              sync.Mutex
	offline         bool
	attempted       bool
	cache           map[string]ModelPricing
	overrides       map[string]Override
}

var embeddedOnce sync.Once
var embeddedPrices, embeddedPrimary map[string]ModelPricing

func NewService() *Service {
	embeddedOnce.Do(func() {
		embeddedPrices = modelsPricing(snapshot)
		embeddedPrimary = primaryPricing(embeddedPrices)
		for model, p := range embeddedPrimary {
			embeddedPrices[model] = p
		}
	})
	cache := make(map[string]ModelPricing, len(embeddedPrices))
	primary := make(map[string]ModelPricing, len(embeddedPrimary))
	for model, p := range embeddedPrices {
		cache[model] = p
	}
	for model, p := range embeddedPrimary {
		primary[model] = p
	}
	return &Service{client: &http.Client{Timeout: 10 * time.Second}, cache: cache, primary: primary, overrides: map[string]Override{}, memo: map[string]lookupResult{}}
}
func (s *Service) SetOffline(v bool) { s.mu.Lock(); defer s.mu.Unlock(); s.offline = v }
func (s *Service) SetOverrides(v map[string]Override) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.overrides = v
}
func (s *Service) HasOverride(model string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.overrides[model]
	return ok
}
func (s *Service) GetModelPrice(ctx context.Context, model string) (float64, float64, float64, float64, float64, float64, error) {
	p, err := s.GetPricing(ctx, model, time.Time{})
	return p.InputCostPerToken, p.OutputCostPerToken, p.CacheCreationInputTokenCost, p.CacheReadInputTokenCost, p.WebSearchCostPerRequest, p.WebFetchCostPerRequest, err
}
func (s *Service) GetPricing(ctx context.Context, model string, at time.Time) (ModelPricing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.offline && !s.attempted {
		s.attempted = true
		s.refresh(ctx)
	}
	resolved := ResolveModelName(model)
	_, exactPrimary := s.primary[model]
	_, aliasPrimary := s.primary[canonical(resolved)]
	if !s.offline && (!s.modelsAttempted || (s.modelsFailed && time.Now().After(s.modelsRetryAt))) && !exactPrimary && !aliasPrimary {
		s.modelsAttempted = true
		s.refreshModels(ctx)
	}
	p, ok := s.primary[model]
	if !ok {
		p, ok = s.primary[canonical(model)]
	}
	if !ok && resolved != model {
		p, ok = s.lookup(resolved)
	}
	if !ok {
		p, ok = s.lookup(model)
	}
	override, hasOverride := s.overrides[model]
	if !hasOverride {
		override, hasOverride = s.overrides[resolved]
	}
	if !ok && !hasOverride {
		return ModelPricing{}, fmt.Errorf("no pricing found for model %q", model)
	}
	if p.FastMultiplier == 0 {
		p.FastMultiplier = 1
	}
	identity := normalize(resolved)
	if !hasOverride && (identity == "deepseek-v4-flash" || identity == "deepseek-v4-pro") {
		override, hasOverride = s.overrides[identity]
	}
	if !at.IsZero() && (identity == "deepseek-v4-flash" || identity == "deepseek-v4-pro") {
		rates := []float64{.14, .28, .14, .0028}
		if identity == "deepseek-v4-pro" {
			rates = []float64{.435, .87, .435, .003625}
		}
		if at.UnixMilli() >= 1786896000000 {
			rates = []float64{.22, .66, .22, .007}
			if identity == "deepseek-v4-pro" {
				rates = []float64{.66, 1.98, .66, .022}
			}
			utc := at.UTC()
			if utc.Weekday() != time.Saturday && utc.Weekday() != time.Sunday && ((utc.Hour() >= 1 && utc.Hour() < 4) || (utc.Hour() >= 6 && utc.Hour() < 10)) {
				for i := range rates {
					rates[i] *= 2
				}
			}
		}
		p.InputCostPerToken = rates[0] / 1e6
		p.OutputCostPerToken = rates[1] / 1e6
		p.CacheCreationInputTokenCost = rates[2] / 1e6
		p.CacheReadInputTokenCost = rates[3] / 1e6
		p.cacheReadExplicit = true
		p.InputAbove = nil
		p.OutputAbove = nil
		p.CacheCreateAbove = nil
		p.CacheReadAbove = nil
	}
	if hasOverride {
		p = apply(p, override)
	}
	return p, nil
}
func apply(p ModelPricing, o Override) ModelPricing {
	if o.InputCostPerToken != nil {
		old := p.InputCostPerToken
		p.InputCostPerToken = *o.InputCostPerToken
		if old > 0 && !p.cacheReadExplicit {
			p.CacheCreationInputTokenCost *= p.InputCostPerToken / old
			p.CacheReadInputTokenCost *= p.InputCostPerToken / old
			if p.CacheCreateAbove != nil {
				value := *p.CacheCreateAbove * p.InputCostPerToken / old
				p.CacheCreateAbove = &value
			}
			if p.CacheReadAbove != nil {
				value := *p.CacheReadAbove * p.InputCostPerToken / old
				p.CacheReadAbove = &value
			}
		}
	}
	if o.OutputCostPerToken != nil {
		p.OutputCostPerToken = *o.OutputCostPerToken
	}
	if o.CacheCreationInputTokenCost != nil {
		p.CacheCreationInputTokenCost = *o.CacheCreationInputTokenCost
	}
	if o.CacheReadInputTokenCost != nil {
		p.CacheReadInputTokenCost = *o.CacheReadInputTokenCost
	}
	if o.InputAbove != nil {
		p.InputAbove = o.InputAbove
	}
	if o.OutputAbove != nil {
		p.OutputAbove = o.OutputAbove
	}
	if o.CacheCreateAbove != nil {
		p.CacheCreateAbove = o.CacheCreateAbove
	}
	if o.CacheReadAbove != nil {
		p.CacheReadAbove = o.CacheReadAbove
	}
	if o.FastMultiplier != nil {
		p.FastMultiplier = *o.FastMultiplier
	}
	if o.MaxInputTokens != nil {
		p.MaxInputTokens = *o.MaxInputTokens
	}
	return p
}
func normalize(s string) string {
	return strings.NewReplacer(".", "-", "@", "-").Replace(strings.ToLower(s))
}

var dateSuffix = regexp.MustCompile(`-(?:[0-9]{8}|[0-9]{4}-[0-9]{2}-[0-9]{2})$`)

func ResolveModelName(model string) string {
	aliases := map[string]string{}
	raw := strings.TrimSpace(os.Getenv("CCUSAGE_MODEL_ALIASES"))
	if json.Unmarshal([]byte(raw), &aliases) != nil {
		for _, pair := range strings.FieldsFunc(strings.Trim(raw, "{}"), func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
			if a, b, ok := strings.Cut(pair, "="); ok {
				aliases[strings.TrimSpace(a)] = strings.TrimSpace(b)
			}
		}
	}
	if alias := aliases[model]; alias != "" {
		return alias
	}
	if base, ok := strings.CutSuffix(model, "-fast"); ok {
		if alias := aliases[base]; alias != "" {
			return alias + "-fast"
		}
	}
	return model
}
func canonical(model string) string {
	switch model {
	case "gpt-reserve":
		return "gpt-5.6-luna"
	case "gpt-5.6":
		return "gpt-5.6-sol"
	case "gpt-5.3-spark":
		return "gpt-5.3-codex-spark"
	}
	return model
}
func find(all map[string]ModelPricing, model string) (ModelPricing, bool) {
	if p, ok := all[model]; ok {
		return p, true
	}
	candidates := []string{canonical(model), strings.TrimSuffix(model, "-fast"), dateSuffix.ReplaceAllString(model, "")}
	for _, prefix := range []string{"anthropic.", "us.anthropic.", "eu.anthropic.", "global.anthropic.", "jp.anthropic.", "au.anthropic."} {
		if strings.HasPrefix(model, prefix) {
			tail := strings.TrimPrefix(model, prefix)
			tail, _, _ = strings.Cut(tail, ":")
			candidates = append(candidates, tail)
		}
	}
	if strings.HasPrefix(model, "[") {
		if _, tail, ok := strings.Cut(model, "] "); ok {
			candidates = append(candidates, tail)
		}
	}
	if _, tail, ok := strings.Cut(model, "/"); ok {
		candidates = append(candidates, tail)
	}
	for _, c := range append([]string{model}, candidates...) {
		c = canonical(c)
		if p, ok := all[c]; ok {
			return p, true
		}
		n := normalize(c)
		keys := make([]string, 0, len(all))
		for key := range all {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if normalize(key) == n {
				return all[key], true
			}
		}
		best := ""
		for _, key := range keys {
			if all[key].ExactOnly {
				continue
			}
			base := normalize(dateSuffix.ReplaceAllString(key, ""))
			if (n == base || strings.HasPrefix(n, base+"-")) && len(base) > len(best) {
				best = base
				model = key
			}
		}
		if best != "" {
			return all[model], true
		}
	}
	return ModelPricing{}, false
}
func (s *Service) lookup(model string) (ModelPricing, bool) {
	if result, ok := s.memo[model]; ok {
		return result.price, result.found
	}
	p, ok := find(s.cache, model)
	s.memo[model] = lookupResult{p, ok}
	return p, ok
}
func (s *Service) refresh(ctx context.Context) {
	live := litePricing(s.fetch(ctx, "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"))
	patchContextTiers(live, modelsPricing(snapshot))
	for model, p := range live {
		s.cache[model] = p
		s.primary[model] = p
	}
	s.memo = map[string]lookupResult{}
}
func modelsPricing(payload []byte) map[string]ModelPricing {
	type cost struct {
		Input      float64  `json:"input"`
		Output     float64  `json:"output"`
		CacheRead  *float64 `json:"cache_read"`
		CacheWrite *float64 `json:"cache_write"`
		Tiers      []struct {
			Input      *float64 `json:"input"`
			Output     *float64 `json:"output"`
			CacheRead  *float64 `json:"cache_read"`
			CacheWrite *float64 `json:"cache_write"`
			Tier       struct {
				Type string `json:"type"`
				Size int    `json:"size"`
			} `json:"tier"`
		} `json:"tiers"`
	}
	var data map[string]struct {
		Cost      cost `json:"cost"`
		ExactOnly bool `json:"exactOnly"`
		Limit     struct {
			Context int `json:"context"`
		} `json:"limit"`
	}
	if err := json.Unmarshal(payload, &data); err != nil {
		panic(err)
	}
	var fast struct {
		Exact  map[string]float64 `json:"exact"`
		Prefix map[string]float64 `json:"normalized_prefix"`
	}
	if err := json.Unmarshal(fastSnapshot, &fast); err != nil {
		panic(err)
	}
	all := make(map[string]ModelPricing, len(data))
	perToken := func(v *float64) *float64 {
		if v == nil {
			return nil
		}
		p := *v / 1e6
		return &p
	}
	for name, m := range data {
		p := ModelPricing{InputCostPerToken: m.Cost.Input / 1e6, OutputCostPerToken: m.Cost.Output / 1e6, CacheCreationInputTokenCost: m.Cost.Input * 1.25 / 1e6, CacheReadInputTokenCost: m.Cost.Input * .1 / 1e6, FastMultiplier: 1, MaxInputTokens: m.Limit.Context, ExactOnly: m.ExactOnly, WebSearchCostPerRequest: AnthropicWebSearchCostPerRequest}
		if m.Cost.CacheRead != nil {
			p.CacheReadInputTokenCost = *m.Cost.CacheRead / 1e6
			p.cacheReadExplicit = true
		}
		if m.Cost.CacheWrite != nil {
			p.CacheCreationInputTokenCost = *m.Cost.CacheWrite / 1e6
			p.cacheCreateExplicit = true
		}
		for _, tier := range m.Cost.Tiers {
			if tier.Tier.Type == "context" && tier.Tier.Size > 0 && (p.LongContextThreshold == 0 || tier.Tier.Size < p.LongContextThreshold) {
				p.LongContextThreshold = tier.Tier.Size
				p.InputAbove = perToken(tier.Input)
				p.OutputAbove = perToken(tier.Output)
				p.CacheReadAbove = perToken(tier.CacheRead)
				p.CacheCreateAbove = perToken(tier.CacheWrite)
			}
		}
		for key, mult := range fast.Exact {
			if canonical(name) == key || strings.HasSuffix(name, "/"+key) {
				p.FastMultiplier = mult
			}
		}
		for key, mult := range fast.Prefix {
			n := normalize(name)
			if n == key || strings.HasPrefix(n, key+"-") || strings.Contains(n, "/"+key) {
				p.FastMultiplier = mult
			}
		}
		all[name] = p
	}
	return all
}
