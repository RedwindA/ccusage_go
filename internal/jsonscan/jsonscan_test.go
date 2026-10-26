package jsonscan

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"math/bits"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

var seeds = []string{
	`{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":"hi \"there\" \\ é \n"}],"usage":{"input_tokens":1,"output_tokens":2}}}`,
	`{"a":1,"a":2,"A":3}`,
	`{"cwd":"/x","k\"q":1,"":0}`,
	`"café 😀 \ud83d x \ude00 \ud83dA \/ \b\f\n\r\t"`,
	"\"bad utf8 \xff\xfe \xe2\x82\"",
	"{\"k\xff\":1}",
	`[]`, `{}`, `[[],{},[{}]]`, `null`, `true`, `false`, `""`,
	`0`, `-0`, `12`, `-12`, `1.5`, `-1.5e-3`, `1E+2`, `123456789012345`, `-999999999999999`, `1700000000000`, `1234567890123456`, `9007199254740993`,
	`1e999`, `-1e999`, `1e-999`, `[1e999]`, `{"a":1e400}`,
	`  {"a" : [ 1 , 2 ] }  `,
	`{"a":1`, `{"a":1}x`, `{"a":1,}`, `{"a" 1}`, `{"a":01}`, `{"a":1.}`, `{"a":-}`, `{"a":1e}`, `{"a":tru}`,
	`{"a":"\x"}`, `{"a":"\u12G4"}`, "{\"a\":\"tab\there\"}", `[1,]`, `[1 2]`, `{,}`, ``, ` `, `nul`, `nullx`,
	`"\'"`, `{1:2}`, `[`, `]`, `"`, `"\`, `"\u00`,
}

func TestMatchesEncodingJSON(t *testing.T) {
	for _, seed := range seeds {
		check(t, []byte(seed))
	}
}

func TestMaxDepth(t *testing.T) {
	for _, depth := range []int{maxDepth, maxDepth + 1} {
		check(t, []byte(strings.Repeat("[", depth)+strings.Repeat("]", depth)))
		check(t, []byte(strings.Repeat(`{"a":`, depth)+"1"+strings.Repeat("}", depth)))
	}
}

func FuzzMatchesEncodingJSON(f *testing.F) {
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		check(t, data)
	})
}

func check(t *testing.T, data []byte) {
	t.Helper()
	var want interface{}
	wantErr := json.Unmarshal(data, &want)
	got, gotErr := Decode(data)
	compare(t, "Decode", data, got, gotErr, want, wantErr)

	var wantMap map[string]interface{}
	wantMapErr := json.Unmarshal(data, &wantMap)
	gotMap, gotMapErr := DecodeObject(data)
	compare(t, "DecodeObject", data, gotMap, gotMapErr, wantMap, wantMapErr)

	// FieldsStrict with in-place decoding, stored last-one-wins, must
	// rebuild the same map; so must a walk that decodes only every other
	// member and leaves the rest to be skipped.
	for _, every := range []int{1, 2} {
		var fields map[string]interface{}
		n := 0
		null, fieldsErr := FieldsStrict(data, func(key []byte, value Value) error {
			if fields == nil {
				fields = map[string]interface{}{}
			}
			n++
			if n%every != 0 {
				return nil
			}
			v, err := value.Decode()
			fields[string(key)] = v
			return err
		})
		if fieldsErr == nil && !null && fields == nil {
			fields = map[string]interface{}{}
		}
		if every == 1 {
			compare(t, "FieldsStrict", data, fields, fieldsErr, wantMap, wantMapErr)
		} else if (fieldsErr != nil) != (wantMapErr != nil) {
			t.Fatalf("FieldsStrict(%q) skipping: error mismatch: got %v, encoding/json %v", data, fieldsErr, wantMapErr)
		}
	}

	// Nested walks must match a full decode as well.
	var nested map[string]interface{}
	null, nestedErr := FieldsStrict(data, func(key []byte, value Value) error {
		if nested == nil {
			nested = map[string]interface{}{}
		}
		v, err := walk(value)
		nested[string(key)] = v
		return err
	})
	if nestedErr == nil && !null && nested == nil {
		nested = map[string]interface{}{}
	}
	compare(t, "nested FieldsStrict", data, nested, nestedErr, wantMap, wantMapErr)

	// Fields with Raw must match decoding into map[string]json.RawMessage,
	// which checks syntax only.
	var wantRaw map[string]json.RawMessage
	wantRawErr := json.Unmarshal(data, &wantRaw)
	var raw map[string]json.RawMessage
	null, rawErr := Fields(data, func(key []byte, value Value) error {
		if raw == nil {
			raw = map[string]json.RawMessage{}
		}
		b, err := value.Raw()
		raw[string(key)] = b
		return err
	})
	if rawErr == nil && !null && raw == nil {
		raw = map[string]json.RawMessage{}
	}
	if (rawErr != nil) != (wantRawErr != nil) {
		t.Fatalf("Fields(%q): error mismatch: got %v, encoding/json %v", data, rawErr, wantRawErr)
	}
	if wantRawErr == nil && !reflect.DeepEqual(raw, wantRaw) {
		t.Fatalf("Fields(%q): value mismatch:\n got  %q\n want %q", data, raw, wantRaw)
	}
	// Leaving every value to be skipped must accept and reject the same
	// inputs.
	_, skipErr := Fields(data, func([]byte, Value) error { return nil })
	if (skipErr != nil) != (wantRawErr != nil) {
		t.Fatalf("Fields(%q) skipping: error mismatch: got %v, encoding/json %v", data, skipErr, wantRawErr)
	}
}

