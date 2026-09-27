package sourcesa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// projectCodexRows is the reference for readCodexRows: every row
// encoding/json decodes, restricted to the fields parseCodex reads.
func projectCodexRows(t *testing.T, path string) []object {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []object
	for _, line := range strings.Split(string(data), "\n") {
		var m object
		if json.Unmarshal([]byte(line), &m) == nil && m != nil {
			rows = append(rows, m)
		}
	}
	for i, row := range rows {
		out := object{}
		for k, v := range row {
			if codexRowKeys[k] {
				out[k] = v
			}
		}
		if payload, ok := row["payload"]; ok {
			switch str(row["type"]) {
			case "session_meta", "turn_context", "event_msg":
				if fields, ok := payload.(object); ok {
					projected := object{}
					for k, v := range fields {
						if codexPayloadKeys[k] {
							projected[k] = v
						}
					}
					payload = projected
				}
				out["payload"] = payload
			}
		}
		rows[i] = out
	}
	return rows
}

func checkCodexRows(t *testing.T, path string) {
	t.Helper()
	got, err := readCodexRows(path)
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
		`{"type":"event_msg","payload":{"type":"token_count"},"payload":{"type":"thread_settings_applied"}}`,
		`{"type":"event_msg","payload":[1e999]}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"long text [1e999 is fine in a string]","info":null}}`,
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
