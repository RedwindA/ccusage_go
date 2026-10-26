package sourcesa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// projectCodexRows is the reference for readCodexRows: every row
// encoding/json decodes, restricted to the fields parseCodex reads, with
// the numbers codexNumbers keeps.
func projectCodexRows(t *testing.T, path string) []object {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []object
	for _, line := range strings.Split(string(data), "\n") {
		var m object
		if json.Unmarshal([]byte(line), &m) != nil || m == nil {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.UseNumber()
		m = nil
		if err := decoder.Decode(&m); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, m)
	}
	for i, row := range rows {
		out := object{}
		for k, v := range row {
			if codexRowKeys[k] {
				out[k] = v
			}
		}
		codexNumbers(out, "parent")
		if payload, ok := row["payload"]; ok {
			switch str(row["type"]) {
			case "session_meta", "turn_context", "event_msg", "token_usage_record", "compacted":
				kind := ""
				if fields, ok := payload.(object); ok {
					projected := object{}
					for k, v := range fields {
						if codexPayloadKeys[k] {
							projected[k] = v
						}
					}
					payload, kind = projected, "parent"
				}
				out["payload"] = codexNumbers(payload, kind)
			}
		}
		rows[i] = out
	}
	return rows
}

// codexNumbers turns the json.Numbers in v back into float64s, as
// encoding/json decodes them, except a member of a usage object (kind
// "usage") not written as an unsigned integer. A parent object's members
// that codexUsageKeys or codexUsageParents name are usage or parent objects.
func codexNumbers(v interface{}, kind string) interface{} {
	switch v := v.(type) {
	case json.Number:
		f, _ := v.Float64()
		return f
	case object:
		for k, e := range v {
			if n, ok := e.(json.Number); ok && kind == "usage" {
				if _, err := strconv.ParseUint(string(n), 10, 64); err != nil {
					continue
				}
			}
			member := ""
			if _, ok := e.(object); ok && kind == "parent" && codexUsageKeys[k] {
				member = "usage"
			} else if ok && kind == "parent" && codexUsageParents[k] {
				member = "parent"
			}
			v[k] = codexNumbers(e, member)
		}
	case []interface{}:
		for i, e := range v {
			v[i] = codexNumbers(e, "")
		}
	}
	return v
}

func checkCodexRows(t *testing.T, path string) {
	t.Helper()
	got, _, err := readCodexRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := projectCodexRows(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: readCodexRows mismatch:\n got  %#v\n want %#v", path, got, want)
	}
}

func TestReadCodexRowsMatchesReadLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	lines := []string{
		`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"s","cwd":"/w"}}`,
		`{"payload":{"model":"gpt-5.5"},"type":"turn_context"}`,
		`{"type":" event_msg ","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":3}}}}`,
		`{"type":"response_item","payload":{"type":"message","content":[{"text":"\"usage\" is not a key here"}]}}`,
		`{"type":"turn_context","payload":{"model":"escaped-key"}}`,
		`{"type":"response_item","usage":{"input_tokens":1},"model":"m","data":{"usage":{"output_tokens":2}},"createdAt":1700000000}`,
		`{"type":"response_item","usage":{"input_tokens":1.0,"output_tokens":"2","details":{"n":1.5}},"data":{"usage":{"output_tokens":-0},"x":2.5,"result":{"usage":{"input_tokens":1e1}}},"response":[{"usage":{"input_tokens":1.5}}],"createdAt":1.7e9}`,
		`{"timestamp":"2026-01-01T00:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":3.5},"last_token_usage":{"input_tokens":18446744073709551616},"rate":0.5},"thread_settings":{"usage":{"input_tokens":2.5}}}}`,
		`{"type":"event_msg","payload":{"type":"token_count"},"payload":{"type":"thread_settings_applied"}}`,
		`{"type":"event_msg","payload":[1e999]}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"long text [1e999 is fine in a string]","info":null}}`,
		`{"timestamp":"2026-01-01T00:00:01Z","type":"token_usage_record","payload":{"response_id":"r","usage":{"input_tokens":3},"thread_token_usage":{"input_tokens":9},"output":"skipped"}}`,
		`{"type":"compacted","payload":{"message":"summary","replacement_history":[{"type":"message","content":"skipped"}],"compaction_response_id":"r"}}`,
		`{"type":"session_meta","payload":{"id":"s2","instructions":"skipped","source":{"subagent":{"thread_spawn":{"parent_thread_id":"p"}}}}}`,
		`{"type":"event_msg","payload":{"type":"x","junk":1e999}}`,
		`{"type":"event_msg","payload":"not an object"}`,
		`{"type":"event_msg","payload":null}`,
		`not json`,
		`null`,
		``,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	checkCodexRows(t, path)
}

// CODEX_ROWS_DIR checks readCodexRows against real session files.
func TestReadCodexRowsRealData(t *testing.T) {
	dir := os.Getenv("CODEX_ROWS_DIR")
	if dir == "" {
		t.Skip("CODEX_ROWS_DIR not set")
	}
	n := 0
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && filepath.Ext(p) == ".jsonl" {
			checkCodexRows(t, p)
			n++
		}
		return nil
	})
	t.Logf("checked %d files", n)
}