// walk decodes a value by descending into objects with Value.Fields and
// into arrays with Value.Elements.
func walk(value Value) (interface{}, error) {
	if value.Kind() == '[' {
		a := []interface{}{}
		err := value.Elements(func(v Value) error {
			x, err := walk(v)
			a = append(a, x)
			return err
		})
		return a, err
	}
	if value.Kind() != '{' {
		return value.Decode()
	}
	m := map[string]interface{}{}
	_, err := value.Fields(func(key []byte, v Value) error {
		x, err := walk(v)
		m[string(key)] = x
		return err
	})
	return m, err
}

func compare(t *testing.T, name string, data []byte, got interface{}, gotErr error, want interface{}, wantErr error) {
	t.Helper()
	if (gotErr != nil) != (wantErr != nil) {
		t.Fatalf("%s(%q): error mismatch: got %v, encoding/json %v", name, data, gotErr, wantErr)
	}
	if wantErr == nil && !sameJSON(got, want) {
		t.Fatalf("%s(%q): value mismatch:\n got  %#v\n want %#v", name, data, got, want)
	}
}

// sameJSON is reflect.DeepEqual that also distinguishes -0 from 0.
func sameJSON(a, b interface{}) bool {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && x == y && math.Signbit(x) == math.Signbit(y)
	case []interface{}:
		y, ok := b.([]interface{})
		if !ok || len(x) != len(y) || (x == nil) != (y == nil) {
			return false
		}
		for i := range x {
			if !sameJSON(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]interface{}:
		y, ok := b.(map[string]interface{})
		if !ok || len(x) != len(y) || (x == nil) != (y == nil) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !sameJSON(v, w) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}

// stopMask's lowest bit must mark a word's first stop byte exactly, even
// where borrows from that byte mark others above it.
func TestStopMask(t *testing.T) {
	first := func(w uint64) int {
		for i := 0; i < 8; i++ {
			if c := byte(w >> (8 * i)); c == '"' || c == '\\' || c < 0x20 {
				return i
			}
		}
		return 8
	}
	got := func(w uint64) int { return bits.TrailingZeros64(stopMask(w)) >> 3 }
	for c := 0; c < 256; c++ {
		for pos := 0; pos < 8; pos++ {
			word := []byte("abcdefgh")
			word[pos] = byte(c)
			w := binary.LittleEndian.Uint64(word)
			if got(w) != first(w) {
				t.Fatalf("byte %#x at %d: got stop %d, want %d", c, pos, got(w), first(w))
			}
		}
	}
	rng := rand.New(rand.NewSource(1))
	for n := 0; n < 1_000_000; n++ {
		w := rng.Uint64()
		switch n % 3 {
		case 0: // bias toward high bytes and near-threshold values
			w |= 0x8080808080808080 &^ (rng.Uint64() & rng.Uint64())
		case 1: // runs of stops, whose borrows chain
			w &= rng.Uint64() | 0x1f1f1f1f1f1f1f1f
		}
		if got(w) != first(w) {
			t.Fatalf("%#016x: got stop %d, want %d", w, got(w), first(w))
		}
	}
}

// benchLine is a transcript line shaped like Claude Code's: short metadata
// members, then long text with escapes in nested content.
var benchLine = func() []byte {
	text := strings.Repeat(`Reading the file shows \"parseFile\" calls scanLine for each line.\nIt then decodes usage.\n`, 40)
	return []byte(`{"parentUuid":"6f1c0e1a-2b8d-4c3e-9f1a-0b2c3d4e5f60","isSidechain":false,"userType":"external",` +
		`"cwd":"/home/user/project","sessionId":"0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9","version":"2.1.3","gitBranch":"main",` +
		`"message":{"id":"msg_01ABCDEFGHIJKLMNOPQRSTUV","type":"message","role":"assistant","model":"claude-opus-4-5",` +
		`"content":[{"type":"thinking","thinking":"` + text + `","signature":"EqQBCkgIBRABGAIiQ"},` +
		`{"type":"tool_use","id":"toolu_01","name":"Edit","input":{"file_path":"/home/user/project/main.go","old_string":"` + text + `","new_string":"x"}}],` +
		`"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":3,"cache_creation_input_tokens":2048,"cache_read_input_tokens":40960,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":2048},"output_tokens":512,"service_tier":"standard"}},` +
		`"requestId":"req_011CABCDEFGHIJKLMNOPQRST","type":"assistant","uuid":"7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d","timestamp":"2026-01-02T03:04:05.678Z"}`)
}()

func BenchmarkFieldsStrict(b *testing.B) {
	b.SetBytes(int64(len(benchLine)))
	for b.Loop() {
		if _, err := FieldsStrict(benchLine, func([]byte, Value) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
}
