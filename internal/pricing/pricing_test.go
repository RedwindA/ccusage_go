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
