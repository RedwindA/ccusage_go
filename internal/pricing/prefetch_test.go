package pricing

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestPendingPricesPanicsOnlyWhenConsumed(t *testing.T) {
	p := startPending(func() map[string]ModelPricing { panic("bad schema") })
	<-p.done // an unconsumed failure must not crash the process
	defer func() {
		if r := recover(); r != "bad schema" {
			t.Fatalf("recovered %v, want the fetch panic", r)
		}
	}()
	p.wait()
	t.Fatal("wait returned instead of panicking")
}

func TestStalePrefetchIsFetchedAgain(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	stale := map[string]ModelPricing{"stale-model-1": {InputCostPerToken: 1}}
	for _, age := range []time.Duration{0, 2 * prefetchTTL} {
		s := NewService()
		tr := &catalogTransport{lite: `{"fresh-lite-1":{"input_cost_per_token":0.000007,"output_cost_per_token":0.000009}}`, models: `{"openai":{"models":{"fresh-model-9":{"cost":{"input":2,"output":8}}}}}`}
		s.client = &http.Client{Transport: tr}
		s.UsePrefetch(&Prefetch{
			started: time.Now().Add(-age),
			lite:    startPending(func() map[string]ModelPricing { return stale }),
			models:  startPending(func() map[string]ModelPricing { return stale }),
		})
		ctx := context.Background()
		fresh, _ := s.GetPricing(ctx, "fresh-model-9", time.Time{})
		old, _ := s.GetPricing(ctx, "stale-model-1", time.Time{})
		if age == 0 && (len(tr.requests) != 0 || old.InputCostPerToken != 1) {
			t.Fatalf("fresh prefetch: requests %v, stale-model-1 %+v", tr.requests, old)
		}
		if age > 0 && (len(tr.requests) != 2 || fresh.InputCostPerToken != 2e-6 || old.InputCostPerToken == 1) {
			t.Fatalf("expired prefetch: requests %v, fresh-model-9 %+v, stale-model-1 %+v", tr.requests, fresh, old)
		}
	}
}
