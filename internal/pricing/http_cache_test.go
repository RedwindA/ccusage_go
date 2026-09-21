package pricing

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestPricingHTTPCacheRevalidatesAndDoesNotUseUnconfirmedBody(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	url := "https://raw.githubusercontent.com/pricing-test.json"
	body := `{"test":{"input_cost_per_token":1,"output_cost_per_token":2}}`
	calls := 0
	s := NewService()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		status, data := 200, body
		if calls > 1 {
			if r.Header.Get("If-None-Match") != `"v1"` {
				t.Fatal("ETag missing")
			}
			status, data = 304, ""
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(data)), Header: http.Header{"Etag": {`"v1"`}}}, nil
	})}
	if string(s.fetch(context.Background(), url)) != body || string(s.fetch(context.Background(), url)) != body || calls != 2 {
		t.Fatal("ETag revalidation failed")
	}
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	if s.fetch(context.Background(), url) != nil {
		t.Fatal("unconfirmed stale cache was used")
	}
}
func TestInvalidCachedBodyRetriesWithoutETag(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	url := "https://raw.githubusercontent.com/pricing-test.json"
	writePricingCache(pricingCachePath(url), `"bad"`, []byte(`{"not":"prices"}`))
	calls := 0
	s := NewService()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		status, body := 304, ""
		if calls == 2 {
			if r.Header.Get("If-None-Match") != "" {
				t.Fatal("invalid ETag reused")
			}
			status, body = 200, `{"test":{"input_cost_per_token":1,"output_cost_per_token":2}}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	if len(s.fetch(context.Background(), url)) == 0 || calls != 2 {
		t.Fatal("bad cache did not retry")
	}
	if _, err := os.Stat(pricingCachePath(url)); !os.IsNotExist(err) {
		t.Fatal("invalid cache retained")
	}
}
