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
