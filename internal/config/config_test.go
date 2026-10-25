package config

import (
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ccusage.json")
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestPrecedenceAndFieldMerge(t *testing.T) {
	c, err := Load(fixture(t, `{"defaults":{"offline":true,"pricingOverrides":{"private":{"inputCostPerToken":1,"outputCostPerToken":2}}},"commands":{"daily":{"mode":"display"},"codex:daily":{"mode":"calculate"}},"codex":{"defaults":{"offline":false},"commands":{"daily":{"pricingOverrides":{"private":{"inputCostPerToken":0}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	v := c.Options("daily", "codex")
	if v["offline"] != false || v["mode"] != "calculate" {
		t.Fatal(v)
	}
	p := c.PricingOverrides("daily", "codex")["private"]
	if p.InputCostPerToken == nil || *p.InputCostPerToken != 0 || p.OutputCostPerToken == nil || *p.OutputCostPerToken != 2 {
		t.Fatal(p)
	}
	if c.Options("daily", "")["offline"] != true {
		t.Fatal("Options mutated original config")
	}
}
func TestPricingOverridesMergeSpeedMultipliers(t *testing.T) {
	c, err := Load(fixture(t, `{"defaults":{"pricingOverrides":{"model-a":{"inputCostPerToken":3,"fastMultiplier":2,"flexMultiplier":0.75},"model-b":{"fastMultiplier":2,"flexMultiplier":0.75}}},"commands":{"daily":{"pricingOverrides":{"model-a":{"inputCostPerToken":2,"fastMultiplier":3,"flexMultiplier":0.5},"model-b":{"maxInputTokens":1000000},"new-model":{"inputCostPerToken":1,"flexMultiplier":0.5}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	overrides := c.PricingOverrides("daily", "codex")
	for name, want := range map[string][2]float64{"model-a": {3, .5}, "model-b": {2, .75}} {
		p := overrides[name]
		if p.FastMultiplier == nil || *p.FastMultiplier != want[0] || p.FlexMultiplier == nil || *p.FlexMultiplier != want[1] {
			t.Errorf("%s: %+v", name, p)
		}
	}
	if p := overrides["new-model"]; p.FlexMultiplier == nil || *p.FlexMultiplier != .5 || p.InputCostPerToken == nil || *p.InputCostPerToken != 1 {
		t.Errorf("new-model: %+v", p)
	}
}
func TestValidation(t *testing.T) {
	for _, s := range []string{`{"defaults":{"since":"2026-02-30"}}`, `{"defaults":{"mode":"bad"}}`, `{"defaults":{"pricingOverrides":{"x":{"inputCostPerToken":-1}}}}`, `{"pi":{"stores":[{"name":"codex","path":"/tmp"}]}}`, `{"pi":{"stores":[{"name":"custom","path":""}]}}`} {
		if c, err := Load(fixture(t, s)); err == nil && c.Validate("daily", "") == nil {
			t.Errorf("accepted %s", s)
		}
	}
}
func TestNamedStores(t *testing.T) {
	c, err := Load(fixture(t, `{"pi":{"stores":[{"name":"omp","path":"~/.omp/agent/sessions"}]}}`))
	if err != nil || len(c.PiStores) != 1 || c.PiStores[0].Name != "omp" {
		t.Fatalf("%+v %v", c, err)
	}
}
func TestAliasMapsReplaceWhilePricingMerges(t *testing.T) {
	c, err := Load(fixture(t, `{"defaults":{"modelLabelAliases":{"a":"A","b":"B"}},"commands":{"statusline":{"modelLabelAliases":{"a":"New"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	aliases := c.Options("statusline", "")["modelLabelAliases"].(map[string]any)
	if len(aliases) != 1 || aliases["a"] != "New" {
		t.Fatal(aliases)
	}
}
func TestDiscoverySkipsMalformedAndPrefersFirstReadableConfig(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(first, "ccusage.json"), []byte("malformed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "ccusage.json"), []byte(`{"defaults":{"mode":"display"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", first+","+second)
	c, err := Load("")
	if err != nil || c.Options("daily", "")["mode"] != "display" {
		t.Fatalf("%+v %v", c, err)
	}
}
func TestValidationIsScopedToActiveCommandAndNamedStoreReaders(t *testing.T) {
	c, err := Load(fixture(t, `{"commands":{"weekly":{"since":"2026-02-30"}},"pi":{"stores":[{"name":"bad"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Validate("daily", "codex"); err != nil {
		t.Fatalf("unrelated command blocked: %v", err)
	}
	if err = c.Validate("statusline", "claude"); err != nil {
		t.Fatalf("statusline reads irrelevant store: %v", err)
	}
	if c.Validate("weekly", "") == nil {
		t.Fatal("invalid active weekly config accepted")
	}
	if c.Validate("daily", "") == nil {
		t.Fatal("invalid named store accepted for unified daily")
	}
}
