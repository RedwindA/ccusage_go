package calculator

import (
	"context"
	"github.com/RedwindA/ccusage_go/internal/pricing"
	"github.com/RedwindA/ccusage_go/internal/types"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func modernCalculator() *Calculator { s := pricing.NewService(); s.SetOffline(true); return New(s) }
func TestModesPreserveExplicitZero(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		recorded bool
		wantZero bool
	}{{"auto", true, true}, {"auto", false, false}, {"calculate", true, false}, {"display", false, true}} {
		c := modernCalculator()
		c.SetMode(tc.mode)
		entries, _ := c.CalculateCosts(context.Background(), []types.UsageEntry{{Model: "gpt-5.6-sol", InputTokens: 100, HasCost: tc.recorded}})
		if (entries[0].Cost == 0) != tc.wantZero {
			t.Errorf("%+v got %v", tc, entries[0].Cost)
		}
	}
}
func TestLongContextAndFast(t *testing.T) {
	c := modernCalculator()
	e := types.UsageEntry{Model: "grok-4.5", InputTokens: 10000, OutputTokens: 1000, CacheReadInputTokens: 500000}
	_ = c.CalculateCost(&e)
	if math.Abs(e.Cost-.352) > 1e-9 {
		t.Fatalf("whole context tier: %v", e.Cost)
	}
	a := types.UsageEntry{Model: "gpt-5.6-sol", InputTokens: 100}
	b := a
	b.Speed = "priority"
	_ = c.CalculateCost(&a)
	_ = c.CalculateCost(&b)
	if b.Cost != a.Cost*2 {
		t.Fatalf("fast: %v / %v", a.Cost, b.Cost)
	}
}

// Upstream applies the Fast multiplier only to usage that records Fast
// speed; a model id ending in -fast, like Copilot's claude-opus-4.6-fast or
// a models.dev -fast entry, is priced as written. Expected costs are
// upstream ccusage's.
func TestFastSuffixedModelsArePricedAsWritten(t *testing.T) {
	c := modernCalculator()
	for _, tc := range []struct {
		agent, model, speed string
		cost                float64
	}{
		{"claude", "claude-opus-4-8", "", 1.31375},
		{"claude", "claude-opus-4-8", "fast", 2.6275},
		{"claude", "claude-opus-4-8-fast", "", 2.6275},
		{"claude", "claude-opus-4-6-fast", "", 1.31375},
		{"claude", "anthropic/claude-opus-5-fast", "", 1.31375},
		{"claude", "gpt-5.5-fast", "", 2.60375},
		{"claude", "openai/gpt-5.5-fast", "", 2.60375},
	} {
		e := types.UsageEntry{Agent: tc.agent, Model: tc.model, Speed: tc.speed, InputTokens: 250000, OutputTokens: 1000, CacheCreationInputTokens: 3000, CacheReadInputTokens: 40000}
		_ = c.CalculateCost(&e)
		if math.Abs(e.Cost-tc.cost) > 1e-9 {
			t.Errorf("%s %s speed %q: %v, want %v", tc.agent, tc.model, tc.speed, e.Cost, tc.cost)
		}
	}
	e := types.UsageEntry{Agent: "copilot", Model: "claude-opus-4.6-fast", InputTokens: 1000000}
	_ = c.CalculateCost(&e)
	if math.Abs(e.Cost-5) > 1e-9 {
		t.Errorf("copilot claude-opus-4.6-fast: %v, want 5", e.Cost)
	}
}
func TestCacheTTLAndRawModelOverride(t *testing.T) {
	s := pricing.NewService()
	s.SetOffline(true)
	input, output, cache := 1.0, 2.0, 1.25
	s.SetOverrides(map[string]pricing.Override{"[pi] custom": {InputCostPerToken: &input, OutputCostPerToken: &output, CacheCreationInputTokenCost: &cache}})
	c := New(s)
	e := types.UsageEntry{Agent: "pi", Model: "custom", CacheCreationInputTokens: 999, Raw: map[string]interface{}{"cache_creation": map[string]interface{}{"ephemeral_5m_input_tokens": 10.0, "ephemeral_1h_input_tokens": 20.0}}}
	_ = c.CalculateCost(&e)
	if e.Cost != 52.5 {
		t.Fatal(e.Cost)
	}
}
func TestPiAutoOverrideResolvesConfiguredModelAliases(t *testing.T) {
	t.Setenv("CCUSAGE_MODEL_ALIASES", `{"private-model":"custom-model"}`)
	s := pricing.NewService()
	s.SetOffline(true)
	input := 0.001
	s.SetOverrides(map[string]pricing.Override{"custom-model": {InputCostPerToken: &input}})
	c := New(s)
	c.SetMode("auto")
	for model, want := range map[string]float64{"custom-model": 1, "private-model": 1, "unrelated-model": 1.5} {
		e := types.UsageEntry{Agent: "pi", Model: model, InputTokens: 1000, Cost: 1.5, HasCost: true, Raw: map[string]interface{}{"source_format": "pi"}}
		_ = c.CalculateCost(&e)
		if math.Abs(e.Cost-want) > 1e-12 {
			t.Errorf("%s: %v, want %v", model, e.Cost, want)
		}
	}
}

