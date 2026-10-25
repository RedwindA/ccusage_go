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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed models-dev-pricing.json
var snapshot []byte

//go:embed fast-multiplier-overrides.json
var fastSnapshot []byte

//go:embed flex-multiplier-overrides.json
var flexSnapshot []byte

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
	FlexMultiplier              float64  `json:"flex_multiplier,omitempty"`
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
	FlexMultiplier              *float64 `json:"flexMultiplier,omitempty"`
	MaxInputTokens              *int     `json:"maxInputTokens,omitempty"`
}
type LiteLLMResponse map[string]ModelPricing
type lookupResult struct {
	price ModelPricing
	found bool
}

// Service prices models from three tables, like upstream: primary holds the
// LiteLLM snapshot (or its live refresh), the built-in rates and the
// overrides, live holds the live models.dev catalog, and models its embedded
// snapshot; each prices only what the tables before it miss.
type Service struct {
	primary         map[string]ModelPricing
	live            map[string]ModelPricing
	models          map[string]ModelPricing
	modelsAttempted bool
	modelsFailed    bool
	modelsRetryAt   time.Time
	memo            map[string]lookupResult
	idx             *priceIndex
	primaryIdx      *priceIndex
	liveIdx         *priceIndex
	prefetch        *Prefetch
	// prefetchedModelsUsed records that refreshModels consumed prefetch.models.
	prefetchedModelsUsed bool
	client               *http.Client
	mu                   sync.Mutex
	offline              bool
	attempted            bool
	overrides            map[string]Override
	// replaced holds the primary entries setOverrides replaced, by key.
	replaced map[string]lookupResult
}

// snapshotPricing is the embedded models.dev snapshot; callers must not
// modify the returned map.
var snapshotPricing = sync.OnceValue(func() map[string]ModelPricing {
	return cachedEmbedded("snapshot", func() map[string]ModelPricing { return modelsPricing(snapshot) })
})

var embeddedOnce sync.Once
var embeddedPrimary map[string]ModelPricing

func initEmbedded() {
	embeddedPrimary = cachedEmbedded("primary", func() map[string]ModelPricing { return primaryPricing(snapshotPricing()) })
}

// Preload starts decoding the embedded price tables in the background, so a
// command can overlap that fixed cost with loading usage data before it
// calls NewService.
func Preload() { go embeddedOnce.Do(initEmbedded) }

func NewService() *Service {
	embeddedOnce.Do(initEmbedded)
	primary := make(map[string]ModelPricing, len(embeddedPrimary))
	for model, p := range embeddedPrimary {
		primary[model] = p
	}
	return &Service{client: &http.Client{Timeout: 10 * time.Second}, models: snapshotPricing(), primary: primary, overrides: map[string]Override{}, memo: map[string]lookupResult{}}
}
func (s *Service) SetOffline(v bool) { s.mu.Lock(); defer s.mu.Unlock(); s.offline = v }
func (s *Service) SetOverrides(v map[string]Override) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setOverrides(v)
}

