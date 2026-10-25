package pricing

import (
	"context"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

type catalogTransport struct {
	requests     []string
	lite, models string
}

func (t *catalogTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, r.URL.Host)
	body := t.lite
	if r.URL.Host == "models.dev" {
		body = t.models
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}
func TestLiveCatalogTrustDetailsAndAssetGate(t *testing.T) {
	raw := `{"reseller":{"models":{"future-model-9":{"cost":{"input":99,"output":99}}}},"openai":{"models":{"future-model-9":{"cost":{"input":2,"output":8,"cache_read":0.2}},"gpt-image-1":{"cost":{"input":20,"output":40}}}},"azure":{"models":{"future-model-9":{"cost":{"input":30,"output":60}},"detail-model-8":{"cost":{"input":3,"output":6}}}},"google-vertex":{"models":{"detail.model.8":{"cost":{"input":4,"output":8,"cache_read":0.4}}}}}`
	p := parseModelsCatalog([]byte(raw))
	if p["future-model-9"].InputCostPerToken != 2e-6 {
		t.Fatal(p["future-model-9"])
	}
	if p["detail.model.8"].InputCostPerToken != 4e-6 {
		t.Fatal(p)
	}
	if _, ok := p["detail-model-8"]; ok {
		t.Fatal("normalized losing spelling retained")
	}
	if _, ok := p["gpt-image-1"]; ok {
		t.Fatal("asset prices treated as tokens")
	}
}
func TestOnlineRefreshPrimaryThenModelsFallback(t *testing.T) {
	s := NewService()
	tr := &catalogTransport{lite: `{"authoritative-v1":{"input_cost_per_token":0.000007,"output_cost_per_token":0.000009}}`, models: `{"openai":{"models":{"authoritative-v1":{"cost":{"input":99,"output":99}},"future-model-9":{"cost":{"input":2,"output":8}}}}}`}
	s.client = &http.Client{Transport: tr}
	p, err := s.GetPricing(context.Background(), "authoritative-v1", time.Time{})
	if err != nil || p.InputCostPerToken != 7e-6 || len(tr.requests) != 1 {
		t.Fatalf("primary: %+v %v %v", p, err, tr.requests)
	}
	p, err = s.GetPricing(context.Background(), "future-model-9", time.Time{})
	if err != nil || p.InputCostPerToken != 2e-6 || len(tr.requests) != 2 {
		t.Fatalf("fallback: %+v %v %v", p, err, tr.requests)
	}
	p, _ = s.GetPricing(context.Background(), "authoritative-v1", time.Time{})
	if p.InputCostPerToken != 7e-6 {
		t.Fatal("fallback replaced authoritative price")
	}
}
func TestPinnedSnapshotAndProviderFacts(t *testing.T) {
	s := NewService()
	s.SetOffline(true)
	for _, tc := range []struct {
		model         string
		input, create float64
	}{{"gpt-5.2-codex", 1.75e-6, 1.75e-6}, {"gpt-5.1-codex", 1.25e-6, 1.25e-6}, {"claude-3-opus", 15e-6, 18.75e-6}} {
		p, err := s.GetPricing(context.Background(), tc.model, time.Time{})
		if err != nil || math.Abs(p.InputCostPerToken-tc.input) > 1e-15 || math.Abs(p.CacheCreationInputTokenCost-tc.create) > 1e-15 {
			t.Errorf("%s %+v %v", tc.model, p, err)
		}
	}
	p, _ := s.GetPricing(context.Background(), "zai/glm-4.5", time.Time{})
	if p.CacheCreationInputTokenCost != 0 {
		t.Fatal(p)
	}
}

// Ported from upstream's dated_openai_models_inherit_published_fast_multipliers,
// applies_speed_multipliers_to_qualified_flat_models_dev_models and
// embedded_pricing_includes_claude_fast_multiplier_for_provider_models.
func TestFastMultiplierForDatedAndProviderIDs(t *testing.T) {
	for model, want := range map[string]float64{"gpt-6-astra-2026-09-24": 2, "gpt-5.4-2026-03-05": 2, "openai/gpt-5.6-sol-20261001": 2, "gpt-5.6-terra-2026-10-01": 2, "gpt-5.6-luna-2026-10-01": 2, "global.openai.gpt-5.5-2026-04-23": 2.5, "gpt-5.2-codex-2026-01-14": 1, "gpt-5.4-unsupported": 1} {
		p := litePricing([]byte(`{"` + model + `":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`))
		if got := p[model].FastMultiplier; got != want {
			t.Errorf("LiteLLM %s: %v, want %v", model, got, want)
		}
	}
	for model, want := range map[string]float64{"openai/gpt-6-astra": 2, "openai.gpt-5.4": 2, "global.openai.gpt-6.1-sol": 2, "global.openai.gpt-5.4-mini-2026-03-17": 1, "openai.gpt-5.4-mini-extra": 1} {
		p := modelsPricing([]byte(`{"` + model + `":{"cost":{"input":10,"output":50}}}`))
		if got := p[model].FastMultiplier; got != want {
			t.Errorf("models.dev %s: %v, want %v", model, got, want)
		}
	}
	s := NewService()
	s.SetOffline(true)
	for model, want := range map[string]float64{"anthropic.claude-opus-4-6-v1": 6, "anthropic.claude-opus-4-7": 6, "anthropic.claude-opus-4-8": 2} {
		if p, err := s.GetPricing(context.Background(), model, time.Time{}); err != nil || p.FastMultiplier != want {
			t.Errorf("%s: %+v %v", model, p, err)
		}
	}
}

// Ported from upstream's long_context_overlay_survives_litellm_refresh_and_defers_to_upstream.
func TestModelsDevTiersFillOnlyFlatLiteLLMRates(t *testing.T) {
	live := parseLite([]byte(`{"gpt-5.5":{"input_cost_per_token":0.000006,"output_cost_per_token":0.000031},"gpt-5.5-2026-04-23":{"input_cost_per_token":0.000006,"output_cost_per_token":0.000031}}`))
	for _, model := range []string{"gpt-5.5", "gpt-5.5-2026-04-23"} {
		if p := live[model]; p.InputAbove == nil || *p.InputAbove != 10e-6 || p.LongContextThreshold != 272000 {
			t.Errorf("%s: %+v", model, p)
		}
	}
	live = parseLite([]byte(`{"gpt-5.5":{"input_cost_per_token":0.000006,"output_cost_per_token":0.000031,"input_cost_per_token_above_200k_tokens":0.000012}}`))
	if p := live["gpt-5.5"]; p.InputAbove == nil || *p.InputAbove != 12e-6 || p.OutputAbove != nil || p.LongContextThreshold != 0 {
		t.Errorf("published tier replaced: %+v", p)
	}
}

// Like upstream's find_with_fallbacks, the live models.dev catalog prices
// what LiteLLM misses before the embedded snapshot does, so a snapshot id
// live no longer lists loses to a live fuzzy match, and live's exact-only
// ids still keep a fuzzy LiteLLM match off their tier
// (live_exact_only_fallback_beats_a_fuzzy_primary_match).
func TestLiveModelsDevPricesBeforeTheEmbeddedSnapshot(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ctx := context.Background()
	s := tableService(map[string]ModelPricing{"acme": {InputCostPerToken: 1e-6}})
	s.offline = false
	s.models = map[string]ModelPricing{"vendor/acme4-ft-31b": {InputCostPerToken: 2e-6, MaxInputTokens: 1000}, "acme5": {InputCostPerToken: 3e-6}}
	s.client = &http.Client{Transport: &catalogTransport{lite: `{"x":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000001}}`, models: `{"openai":{"models":{"acme4":{"cost":{"input":5,"output":5},"limit":{"context":2000}},"acme-turbo":{"cost":{"input":7,"output":7}}}}}`}}
	for model, want := range map[string]float64{"acme4-ft-31b": 5e-6, "acme5": 3e-6, "acme-turbo": 7e-6} {
		if p, err := s.GetPricing(ctx, model, time.Time{}); err != nil || p.InputCostPerToken != want {
			t.Errorf("%s: %+v %v, want %v", model, p, err, want)
		}
	}
	if limit, ok := s.ContextLimit(ctx, "acme4-ft-31b"); !ok || limit != 2000 {
		t.Errorf("context limit: %d %v, want 2000", limit, ok)
	}
}