// An override reached through an alias of the store-prefixed label prices
// the record, as upstream's find on the "[pi] model" label does, instead of
// only making auto mode drop the recorded cost.
func TestPiOverrideThroughAnAliasOfTheStoreLabel(t *testing.T) {
	s := pricing.NewService()
	s.SetOffline(true)
	input, output := 0.001, 0.002
	s.SetOverrides(map[string]pricing.Override{"custom": {InputCostPerToken: &input, OutputCostPerToken: &output}})
	c := New(s)
	for _, agent := range []string{"pi", "omp"} {
		t.Setenv("CCUSAGE_MODEL_ALIASES", `{"[`+agent+`] private":"custom"}`)
		for _, mode := range []string{"auto", "calculate"} {
			c.SetMode(mode)
			e := types.UsageEntry{Agent: agent, Model: "private", InputTokens: 1000, OutputTokens: 2000, Cost: 1.5, HasCost: true, Raw: map[string]interface{}{"source_format": "pi"}}
			_ = c.CalculateCost(&e)
			if math.Abs(e.Cost-5) > 1e-12 || e.Raw["missing_pricing"] != nil {
				t.Errorf("%s %s: %v %v", agent, mode, e.Cost, e.Raw)
			}
		}
	}
}

// Like upstream's calculate_hermes_cost, a provider-qualified rate that bills
// nothing, such as LiteLLM's free zai/glm-4.7-flash, gives way to the raw
// model, and leaves no missing-pricing flag when the raw model has no rate.
func TestHermesSkipsAProviderRateThatBillsNothing(t *testing.T) {
	s := pricing.NewService()
	s.SetOffline(true)
	zero := 0.0
	s.SetOverrides(map[string]pricing.Override{"acme/private": {InputCostPerToken: &zero, OutputCostPerToken: &zero}})
	c := New(s)
	hermes := func(model string, provider any) types.UsageEntry {
		e := types.UsageEntry{Agent: "hermes", Model: model, InputTokens: 1000, OutputTokens: 500, CacheReadInputTokens: 2000, Raw: map[string]interface{}{"provider": provider}}
		_ = c.CalculateCost(&e)
		return e
	}
	want := hermes("glm-4.7-flash", "hermes").Cost
	for _, provider := range []string{"zai", "ZAI"} {
		if e := hermes("glm-4.7-flash", provider); want <= 0 || e.Cost != want {
			t.Errorf("%s: %v, want %v", provider, e.Cost, want)
		}
	}
	if e := hermes("private", "acme"); e.Cost != 0 || e.Raw["missing_pricing"] != nil {
		t.Errorf("free acme/private: %v %v", e.Cost, e.Raw)
	}
}

