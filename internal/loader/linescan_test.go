package loader

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// projectLine is the reference behavior scanLine must match: a full
// json.Unmarshal restricted to the keys the loader reads.
func projectLine(line []byte) (map[string]interface{}, error) {
	var full map[string]interface{}
	if err := json.Unmarshal(line, &full); err != nil {
		return nil, err
	}
	if full == nil {
		return nil, nil
	}
	out := map[string]interface{}{}
	for k, v := range full {
		if !lineTopKeys[k] {
			continue
		}
		if message, ok := v.(map[string]interface{}); ok && k == "message" {
			filtered := map[string]interface{}{}
			for mk, mv := range message {
				if lineMessageKeys[mk] {
					filtered[mk] = mv
				}
			}
			v = filtered
		}
		out[k] = v
	}
	return out, nil
}

var scanLineSeeds = []string{
	`{"type":"assistant","cwd":"/w","sessionId":"s","timestamp":"2026-01-01T00:00:00Z","requestId":"r","message":{"id":"m","model":"claude-opus-4-8","content":[{"type":"text","text":"hi \"there\" \\ é \n"}],"usage":{"input_tokens":1,"output_tokens":2,"cache_creation":{"ephemeral_5m_input_tokens":3},"iterations":[{"type":"advisor_message","model":"x"}]}}}`,
	`{"type":"custom-title","sessionId":"s","customTitle":"kept","CustomTitle":false}`,
	`{"cwd":"/a","cwd":"/b","type":"user"}`,
	`{"cwd":"/escaped-key","message":{"usage":{"input_tokens":1,"output_tokens":1}}}`,
	`{"cwd":"café 😀","type":"user"}`,
	"{\"cwd\":\"bad utf8 \xff\xfe\",\"type\":\"user\"}",
	`{"message":"not an object","type":"assistant"}`,
	`{"message":null,"costUSD":1.5e-3,"cost":-0,"isSidechain":true,"isMeta":false,"id":null}`,
	`{"message":{"id":1,"model":["x"],"diagnostics":{"cache_miss_reason":{"type":"t"}}},"usage_limit_reset_time":"2026"}`,
	`{"a":[1,-2.5e+10,true,false,null,{"b":[[]]},""],"type":"x"}`,
	`{}`,
	`null`,
	`  {"type":"user"}  `,
	`[]`,
	`"str"`,
	`123`,
	`{"type":"user"`,
	`{"type":"user"}x`,
	`{"type":"user",}`,
	`{"type" "user"}`,
	`{"a":01}`,
	`{"a":1.}`,
	`{"a":-}`,
	`{"a":1e}`,
	`{"a":tru}`,
	`{"a":"\x"}`,
	`{"a":"\u12G4"}`,
	"{\"a\":\"tab\there\"}",
	`{"a":[1,]}`,
	`{"a":[1 2]}`,
	`{"cost":1e999}`,
	`{"skipped":[1e999]}`,
	`{"skipped":-1e-999}`,
	`{"skipped":1797693134862315708145274237317043567981000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000}`,
	`{,}`,
	``,
}

func TestScanLineMatchesUnmarshal(t *testing.T) {
	for _, seed := range scanLineSeeds {
		checkScanLine(t, []byte(seed))
	}
}

func FuzzScanLine(f *testing.F) {
	for _, seed := range scanLineSeeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		checkScanLine(t, line)
	})
}

func checkScanLine(t *testing.T, line []byte) {
	t.Helper()
	want, wantErr := projectLine(line)
	got, gotErr := scanLine(line)
	if (wantErr != nil) != (gotErr != nil) {
		t.Fatalf("%q: error mismatch: scanLine=%v json.Unmarshal=%v", line, gotErr, wantErr)
	}
	if wantErr == nil && !reflect.DeepEqual(got, want) {
		t.Fatalf("%q: value mismatch:\n scanLine=%#v\n  json.Unmarshal=%#v", line, got, want)
	}
}

// dedupeKey must collide exactly when the json.Marshal keys it replaced did.
// Go releases disagree on one case: newer encoding/json writes an invalid
// byte as a raw U+FFFD, merging it with a literal U+FFFD; the Go 1.23 release
// toolchain escapes it as \ufffd, keeping them apart. dedupeKey follows Go
// 1.23, so inputs that could tell the two apart are skipped elsewhere.
func FuzzDedupeKey(f *testing.F) {
	f.Add("msg_1", "req_1", "msg_1", "req_1")
	f.Add("a:b", "", "a", ":b")
	f.Add("\xff", "x", "\xfe", "x")
	f.Add("\xff", "x", "\ufffd", "x")
	f.Add("\xe2\x82", "", "\xff\xff", "")
	f.Add("1:a", "b", "1", "a1:b")
	rawReplacement := func() bool {
		a, _ := json.Marshal("\xff")
		b, _ := json.Marshal("\ufffd")
		return string(a) == string(b)
	}()
	f.Fuzz(func(t *testing.T, a1, a2, b1, b2 string) {
		if rawReplacement && strings.ContainsRune(a1+a2+b1+b2, utf8.RuneError) {
			t.Skip("this toolchain's json.Marshal merges invalid bytes with U+FFFD")
		}
		jsonKey := func(parts ...string) string { b, _ := json.Marshal(parts); return string(b) }
		for _, pair := range [][2][]string{{{a1, a2}, {b1, b2}}, {{a1}, {b1}}, {{a1, "", a2}, {b1, "", b2}}} {
			x, y := pair[0], pair[1]
			if (jsonKey(x...) == jsonKey(y...)) != (dedupeKey(x...) == dedupeKey(y...)) {
				t.Fatalf("%q vs %q: json keys equal=%v, dedupe keys equal=%v", x, y, jsonKey(x...) == jsonKey(y...), dedupeKey(x...) == dedupeKey(y...))
			}
		}
	})
}
