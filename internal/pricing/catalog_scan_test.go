package pricing

import (
	"encoding/json"
	"reflect"
	"regexp"
	"testing"
)

// projectCatalog is the reference for decodeCatalogObject: a full
// encoding/json decode restricted to catalogKey fields.
func projectCatalog(v any) any {
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return v
	}
	out := map[string]any{}
	for k, v := range m {
		if !catalogKey([]byte(k)) {
			continue
		}
		if models, ok := v.(map[string]any); ok && k == "models" {
			projected := map[string]any{}
			for name, model := range models {
				projected[name] = projectCatalog(model)
			}
			v = projected
		}
		out[k] = v
	}
	return out
}

var catalogSeeds = []string{
	`{"id":"anthropic","name":"Anthropic","models":{"claude-x":{"id":"claude-x","name":"X","release_date":"2026","modalities":{"input":["text"],"output":["text"]},"cost":{"input":1,"output":2,"tiers":[{"tier":{"type":"context","size":200000}}]},"limit":{"context":1000}}}}`,
	`{"models":{"a":"not an object","b":null,"c":{"Cost":{"input":1},"COST":2,"exactonly":true,"LIMIT":{}}}}`,
	`{"models":"not an object","cost":{"input":1,"output":1}}`,
	`{"models":{"a":{"models":{"nested":{"cost":1,"junk":2}}}}}`,
	`{"id":1,"models":{},"models":{"x":{"cost":{"input":1,"output":1}}}}`,
	`null`,
	`[]`,
	`{"models":{"a":{"cost":1e999}}}`,
	`{"junk":[1e999]}`,
	`{"models":{"a":{"cost":1}}`,
}

func TestDecodeCatalogObjectMatchesProjection(t *testing.T) {
	for _, seed := range catalogSeeds {
		checkCatalogObject(t, []byte(seed))
	}
}

func FuzzDecodeCatalogObject(f *testing.F) {
	for _, seed := range catalogSeeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		checkCatalogObject(t, data)
	})
}

func checkCatalogObject(t *testing.T, data []byte) {
	t.Helper()
	var full map[string]any
	wantErr := json.Unmarshal(data, &full)
	got, gotErr := decodeCatalogObject(data)
	if (wantErr != nil) != (gotErr != nil) {
		t.Fatalf("%q: error mismatch: got %v, encoding/json %v", data, gotErr, wantErr)
	}
	if wantErr != nil {
		return
	}
	var want map[string]any
	if full != nil {
		want = projectCatalog(full).(map[string]any)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q: mismatch:\n got  %#v\n want %#v", data, got, want)
	}
}

func FuzzRawFields(f *testing.F) {
	for _, seed := range catalogSeeds {
		f.Add([]byte(seed))
	}
	f.Add([]byte(`{"a" : [ 1 , 2 ] , "b":null, "a":{"x" :1}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var want map[string]json.RawMessage
		wantErr := json.Unmarshal(data, &want)
		got, gotErr := rawFields(data)
		if (wantErr != nil) != (gotErr != nil) {
			t.Fatalf("%q: error mismatch: got %v, encoding/json %v", data, gotErr, wantErr)
		}
		if wantErr == nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: mismatch:\n got  %q\n want %q", data, got, want)
		}
	})
}

func FuzzStripDateSuffix(f *testing.F) {
	for _, seed := range []string{"claude-sonnet-4-20250514", "gpt-4o-2024-08-06", "x-2024-0806", "-12345678", "a-123456789", "2024-01-01", "-2024-01-01", "m-2024-1-01", "m-20240101-", "", "-", "model-v1:0", "a-١٢٣٤٥٦٧٨"} {
		f.Add(seed)
	}
	re := regexp.MustCompile(`-(?:[0-9]{8}|[0-9]{4}-[0-9]{2}-[0-9]{2})$`)
	f.Fuzz(func(t *testing.T, s string) {
		if got, want := stripDateSuffix(s), re.ReplaceAllString(s, ""); got != want {
			t.Fatalf("%q: got %q, regexp %q", s, got, want)
		}
	})
}