// Only auto mode reports the recorded cost it billed as stored, following
// each source's own rule for when a recorded cost wins.
func TestAutoModeMarksTheRecordedCostItBills(t *testing.T) {
	s := pricing.NewService()
	s.SetOffline(true)
	input := 0.001
	s.SetOverrides(map[string]pricing.Override{"custom": {InputCostPerToken: &input}})
	c := New(s)
	for _, tc := range []struct {
		name  string
		entry types.UsageEntry
		auto  bool
	}{
		{"claude costUSD", types.UsageEntry{Agent: "claude", Model: "claude-sonnet-4-20250514", InputTokens: 500, Cost: 0.5, HasCost: true}, true},
		{"claude costUSD 0", types.UsageEntry{Agent: "claude", Model: "claude-sonnet-4-20250514", InputTokens: 500, HasCost: true}, true},
		{"claude without costUSD", types.UsageEntry{Agent: "claude", Model: "claude-sonnet-4-20250514", InputTokens: 500}, false},
		{"grok costUsdTicks", types.UsageEntry{Agent: "grok", Model: "grok-4.5-build", InputTokens: 18444, OutputTokens: 130, Cost: 0.0185192, HasCost: true}, true},
		{"kilo cost 0", types.UsageEntry{Agent: "kilo", Model: "gpt-5", InputTokens: 100, HasCost: true}, true},
		{"opencode cost", types.UsageEntry{Agent: "opencode", Model: "gpt-5", InputTokens: 100, Cost: 0.5, HasCost: true}, true},
		{"opencode cost 0", types.UsageEntry{Agent: "opencode", Model: "gpt-5", InputTokens: 100, HasCost: true}, false},
		{"hermes cost 0", types.UsageEntry{Agent: "hermes", Model: "gpt-5", InputTokens: 100, HasCost: true}, false},
		{"pi cost", types.UsageEntry{Agent: "pi", Model: "gpt-5", InputTokens: 100, Cost: 0.5, HasCost: true, Raw: map[string]interface{}{"source_format": "pi"}}, true},
		{"pi override", types.UsageEntry{Agent: "pi", Model: "custom", InputTokens: 100, Cost: 0.5, HasCost: true, Raw: map[string]interface{}{"source_format": "pi"}}, false},
	} {
		for _, mode := range []string{"auto", "calculate", "display"} {
			c.SetMode(mode)
			e := tc.entry
			_ = c.CalculateCost(&e)
			if want := tc.auto && mode == "auto"; e.UsesStoredCost != want {
				t.Errorf("%s %s: UsesStoredCost = %v, want %v", tc.name, mode, e.UsesStoredCost, want)
			}
		}
	}
}