// setOverrides makes each override an entry of primary, like upstream's
// apply_override: in key order, it builds on the entry its key, or that key's
// built-in alias, names there (no rates when neither does), and the lookup
// then reaches it like any rate. It first puts back the entries the previous
// overrides replaced. Callers hold s.mu.
func (s *Service) setOverrides(overrides map[string]Override) {
	for key, old := range s.replaced {
		if old.found {
			s.primary[key] = old.price
		} else {
			delete(s.primary, key)
		}
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	s.replaced = make(map[string]lookupResult, len(keys))
	for _, key := range keys {
		p, ok := s.primary[key]
		s.replaced[key] = lookupResult{p, ok}
		if !ok {
			// A context limit stays with the key that publishes it.
			p = s.primary[canonical(key)]
			p.MaxInputTokens = 0
		}
		s.primary[key] = apply(p, overrides[key])
	}
	s.overrides = overrides
	s.memo = map[string]lookupResult{}
	s.primaryIdx = nil
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
	return s.pricing(ctx, model, at, s.lookup)
}

// exactPricing is GetPricing for an id a fuzzy match would bill at another
// model's rate, like upstream's Antigravity Flash tiers: model, and then its
// CCUSAGE_MODEL_ALIASES target, only name an entry of their own (exactEntry).
func (s *Service) exactPricing(ctx context.Context, model string, at time.Time) (ModelPricing, error) {
	return s.pricing(ctx, model, at, func(model, resolved string) (ModelPricing, bool) {
		p, ok := s.exactEntry(model)
		if !ok && resolved != model {
			p, ok = s.exactEntry(resolved)
		}
		return p, ok
	})
}

func (s *Service) pricing(ctx context.Context, model string, at time.Time, lookup func(model, resolved string) (ModelPricing, bool)) (ModelPricing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resolved := ResolveModelName(model)
	s.refreshFor(ctx, model, resolved)
	p, ok := lookup(model, resolved)
	if !ok {
		return ModelPricing{}, fmt.Errorf("no pricing found for model %q", model)
	}
	if p.FastMultiplier == 0 {
		p.FastMultiplier = 1
	}
	if p.FlexMultiplier == 0 {
		p.FlexMultiplier = 1
	}
	identity := deepseekV4Identity(model)
	if identity == "" {
		identity = deepseekV4Identity(resolved)
	}
	if !at.IsZero() && identity != "" {
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
		// Like upstream's find_at, an override of the model, its alias or the
		// DeepSeek id sets its own fields over the schedule.
		for _, key := range []string{model, resolved, identity} {
			if override, ok := s.overrides[key]; ok {
				p = apply(p, override)
				break
			}
		}
	}
	return p, nil
}

// deepseekV4Identity is upstream's deepseek_v4_model_identity: the direct
// DeepSeek V4 id model spells, reading '.' and '@' as '-' but keeping case.
func deepseekV4Identity(model string) string {
	switch n := normalizer.Replace(model); n {
	case "deepseek-v4-flash", "deepseek-v4-pro":
		return n
	}
	return ""
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
		p.cacheReadExplicit = true
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
	if o.FlexMultiplier != nil {
		p.FlexMultiplier = *o.FlexMultiplier
	}
	if o.MaxInputTokens != nil {
		p.MaxInputTokens = *o.MaxInputTokens
	}
	return p
}

var normalizer = strings.NewReplacer(".", "-", "@", "-")

func normalize(s string) string {
	return normalizer.Replace(strings.ToLower(s))
}

// stripDateSuffix removes a trailing -YYYYMMDD or -YYYY-MM-DD, like
// regexp `-(?:[0-9]{8}|[0-9]{4}-[0-9]{2}-[0-9]{2})$` replaced with "" (the
// two shapes cannot both match one suffix). It runs for every price key.
func stripDateSuffix(s string) string {
	digits := func(t string) bool {
		for i := 0; i < len(t); i++ {
			if t[i] < '0' || t[i] > '9' {
				return false
			}
		}
		return true
	}
	if n := len(s); n >= 9 && s[n-9] == '-' && digits(s[n-8:]) {
		return s[:n-9]
	}
	if n := len(s); n >= 11 && s[n-11] == '-' && s[n-6] == '-' && s[n-3] == '-' && digits(s[n-10:n-6]) && digits(s[n-5:n-3]) && digits(s[n-2:]) {
		return s[:n-11]
	}
	return s
}

type parsedAliases struct {
	raw     string
	aliases map[string]string
}

// lastAliases caches the parse of CCUSAGE_MODEL_ALIASES, which
// ResolveModelName otherwise repeats for every priced entry.
var lastAliases atomic.Pointer[parsedAliases]

func modelAliases() map[string]string {
	raw := strings.TrimSpace(os.Getenv("CCUSAGE_MODEL_ALIASES"))
	if cached := lastAliases.Load(); cached != nil && cached.raw == raw {
		return cached.aliases
	}
	aliases := map[string]string{}
	if json.Unmarshal([]byte(raw), &aliases) != nil {
		for _, pair := range strings.FieldsFunc(strings.Trim(raw, "{}"), func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
			if a, b, ok := strings.Cut(pair, "="); ok {
				aliases[strings.TrimSpace(a)] = strings.TrimSpace(b)
			}
		}
	}
	lastAliases.Store(&parsedAliases{raw: raw, aliases: aliases})
	return aliases
}

func ResolveModelName(model string) string {
	aliases := modelAliases()
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

// priceIndex holds lookup's per-key work for one version of a price table:
// keys in sorted order with their separator-normalized spellings, and the key
// for every normalized spelling, among all keys and among exact-only ones
// ("" when two keys share a spelling, which then names neither).
type priceIndex struct {
	keys      []string
	norm      []string
	byNorm    map[string]string
	exactOnly map[string]string
}

func newPriceIndex(all map[string]ModelPricing) *priceIndex {
	idx := &priceIndex{keys: make([]string, 0, len(all)), byNorm: make(map[string]string, len(all)), exactOnly: map[string]string{}}
	for key := range all {
		idx.keys = append(idx.keys, key)
	}
	sort.Strings(idx.keys)
	idx.norm = make([]string, len(idx.keys))
	spell := func(m map[string]string, n, key string) {
		if hasKey(m, n) {
			m[n] = ""
		} else {
			m[n] = key
		}
	}
	for i, key := range idx.keys {
		idx.norm[i] = normalizer.Replace(key)
		spell(idx.byNorm, idx.norm[i], key)
		if all[key].ExactOnly {
			spell(idx.exactOnly, idx.norm[i], key)
		}
	}
	return idx
}

func hasKey[V any](m map[string]V, key string) bool {
	_, ok := m[key]
	return ok
}

// index returns the priceIndex for s.models; callers hold s.mu.
func (s *Service) index() *priceIndex {
	if s.idx == nil {
		s.idx = newPriceIndex(s.models)
	}
	return s.idx
}

// primaryIndex is index for s.primary, reset with s.primaryIdx.
func (s *Service) primaryIndex() *priceIndex {
	if s.primaryIdx == nil {
		s.primaryIdx = newPriceIndex(s.primary)
	}
	return s.primaryIdx
}

// liveIndex is index for s.live, reset with s.liveIdx.
func (s *Service) liveIndex() *priceIndex {
	if s.liveIdx == nil {
		s.liveIdx = newPriceIndex(s.live)
	}
	return s.liveIdx
}

// priceTable is one price table with its priceIndex.
type priceTable struct {
	all map[string]ModelPricing
	idx *priceIndex
}

// fallbacks are the models.dev tables in the order they price what primary
// misses: the live catalog, then the embedded snapshot. Callers hold s.mu.
func (s *Service) fallbacks() []priceTable {
	return []priceTable{{s.live, s.liveIndex()}, {s.models, s.index()}}
}

// requiresExact reports whether model, or the built-in alias it names, is
// any separator spelling of an exact-only models.dev id such as the
// claude-opus-5-fast tier, which a fuzzy match would bill at another model's
// rate.
func (s *Service) requiresExact(model string) bool {
	for _, table := range s.fallbacks() {
		if hasKey(table.idx.exactOnly, normalizer.Replace(model)) || hasKey(table.idx.exactOnly, normalizer.Replace(canonical(model))) {
			return true
		}
	}
	return false
}

// exactEntry looks model up like upstream's find_exact_with_fallback, in
// LiteLLM and built-in rates before models.dev: the id as written, or the one
// key its separator spelling names, case-sensitively. A spelling two keys
// share names neither, so the next table answers. Callers hold s.mu.
func (s *Service) exactEntry(model string) (ModelPricing, bool) {
	n := normalizer.Replace(model)
	for _, table := range append([]priceTable{{s.primary, s.primaryIndex()}}, s.fallbacks()...) {
		if p, ok := table.all[model]; ok {
			return p, true
		}
		if id := table.idx.byNorm[n]; id != "" {
			return table.all[id], true
		}
	}
	return ModelPricing{}, false
}

// find looks model up in one table like upstream's find_entry_or_alias: the
// id, its built-in alias, the exact-only id a separator spelling names and,
// when fuzzy is set, the longest key matching it, among the entries keep
// accepts. The empty id names nothing, though every key contains it:
// upstream's callers never price it.
func find(all map[string]ModelPricing, idx *priceIndex, model string, fuzzy bool, keep func(ModelPricing) bool) (ModelPricing, bool) {
	if model == "" {
		return ModelPricing{}, false
	}
	if p, ok := all[model]; ok && keep(p) {
		return p, true
	}
	names := []string{model}
	if alias := canonical(model); alias != model {
		names = []string{alias, model}
	}
	for _, name := range names {
		if p, ok := all[name]; ok && keep(p) {
			return p, true
		}
		n := normalizer.Replace(name)
		if id := idx.exactOnly[n]; id != "" {
			return all[id], keep(all[id])
		}
		if !fuzzy || hasKey(idx.exactOnly, n) {
			continue
		}
		best := ""
		for i, key := range idx.keys {
			if len(key) > len(best) && !all[key].ExactOnly && keep(all[key]) && pricingKeyMatches(key, idx.norm[i], name, n) {
				best = key
			}
		}
		if best != "" {
			return all[best], true
		}
	}
	return ModelPricing{}, false
}

func anyEntry(ModelPricing) bool { return true }

func hasContextLimit(p ModelPricing) bool { return p.MaxInputTokens > 0 }

// pricingKeyMatches reports whether one of key and model contains the other,
// as written or with '.' and '@' read as '-'.
func pricingKeyMatches(key, normKey, model, normModel string) bool {
	return containsPricingKey(model, key) || containsPricingKey(key, model) || containsPricingKey(normModel, normKey) || containsPricingKey(normKey, normModel)
}

// containsPricingKey finds key in value between non-alphanumeric boundaries,
// and not followed by a further version number: claude-opus-4 is not in
// claude-opus-4-8, but claude-opus-4-1 is in claude-opus-4-1-20250805.
func containsPricingKey(value, key string) bool {
	for at := 0; at <= len(value); {
		i := strings.Index(value[at:], key)
		if i < 0 {
			return false
		}
		i += at
		if (i == 0 || !isAlphanumeric(value[i-1])) && suffixAllowsKey(key, value[i+len(key):]) {
			return true
		}
		at = i + max(len(key), 1)
	}
	return false
}

func suffixAllowsKey(key, suffix string) bool {
	if suffix == "" {
		return true
	}
	if isAlphanumeric(suffix[0]) {
		return false
	}
	if key == "" || !isDigit(key[len(key)-1]) || (suffix[0] != '-' && suffix[0] != '.') {
		return true
	}
	digits := 1
	for digits < len(suffix) && isDigit(suffix[digits]) {
		digits++
	}
	digits--
	// A release date continues the key; another version number does not.
	return digits == 0 || (digits == 8 && (len(suffix) == 9 || !isAlphanumeric(suffix[9])))
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isAlphanumeric(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// lookup resolves model, or the CCUSAGE_MODEL_ALIASES target resolved, in
// upstream's order: LiteLLM and built-in entries exactly, then fuzzily, and
// models.dev only for what they miss, even when it spells the id exactly.
func (s *Service) lookup(model, resolved string) (ModelPricing, bool) {
	key := model + "\x00" + resolved
	if result, ok := s.memo[key]; ok {
		return result.price, result.found
	}
	names := []string{model}
	if resolved != model {
		names = append(names, resolved)
	}
	fuzzy := !s.requiresExact(model) && !s.requiresExact(resolved)
	var p ModelPricing
	ok := false
	for _, allow := range []bool{false, fuzzy} {
		for _, name := range names {
			if !ok {
				p, ok = find(s.primary, s.primaryIndex(), name, allow, anyEntry)
			}
		}
	}
	for _, table := range s.fallbacks() {
		if !ok {
			p, ok = find(table.all, table.idx, resolved, fuzzy, anyEntry)
		}
	}
	s.memo[key] = lookupResult{p, ok}
	return p, ok
}

// builtinContextLimits are upstream's last-resort context windows, which
// count only once LiteLLM and models.dev publish none.
var builtinContextLimits = map[string]ModelPricing{"gpt-5.6-sol": {MaxInputTokens: 1050000}, "gpt-5.6-terra": {MaxInputTokens: 1050000}, "gpt-5.6-luna": {MaxInputTokens: 1050000}}

var builtinContextIndex = sync.OnceValue(func() *priceIndex { return newPriceIndex(builtinContextLimits) })

// ContextLimit is upstream's PricingMap::context_limit: the context window of
// the entry lookup would price model from, counting only entries that publish
// one (an override's maxInputTokens among them), and then the built-in
// last-resort limits.
func (s *Service) ContextLimit(ctx context.Context, model string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resolved := ResolveModelName(model)
	s.refreshFor(ctx, model, resolved)
	names := []string{model}
	if resolved != model {
		names = append(names, resolved)
	}
	fuzzy := !s.requiresExact(model) && !s.requiresExact(resolved)
	for _, allow := range []bool{false, fuzzy} {
		for _, name := range names {
			if p, ok := find(s.primary, s.primaryIndex(), name, allow, hasContextLimit); ok {
				return p.MaxInputTokens, true
			}
		}
	}
	for _, table := range s.fallbacks() {
		if p, ok := find(table.all, table.idx, resolved, fuzzy, hasContextLimit); ok {
			return p.MaxInputTokens, true
		}
	}
	for _, name := range names {
		if p, ok := find(builtinContextLimits, builtinContextIndex(), name, fuzzy, hasContextLimit); ok {
			return p.MaxInputTokens, true
		}
	}
	return 0, false
}

const liteURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

func parseLite(data []byte) map[string]ModelPricing {
	live := litePricing(data)
	patchContextTiers(live, snapshotPricing())
	return live
}

func fetchLite(ctx context.Context, client *http.Client) map[string]ModelPricing {
	var live map[string]ModelPricing
	data := fetchUsable(ctx, client, liteURL, func(data []byte) bool {
		live = cachedParse(liteURL, data, parseLite)
		return len(live) > 0
	})
	if len(live) == 0 {
		live = cachedParse(liteURL, data, parseLite)
	}
	return live
}

// refreshFor fetches LiteLLM once, and models.dev unless LiteLLM or a
// built-in rate names model or its alias exactly, again a minute after a
// failed fetch; callers hold s.mu.
func (s *Service) refreshFor(ctx context.Context, model, resolved string) {
	s.refresh(ctx)
	_, exactPrimary := s.primary[model]
	_, aliasPrimary := s.primary[canonical(resolved)]
	if !s.offline && (!s.modelsAttempted || (s.modelsFailed && time.Now().After(s.modelsRetryAt))) && !exactPrimary && !aliasPrimary {
		s.modelsAttempted = true
		s.refreshModels(ctx)
	}
}

// refresh fetches LiteLLM on its first call unless offline.
func (s *Service) refresh(ctx context.Context) {
	if s.offline || s.attempted {
		return
	}
	s.attempted = true
	live, prefetched := s.prefetch.take(false)
	if !prefetched {
		live = fetchLite(ctx, s.client)
	}
	// Like upstream, the overrides apply to the refreshed entries.
	overrides := s.overrides
	s.setOverrides(nil)
	for model, p := range live {
		s.primary[model] = p
	}
	s.setOverrides(overrides)
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
	all := make(map[string]ModelPricing, len(data))
	perToken := func(v *float64) *float64 {
		if v == nil {
			return nil
		}
		p := *v / 1e6
		return &p
	}
	for name, m := range data {
		p := ModelPricing{InputCostPerToken: m.Cost.Input / 1e6, OutputCostPerToken: m.Cost.Output / 1e6, CacheCreationInputTokenCost: m.Cost.Input * 1.25 / 1e6, CacheReadInputTokenCost: m.Cost.Input * .1 / 1e6, FastMultiplier: fastMultiplier(name), FlexMultiplier: flexMultiplier(name), MaxInputTokens: m.Limit.Context, ExactOnly: m.ExactOnly, WebSearchCostPerRequest: AnthropicWebSearchCostPerRequest}
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
		all[name] = p
	}
	return all
}
