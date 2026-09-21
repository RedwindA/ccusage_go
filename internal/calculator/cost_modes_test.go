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