// Port of upstream's auto_recalculates_negative_but_keeps_zero_source_cost_for_default_and_named_stores:
// auto mode prices the tokens of a negative recorded Pi cost and keeps a 0,
// while display mode still shows the raw figure.
func TestPiAutoModePricesANegativeRecordedCost(t *testing.T) {
	c := modernCalculator()
	for _, agent := range []string{"pi", "omp"} {
		for _, tc := range []struct {
			mode, name     string
			recorded, want float64
			stored         bool
		}{{"auto", "negative", -1.25, 0.033, false}, {"auto", "zero", 0, 0, true}, {"display", "negative", -1.25, -1.25, false}} {
			c.SetMode(tc.mode)
			e := types.UsageEntry{Agent: agent, Model: "claude-sonnet-4-20250514", InputTokens: 1000, OutputTokens: 2000, Cost: tc.recorded, HasCost: true, Raw: map[string]interface{}{"source_format": "pi"}}
			_ = c.CalculateCost(&e)
			if math.Abs(e.Cost-tc.want) > 1e-12 || e.UsesStoredCost != tc.stored {
				t.Errorf("%s %s %s: cost %v stored %v, want %v %v", agent, tc.mode, tc.name, e.Cost, e.UsesStoredCost, tc.want, tc.stored)
			}
		}
	}
}
func TestUnknownAndReasoning(t *testing.T) {
	c := modernCalculator()
	e := types.UsageEntry{Model: "not-a-real-model", InputTokens: 100}
	_ = c.CalculateCost(&e)
	if e.Cost != 0 || e.Raw["missing_pricing"] != true {
		t.Fatalf("unknown: %+v", e)
	}
	a := types.UsageEntry{Model: "gpt-5.6-sol", OutputTokens: 100}
	b := a
	b.ReasoningTokens = 100
	b.Raw = map[string]interface{}{"reasoning_is_extra": true}
	_ = c.CalculateCost(&a)
	_ = c.CalculateCost(&b)
	if math.Abs(b.Cost-2*a.Cost) > 1e-12 {
		t.Fatalf("reasoning: %v %v", a.Cost, b.Cost)
	}
}
func TestSourceSpecificCostPolicies(t *testing.T) {
	c := modernCalculator()
	for _, agent := range []string{"hermes", "opencode"} {
		e := types.UsageEntry{Agent: agent, Model: "gpt-5.5", InputTokens: 100, HasCost: true}
		_ = c.CalculateCost(&e)
		if e.Cost <= 0 {
			t.Fatalf("%s zero stored cost blocked calculation", agent)
		}
	}
	e := types.UsageEntry{Agent: "kilo", Model: "gpt-5.5", InputTokens: 100, HasCost: true}
	_ = c.CalculateCost(&e)
	if e.Cost != 0 {
		t.Fatal("kilo explicit zero not preserved")
	}
	e = types.UsageEntry{Agent: "zcode", Model: "gpt-5.5", InputTokens: 100, Raw: map[string]interface{}{"provider": "openai"}}
	_ = c.CalculateCost(&e)
	if e.Cost != 0 || e.Raw["missing_pricing"] != true {
		t.Fatalf("unsupported ZCode provider priced: %+v", e)
	}
	a := types.UsageEntry{Agent: "claude", Model: "claude-opus-4-6", InputTokens: 100}
	b := a
	c.SetSpeed("fast")
	_ = c.CalculateCost(&a)
	c.SetSpeed("standard")
	_ = c.CalculateCost(&b)
	if a.Cost != b.Cost {
		t.Fatal("Codex speed changed Claude pricing")
	}
}

// Upstream's Droid, Codebuff and Goose adapters price usage whatever the
// cost mode and flag the models they cannot price.
func TestAlwaysPricedSourcesInDisplayMode(t *testing.T) {
	c := modernCalculator()
	c.SetMode("display")
	for _, agent := range []string{"droid", "codebuff", "goose"} {
		e := types.UsageEntry{Agent: agent, Model: "gpt-5.5", InputTokens: 100}
		_ = c.CalculateCost(&e)
		if e.Cost <= 0 {
			t.Errorf("%s display cost %v", agent, e.Cost)
		}
		e = types.UsageEntry{Agent: agent, Model: "unknown-model-x", InputTokens: 100}
		_ = c.CalculateCost(&e)
		if e.Raw["missing_pricing"] != true {
			t.Errorf("%s display mode left an unknown model unflagged: %+v", agent, e)
		}
	}
}
func TestExtraOutputTokensPrecedeReasoningFallback(t *testing.T) {
	c := modernCalculator()
	a := types.UsageEntry{Model: "gpt-5.5", OutputTokens: 100}
	b := a
	b.ReasoningTokens = 999
	b.Raw = map[string]interface{}{"extra_output_tokens": 100, "reasoning_is_extra": true}
	_ = c.CalculateCost(&a)
	_ = c.CalculateCost(&b)
	if math.Abs(b.Cost-a.Cost*2) > 1e-12 {
		t.Fatalf("%v vs %v", a.Cost, b.Cost)
	}
}
func TestCodexSpeedDetectionUsesOnlyExplicitRoots(t *testing.T) {
	fast, standard := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(fast, "config.toml"), []byte("service_tier = 'priority' # comment\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", fast)
	if got := DetectCodexSpeed([]string{standard}); got != "standard" {
		t.Fatal("global Codex config leaked", got)
	}
	if got := DetectCodexSpeed([]string{fast}); got != "fast" {
		t.Fatal(got)
	}
}

