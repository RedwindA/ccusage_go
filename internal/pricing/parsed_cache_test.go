package pricing

import (
	"os"
	"reflect"
	"testing"
	"unsafe"
)

// fillEveryField sets every field of p, exported or not, to a distinct
// non-zero value, so a field the encoding forgets fails the round trip.
func fillEveryField(p *ModelPricing, seed float64) {
	v := reflect.ValueOf(p).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		f = reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
		value := seed + float64(i)
		switch f.Kind() {
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Float64:
			f.SetFloat(value)
		case reflect.Int:
			f.SetInt(int64(value) * -3)
		case reflect.Pointer:
			f.Set(reflect.ValueOf(&value))
		default:
			panic("fillEveryField: unhandled kind " + f.Kind().String())
		}
	}
}

func TestPriceEncodingCoversFields(t *testing.T) {
	var full ModelPricing
	fillEveryField(&full, 1.5)
	prices := map[string]ModelPricing{"full": full, "zero": {}, "": {FastMultiplier: 1}, "ünïcode/model@x": {InputAbove: new(float64)}}
	got, err := decodePrices(encodePrices(prices))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, prices) {
		t.Fatalf("round trip mismatch:\n got  %+v\n want %+v", got, prices)
	}
}

func TestDecodePricesRejectsDamage(t *testing.T) {
	var full ModelPricing
	fillEveryField(&full, 2)
	encoded := encodePrices(map[string]ModelPricing{"a": full, "b": {}})
	for n := 0; n < len(encoded); n++ {
		if _, err := decodePrices(encoded[:n]); err == nil {
			t.Fatalf("truncated to %d bytes: expected error", n)
		}
	}
	if _, err := decodePrices(append(encoded, 0)); err == nil {
		t.Fatal("trailing byte: expected error")
	}
}

func TestCachedParse(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	old := buildIdentity
	defer func() { buildIdentity = old }()
	identity := "rev-a"
	buildIdentity = func() string { return identity }

	calls := 0
	parse := func(data []byte) map[string]ModelPricing {
		calls++
		return map[string]ModelPricing{string(data): {InputCostPerToken: float64(len(data))}}
	}
	check := func(body string, wantCalls int) {
		t.Helper()
		got := cachedParse(liteURL, []byte(body), parse)
		want := map[string]ModelPricing{body: {InputCostPerToken: float64(len(body))}}
		if !reflect.DeepEqual(got, want) || calls != wantCalls {
			t.Fatalf("body %q: got %v after %d parses, want %v after %d", body, got, calls, want, wantCalls)
		}
	}
	check("one", 1)
	check("one", 1) // cached
	check("two", 2) // different body
	check("two", 2)
	identity = "rev-b"
	check("two", 3) // different build
	path := pricingCachePath(liteURL) + ".parsed"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-5] ^= 1 // corrupt the payload; the checksum catches it
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	check("two", 4)
	identity = "" // unidentifiable build: never cache
	check("two", 5)
	check("two", 6)
}
