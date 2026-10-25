package pricing

import (
	"context"
	"math"
	"net/http"
	"testing"
	"time"
)

type forbiddenNetwork struct{ t *testing.T }

func (f forbiddenNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Fatal("offline pricing attempted network access")
	return nil, nil
}
func TestOfflineSnapshotAndAliases(t *testing.T) {
	s := NewService()
	s.SetOffline(true)
	s.client = &http.Client{Transport: forbiddenNetwork{t}}
	for _, model := range []string{"gpt-5.6-sol", "gpt-6-astra", "claude-opus-4-8", "grok-4.5", "gemini-2.5-pro", "gpt-reserve", "gpt-5.6", "anthropic/claude-sonnet-4.6", "claude-sonnet-4-6-20260901"} {
		p, err := s.GetPricing(context.Background(), model, time.Time{})
		if err != nil || p.InputCostPerToken <= 0 {
			t.Errorf("%s: %+v %v", model, p, err)
		}
	}
	if _, err := s.GetPricing(context.Background(), "entirely-unknown-model", time.Time{}); err == nil {
		t.Error("unknown models must not invent prices")
	}
	t.Setenv("CCUSAGE_MODEL_ALIASES", `{"private-model":"gpt-5.6-sol"}`)
	p, err := s.GetPricing(context.Background(), "private-model", time.Time{})
	if err != nil || p.FastMultiplier != 2 {
		t.Fatalf("alias: %+v %v", p, err)
	}
}
func TestZeroAndPartialOverrides(t *testing.T) {
	s := NewService()
	s.SetOffline(true)
	zero, input := 0.0, 9e-6
	s.SetOverrides(map[string]Override{"gpt-5.6": {InputCostPerToken: &input, OutputCostPerToken: &zero}, "free": {InputCostPerToken: &zero, OutputCostPerToken: &zero}})
	p, err := s.GetPricing(context.Background(), "gpt-5.6", time.Time{})
	if err != nil || p.InputCostPerToken != input || p.OutputCostPerToken != 0 || p.FastMultiplier != 2 {
		t.Fatalf("override: %+v %v", p, err)
	}
	if p, err = s.GetPricing(context.Background(), "free", time.Time{}); err != nil || p.InputCostPerToken != 0 {
		t.Fatalf("free: %+v %v", p, err)
	}
}
func TestDeepSeekTimeSchedule(t *testing.T) {
	s := NewService()
	s.SetOffline(true)
	for _, tc := range []struct {
		at   string
		rate float64
	}{{"2026-08-16T15:59:59Z", .14e-6}, {"2026-08-17T12:00:00Z", .22e-6}, {"2026-08-17T01:00:00Z", .44e-6}, {"2026-08-22T01:00:00Z", .22e-6}} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		p, err := s.GetPricing(context.Background(), "deepseek-v4-flash", at)
		if err != nil || math.Abs(p.InputCostPerToken-tc.rate) > 1e-15 {
			t.Errorf("%s: %+v %v", tc.at, p, err)
		}
	}
}
func TestSourceAliasesAndZcodeOverrides(t *testing.T) {
	s := NewService()
	s.SetOffline(true)
	before := time.UnixMilli(1776698890071)
	after := time.UnixMilli(1776698890072)
	p, err := s.GetSourcePricing(context.Background(), "kimi-for-coding", "kimi", nil, before)
	old, _ := s.GetPricing(context.Background(), "moonshot/kimi-k2.5", before)
	if err != nil || p.InputCostPerToken != old.InputCostPerToken {
		t.Fatalf("Kimi old %+v %v", p, err)
	}
	p, err = s.GetSourcePricing(context.Background(), "kimi-for-coding", "kimi", nil, after)
	next, _ := s.GetPricing(context.Background(), "moonshot/kimi-k2.6", after)
	if err != nil || p.InputCostPerToken != next.InputCostPerToken {
		t.Fatalf("Kimi new %+v %v", p, err)
	}
	rate := 1.0
	cache := 3.0
	s.SetOverrides(map[string]Override{"custom": {InputCostPerToken: &rate, CacheCreationInputTokenCost: &cache}})
	p, err = s.GetSourcePricing(context.Background(), "custom", "zcode", "openai", after)
	if err != nil || p.CacheCreationInputTokenCost != 3 || p.CacheCreationAsInput {
		t.Fatalf("custom provider override %+v %v", p, err)
	}
	p, err = s.GetSourcePricing(context.Background(), "custom", "zcode", "zai", after)
	if err != nil || p.CacheCreationInputTokenCost != 1 || !p.CacheCreationAsInput {
		t.Fatalf("zai override %+v %v", p, err)
	}
}