// Expected costs are upstream ccusage's for the same usage.
func TestEmbeddedSnapshotPricesNewModels(t *testing.T) {
	c := modernCalculator()
	small := types.UsageEntry{InputTokens: 1000, OutputTokens: 500, CacheReadInputTokens: 2000, CacheCreationInputTokens: 300}
	large := types.UsageEntry{InputTokens: 250000, OutputTokens: 10000, CacheReadInputTokens: 50000, CacheCreationInputTokens: 20000}
	fast := small
	fast.Speed = "fast"
	for _, tc := range []struct {
		model string
		usage types.UsageEntry
		cost  float64
	}{
		{"claude-opus-5-5", small, .0159}, {"claude-opus-5-5", fast, .0318}, {"claude-opus-5-5", types.UsageEntry{InputTokens: 500000, CacheReadInputTokens: 500000, OutputTokens: 100000}, 4.1},
		{"claude-sonnet-5-5", small, .00795}, {"claude-sonnet-5-5", large, .655},
		{"claude-haiku-5-5", small, .0004075}, {"claude-haiku-5-5", large, .165},
		{"gpt-6-sol", small, .00815}, {"gpt-6-sol", large, 1.27}, {"gpt-6-sol", fast, .0163},
		{"gpt-6.1-sol", small, .00795}, {"gpt-6.1-sol", large, 1.26}, {"gpt-6.1-sol", fast, .0159},
		{"gpt-6-luna", small, .0004075}, {"gpt-6-luna", large, .0635}, {"gpt-6-luna", fast, .000815},
		{"gemini-4-argon", small, .00795}, {"gemini-4-argon", large, .655},
	} {
		e := tc.usage
		e.Model = tc.model
		_ = c.CalculateCost(&e)
		if math.Abs(e.Cost-tc.cost) > 1e-9 {
			t.Errorf("%s %+v: %v, want %v", tc.model, tc.usage, e.Cost, tc.cost)
		}
	}
}

// LiteLLM no longer lists these ids: upstream bills the Claude ones at
// LiteLLM's Bedrock entries (Sonnet 4 with their 200K split) rather than a
// models.dev spelling, and the dated GPT-6 Sol at Azure's flat dated entry.
func TestIDsLiteLLMLacksPriceLikeUpstream(t *testing.T) {
	c := modernCalculator()
	for model, cost := range map[string]float64{"claude-sonnet-4-20250514": 1.14, "claude-opus-4-1-20250805": 4.95, "gpt-6-sol-2026-09-22": .66} {
		e := types.UsageEntry{Model: model, InputTokens: 250000, OutputTokens: 10000, CacheReadInputTokens: 50000, CacheCreationInputTokens: 20000}
		_ = c.CalculateCost(&e)
		if math.Abs(e.Cost-cost) > 1e-9 {
			t.Errorf("%s: %v, want %v", model, e.Cost, cost)
		}
	}
}
func TestCodexFlexPricing(t *testing.T) {
	c := modernCalculator()
	price := func(speed, agent string) float64 {
		e := types.UsageEntry{Agent: agent, Model: "gpt-5.4", InputTokens: 1000, OutputTokens: 100, Speed: speed}
		_ = c.CalculateCost(&e)
		return e.Cost
	}
	standard := price("standard", "codex")
	if got := price("flex", "codex"); math.Abs(got-standard/2) > 1e-12 {
		t.Fatalf("recorded flex: %v, standard %v", got, standard)
	}
	c.SetSpeed("flex")
	for _, speed := range []string{"", "standard", "fast"} {
		if got := price(speed, "codex"); math.Abs(got-standard/2) > 1e-12 {
			t.Errorf("forced flex over %q: %v, standard %v", speed, got, standard)
		}
	}
	if got := price("", "claude"); got != standard {
		t.Fatalf("Codex speed changed Claude pricing: %v", got)
	}
}

