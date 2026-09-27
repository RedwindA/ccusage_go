package pricing

import (
	"context"
	"net/http"
	"time"
)

// Prefetch downloads and decodes the online price lists in the background so
// their network round trips and parsing overlap with loading usage data.
// models.dev is fetched speculatively: a service only consults it when a
// model is missing from LiteLLM, but by then the result is ready.
type Prefetch struct {
	lite, models *pendingPrices
	started      time.Time
}

// prefetchTTL bounds how long after StartPrefetch its results stand in for
// a fetch. A report consumes them within its first second or so; a live
// monitor that first needs a list much later fetches it then, as it would
// without prefetching.
const prefetchTTL = time.Minute

// take returns the prefetched models.dev list (or LiteLLM list, when models
// is false) if f is non-nil and still fresh.
func (f *Prefetch) take(models bool) (map[string]ModelPricing, bool) {
	if f == nil || time.Since(f.started) > prefetchTTL {
		return nil, false
	}
	if models {
		return f.models.wait(), true
	}
	return f.lite.wait(), true
}

type pendingPrices struct {
	done   chan struct{}
	prices map[string]ModelPricing
	// panicked holds a panic raised by fetch. It is re-raised by wait, so
	// a list that is never consumed cannot crash the process, while one
	// that is consumed fails exactly as fetching it directly would.
	panicked interface{}
}

func startPending(fetch func() map[string]ModelPricing) *pendingPrices {
	p := &pendingPrices{done: make(chan struct{})}
	go func() {
		defer close(p.done)
		defer func() { p.panicked = recover() }()
		p.prices = fetch()
	}()
	return p
}

func (p *pendingPrices) wait() map[string]ModelPricing {
	<-p.done
	if p.panicked != nil {
		panic(p.panicked)
	}
	return p.prices
}

// StartPrefetch begins fetching both online price lists.
func StartPrefetch(ctx context.Context) *Prefetch {
	client := &http.Client{Timeout: 10 * time.Second}
	return &Prefetch{
		started: time.Now(),
		lite:    startPending(func() map[string]ModelPricing { return fetchLite(ctx, client) }),
		models:  startPending(func() map[string]ModelPricing { return fetchModels(ctx, client) }),
	}
}

// UsePrefetch makes the service's first online refreshes consume p instead
// of fetching. Each result is consumed by at most one service.
func (s *Service) UsePrefetch(p *Prefetch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prefetch = p
}