// tableService prices from primary alone, without the embedded snapshots.
func tableService(primary map[string]ModelPricing) *Service {
	return &Service{primary: primary, models: map[string]ModelPricing{}, offline: true, overrides: map[string]Override{}, memo: map[string]lookupResult{}}
}

// Ported from upstream's fuzzy_match_* pricing tests.
func TestFuzzyLookupStopsAtModelVersionBoundaries(t *testing.T) {
	ctx := context.Background()
	s := tableService(map[string]ModelPricing{"claude-opus-4-7": {InputCostPerToken: 5e-6}, "claude-opus-4": {InputCostPerToken: 15e-6}})
	for _, model := range []string{"claude-opus-4.70", "claude-opus-4.8-20260528", "claude-opus-4-9", "claude-opus-5"} {
		if p, err := s.GetPricing(ctx, model, time.Time{}); err == nil {
			t.Errorf("%s matched %+v", model, p)
		}
	}
	if p, err := s.GetPricing(ctx, "claude-opus-4-20250514", time.Time{}); err != nil || p.InputCostPerToken != 15e-6 {
		t.Errorf("dated id: %+v %v", p, err)
	}
	s = tableService(map[string]ModelPricing{"claude-sonnet-4": {InputCostPerToken: 1}, "claude-sonnet-4-20250514": {InputCostPerToken: 2}})
	if p, err := s.GetPricing(ctx, "claude-sonnet-4-20250514-via-bedrock", time.Time{}); err != nil || p.InputCostPerToken != 2 {
		t.Errorf("longest key: %+v %v", p, err)
	}
	embedded := NewService()
	embedded.SetOffline(true)
	if _, err := embedded.GetPricing(ctx, "claude-opus-4-8-20270898", time.Time{}); err != nil {
		t.Error(err)
	}
}

func TestModelsDevPricesOnlyWhatLiteLLMMisses(t *testing.T) {
	ctx := context.Background()
	s := NewService()
	s.SetOffline(true)
	get := func(model string) ModelPricing {
		p, err := s.GetPricing(ctx, model, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	// models.dev spells both ids exactly, yet LiteLLM's entries answer first.
	if p := get("anthropic/claude-haiku-4.5"); p.InputCostPerToken != get("claude-haiku-4-5").InputCostPerToken {
		t.Errorf("reseller rate won: %+v", p)
	}
	if p := get("claude-sonnet-4@20250514"); p.InputAbove == nil || *p.InputAbove != 6e-6 || p.LongContextThreshold != 0 {
		t.Errorf("LiteLLM 200K tier lost: %+v", p)
	}
	// An exact-only tier keeps its own rate instead of the base model's.
	if fast := get("claude-opus-5-fast"); fast.InputCostPerToken != s.models["claude-opus-5-fast"].InputCostPerToken || fast.InputCostPerToken == get("claude-opus-5").InputCostPerToken {
		t.Errorf("fast tier: %+v", fast)
	}
	// Hermes prices a provider-qualified snapshot entry, not the raw model's
	// DeepSeek schedule (upstream 56496325), and only for a case-exact spelling.
	at, _ := time.Parse(time.RFC3339, "2026-08-17T01:00:00Z")
	provider, err := s.GetSourcePricing(ctx, "deepseek-v4-flash", "hermes", "deepseek", at)
	raw, _ := s.GetPricing(ctx, "deepseek-v4-flash", at)
	if err != nil || provider.InputCostPerToken != s.models["deepseek/deepseek-v4-flash"].InputCostPerToken || provider.InputCostPerToken == raw.InputCostPerToken {
		t.Errorf("DeepSeek provider: %+v raw %+v %v", provider, raw, err)
	}
	if kimi, err := s.GetSourcePricing(ctx, "kimi-k2.6", "hermes", "moonshotai", at); err != nil || kimi.InputCostPerToken != get("kimi-k2.6").InputCostPerToken {
		t.Errorf("moonshotai/Kimi-K2.6 matched with case folded: %+v %v", kimi, err)
	}
}