// Codex bills cached input at the input rate (above a long-context tier, its
// rate there) when the price names no cache-read rate, as gpt-5-pro's and
// gpt-5.5-pro's do not; an override's cache-read rate names one. Other agents
// keep the 10% default. Expected costs are upstream ccusage's.
func TestCodexBillsCachedInputAtInputRateWithoutCacheReadRate(t *testing.T) {
	s := pricing.NewService()
	s.SetOffline(true)
	cacheRead := 1e-6
	s.SetOverrides(map[string]pricing.Override{"o3-pro": {CacheReadInputTokenCost: &cacheRead}})
	c := New(s)
	for _, tc := range []struct {
		agent, model  string
		input, cached int
		cost          float64
	}{
		{"codex", "gpt-5-pro", 200000, 800000, 15.12},
		{"codex", "gpt-5.5-pro", 100000, 200000, 18.27},
		{"codex", "o3-pro", 200000, 800000, 4.88},
		{"codex", "gpt-5", 200000, 800000, .36},
		{"claude", "gpt-5-pro", 200000, 800000, 4.32},
	} {
		e := types.UsageEntry{Agent: tc.agent, Model: tc.model, InputTokens: tc.input, CacheReadInputTokens: tc.cached, OutputTokens: 1000}
		_ = c.CalculateCost(&e)
		if math.Abs(e.Cost-tc.cost) > 1e-9 {
			t.Errorf("%s %s: %v, want %v", tc.agent, tc.model, e.Cost, tc.cost)
		}
	}
}
func TestCodexSpeedDetectionFollowsSelectedProfile(t *testing.T) {
	for _, tc := range []struct{ config, want string }{
		{`service_tier = "fast"`, "fast"},
		{`service_tier = 'priority' # use higher tier`, "fast"},
		{`service_tier = "flex"`, "flex"},
		{`service_tier_override = "fast"`, "standard"},
		{`service_tier = "breakfast"`, "standard"},
		// Inactive profiles do not affect pricing.
		{`service_tier = "standard"
[profiles.flex]
service_tier = "flex"`, "standard"},
		{`[profiles.fast]
service_tier = "priority"`, "standard"},
		{`profile = "work"
[profiles.work]
model = "gpt-5.4"
[profiles.other]
service_tier = "priority"`, "standard"},
		// The selected profile overrides the top-level tier.
		{`service_tier = "standard"
profile = "flex.#"
[profiles."flex.#"]
service_tier = "flex"
[profiles.fast]
service_tier = "priority"`, "flex"},
		{`service_tier = "priority"
profile = "standard"
[profiles.standard]
service_tier = "default"`, "standard"},
		// A selected profile without a tier inherits the top-level tier.
		{`service_tier = "flex"
profile = "work"
[profiles.work]
model = "gpt-6.1-sol"
[profiles.fast]
service_tier = "priority"`, "flex"},
		{`service_tier = "standard"
instructions = '''
service_tier = "priority"
'''`, "standard"},
		{`service_tier = "fast`, "standard"},
	} {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(tc.config), 0600); err != nil {
			t.Fatal(err)
		}
		if got := DetectCodexSpeed([]string{home}); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.config, got, tc.want)
		}
	}
}
func TestCodexSpeedDetectionPrefersFastThenFlexAcrossRoots(t *testing.T) {
	roots := map[string]string{}
	for _, tier := range []string{"standard", "flex", "priority"} {
		roots[tier] = t.TempDir()
		if err := os.WriteFile(filepath.Join(roots[tier], "config.toml"), []byte(`service_tier = "`+tier+`"`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		roots []string
		want  string
	}{
		{[]string{roots["standard"], roots["flex"]}, "flex"},
		{[]string{roots["flex"], roots["standard"]}, "flex"},
		{[]string{roots["flex"], roots["priority"]}, "fast"},
		{[]string{roots["priority"], roots["flex"]}, "fast"},
	} {
		if got := DetectCodexSpeed(tc.roots); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.roots, got, tc.want)
		}
	}
}
