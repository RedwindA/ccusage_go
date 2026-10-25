package commands

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sourceCLIWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func sourceCLIDB(t *testing.T, path, schema string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func sourceCLIRun(args ...string) (stdout, stderr string, err error) {
	cmd := NewRootCommand("test")
	var out, diagnostics bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostics)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), diagnostics.String(), err
}
func sourceCLIJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	out, diagnostics, err := sourceCLIRun(args...)
	if err != nil {
		t.Fatalf("%v stderr=%s stdout=%s", err, diagnostics, out)
	}
	var payload map[string]any
	if err = json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("stdout is not a JSON document: %v\n%s\nstderr=%s", err, out, diagnostics)
	}
	return payload
}
func sourceCLIProtoInt(field, value uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, field<<3), value)
}
func sourceCLIProtoBytes(field uint64, value []byte) []byte {
	b := binary.AppendUvarint(nil, field<<3|2)
	b = binary.AppendUvarint(b, uint64(len(value)))
	return append(b, value...)
}

// Native fixtures exercise discovery, loading, pricing, aggregation and public JSON.
func TestNativeSourceCLIMatrix(t *testing.T) {
	for _, name := range []string{"opencode", "codebuff", "hermes", "goose", "kilo", "kimi", "copilot", "antigravity", "zcode"} {
		t.Run(name, func(t *testing.T) {
			isolateSourceHomes(t)
			dir := t.TempDir()
			want := 15.0
			switch name {
			case "opencode", "kilo":
				db := sourceCLIDB(t, filepath.Join(dir, name+".db"), `CREATE TABLE message(id TEXT,session_id TEXT,data TEXT);`)
				_, err := db.Exec(`INSERT INTO message VALUES('m','s','{"role":"assistant","modelID":"gpt-5","providerID":"openai","time":{"created":1778000000000},"tokens":{"input":10,"output":5}}')`)
				if err != nil {
					t.Fatal(err)
				}
			case "hermes":
				sourceCLIDB(t, filepath.Join(dir, "state.db"), `CREATE TABLE sessions(id TEXT,model TEXT,billing_provider TEXT,started_at REAL,message_count INTEGER,input_tokens INTEGER,output_tokens INTEGER,cache_read_tokens INTEGER,cache_write_tokens INTEGER,reasoning_tokens INTEGER,estimated_cost_usd REAL,actual_cost_usd REAL); INSERT INTO sessions VALUES('s','gpt-5','openai',1778000000,4,10,5,0,0,0,NULL,NULL);`)
			case "goose":
				sourceCLIDB(t, filepath.Join(dir, "sessions.db"), `CREATE TABLE sessions(id TEXT,model_config_json TEXT,provider_name TEXT,created_at TEXT,total_tokens INTEGER,input_tokens INTEGER,output_tokens INTEGER,accumulated_total_tokens INTEGER,accumulated_input_tokens INTEGER,accumulated_output_tokens INTEGER); INSERT INTO sessions VALUES('s','{"model_name":"gpt-5"}','openai','2026-05-05T12:00:00Z',15,10,5,15,10,5);`)
			case "zcode":
				sourceCLIDB(t, filepath.Join(dir, "cli/db/db.sqlite"), `CREATE TABLE session(id TEXT,directory TEXT); CREATE TABLE model_usage(id TEXT,session_id TEXT,started_at INTEGER,model_id TEXT,status TEXT,input_tokens INTEGER,output_tokens INTEGER); INSERT INTO session VALUES('s','/workspace/zcode'); INSERT INTO model_usage VALUES('m','s',1778000000000,'glm-5','completed',10,5);`)
			case "kimi":
				sourceCLIWrite(t, filepath.Join(dir, "sessions/work/s/agents/main/wire.jsonl"), `{"type":"usage.record","usageScope":"turn","model":"kimi-code/kimi-k2.6","time":1778000000000,"usage":{"inputOther":10,"output":5}}`)
			case "codebuff":
				sourceCLIWrite(t, filepath.Join(dir, "projects/work/chats/2026-05-05T12-00-00.000Z/chat-messages.json"), `[{"id":"m","variant":"ai","credits":2.5,"metadata":{"model":"gpt-5","usage":{"inputTokens":10,"outputTokens":5}}}]`)
			case "copilot":
				sourceCLIWrite(t, filepath.Join(dir, "session-state/s/events.jsonl"), `{"type":"session.shutdown","id":"m","timestamp":"2026-05-05T12:00:00Z","data":{"modelMetrics":{"gpt-5":{"usage":{"inputTokens":10,"outputTokens":5},"requests":{"count":4}}}}}`)
			case "antigravity":
				db := sourceCLIDB(t, filepath.Join(dir, "conversations/s.db"), `CREATE TABLE gen_metadata(idx INTEGER PRIMARY KEY,data BLOB);`)
				usage := append(sourceCLIProtoInt(2, 10), sourceCLIProtoInt(3, 5)...)
				chat := sourceCLIProtoBytes(19, []byte("gemini-2.5-pro"))
				chat = append(chat, sourceCLIProtoBytes(4, usage)...)
				chat = append(chat, sourceCLIProtoBytes(9, sourceCLIProtoBytes(4, sourceCLIProtoInt(1, 1778000000)))...)
				if _, err := db.Exec("INSERT INTO gen_metadata VALUES(1,?)", sourceCLIProtoBytes(1, chat)); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{name, "daily", "--offline", "--json", "--data-path", dir, "--timezone", "UTC"}
			payload := sourceCLIJSON(t, args...)
			daily := payload["daily"].([]any)
			totals := payload["totals"].(map[string]any)
			if len(daily) != 1 || totals["totalTokens"] != want {
				t.Fatalf("%s %#v", name, payload)
			}
			if name == "codebuff" && totals["credits"] != 2.5 {
				t.Fatal(payload)
			}
			if name == "copilot" || name == "hermes" {
				if daily[0].(map[string]any)["messageCount"] != 4.0 {
					t.Fatal(payload)
				}
			}
			payload = sourceCLIJSON(t, append(args, "--no-cost")...)
			data, _ := json.Marshal(payload)
			if strings.Contains(strings.ToLower(string(data)), "cost") {
				t.Fatalf("cost leaked: %s", data)
			}
			args[1] = "session"
			payload = sourceCLIJSON(t, args...)
			if sessions, ok := payload["sessions"].([]any); !ok || len(sessions) != 1 {
				t.Fatalf("focused session key: %#v", payload)
			}
		})
	}
}
func TestOpenCodeAggregateOnlySessionCLI(t *testing.T) {
	isolateSourceHomes(t)
	dir := t.TempDir()
	sourceCLIDB(t, filepath.Join(dir, "opencode.db"), `CREATE TABLE session_v2(id TEXT,time_created INTEGER,cost REAL,tokens_input INTEGER,tokens_output INTEGER,tokens_cache_read INTEGER,tokens_cache_write INTEGER,tokens_reasoning INTEGER,model TEXT); INSERT INTO session_v2 VALUES('s',1778000000000,1.25,100,25,0,0,0,'gpt-5');`)
	args := []string{"opencode", "daily", "--json", "--offline", "--data-path", dir, "--timezone", "UTC"}
	daily := sourceCLIJSON(t, args...)
	if len(daily["daily"].([]any)) != 0 {
		t.Fatal(daily)
	}
	args[1] = "session"
	session := sourceCLIJSON(t, args...)
	if session["totals"].(map[string]any)["totalTokens"] != 125.0 {
		t.Fatal(session)
	}
	bounded := sourceCLIJSON(t, append(args, "--since", "2026-01-01")...)
	if len(bounded["sessions"].([]any)) != 0 {
		t.Fatal(bounded)
	}
}
func TestSourceCLIErrorsKeepStdoutEmpty(t *testing.T) {
	isolateSourceHomes(t)
	dir := t.TempDir()
	sourceCLIWrite(t, filepath.Join(dir, "conversations/broken.db"), "not sqlite")
	stdout, _, err := sourceCLIRun("antigravity", "daily", "--offline", "--json", "--data-path", dir)
	if err == nil || stdout != "" {
		t.Fatalf("error=%v stdout=%q", err, stdout)
	}
}

// Upstream loads every source of a unified report and fails it on the first
// source that fails, so other sources' usage is not reported without it.
func TestUnifiedReportsFailOnASourceThatFails(t *testing.T) {
	home := isolateSourceHomes(t)
	t.Setenv("CLAUDE_CONFIG_DIR", fixtureReport(t))
	antigravity := filepath.Join(home, "antigravity")
	t.Setenv("ANTIGRAVITY_DATA_DIR", antigravity)
	sourceCLIDB(t, filepath.Join(antigravity, "conversations/acp.db"), `CREATE TABLE other(x INTEGER); INSERT INTO other VALUES(1);`)
	for _, kind := range []string{"daily", "weekly", "monthly", "session"} {
		stdout, _, err := sourceCLIRun(kind, "--offline", "--json")
		if err == nil || !strings.Contains(err.Error(), "gen_metadata") || stdout != "" {
			t.Fatalf("%s: error=%v stdout=%q", kind, err, stdout)
		}
	}
}

// Like upstream, a source file or directory that cannot be read or parsed is
// skipped, so it fails neither the unified report nor its source's own.
func TestReportsSkipSourceFilesThatCannotBeRead(t *testing.T) {
	home := isolateSourceHomes(t)
	claude := fixtureReport(t)
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	for _, env := range []string{"AMP_DATA_DIR", "GEMINI_DATA_DIR", "CODEX_HOME", "KIMI_DATA_DIR", "OPENCODE_DATA_DIR"} {
		t.Setenv(env, filepath.Join(home, env))
	}
	sourceCLIWrite(t, filepath.Join(home, "AMP_DATA_DIR/threads/T-1.json"), `{"id":"T-1","messages":[`)
	sourceCLIWrite(t, filepath.Join(home, "GEMINI_DATA_DIR/x/chats/session-2026-10-20T10-00-abc.json"), `{"sessionId":`)
	lock := func(path string, dir bool) {
		if dir {
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
		} else {
			sourceCLIWrite(t, path, "{}")
		}
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(path, 0700) })
		if _, err := os.Open(path); err == nil {
			t.Skip("file permissions do not keep this user from reading, as for root or on Windows")
		}
	}
	lock(filepath.Join(claude, "projects/locked"), true)
	lock(filepath.Join(home, "GEMINI_DATA_DIR/locked"), true)
	lock(filepath.Join(home, "CODEX_HOME/sessions/2026/10/20/rollout-2026-10-20T10-00-00-0199a213-81c0-7800-8aa1-bbab2a035a53.jsonl"), false)
	lock(filepath.Join(home, "KIMI_DATA_DIR/sessions/a/b/wire.jsonl"), false)
	lock(filepath.Join(home, "OPENCODE_DATA_DIR/storage/message/locked"), true)
	for _, report := range [][]string{{"daily"}, {"session"}, {"claude", "daily"}, {"amp", "daily"}, {"gemini", "daily"}, {"codex", "daily"}, {"kimi", "daily"}, {"opencode", "daily"}} {
		payload := sourceCLIJSON(t, append(report, "--offline", "--json")...)
		want := 0.0
		if len(report) == 1 || report[0] == "claude" {
			want = 171
		}
		if tokens := payload["totals"].(map[string]any)["totalTokens"]; tokens != want {
			t.Errorf("%v: totalTokens %v, want %v", report, tokens, want)
		}
	}
	// So is a Claude transcript that cannot be read when no other one has
	// usage.
	only := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", only)
	lock(filepath.Join(only, "projects/p/s.jsonl"), false)
	for _, report := range [][]string{{"daily"}, {"claude", "daily"}, {"blocks"}} {
		if _, stderr, err := sourceCLIRun(append(report, "--offline", "--json")...); err != nil {
			t.Errorf("%v: %v %s", report, err, stderr)
		}
	}
}

func TestNativeSourceExtraReasoningIsBilled(t *testing.T) {
	isolateSourceHomes(t)
	root := t.TempDir()
	sourceCLIDB(t, filepath.Join(root, "state.db"), `CREATE TABLE sessions(id TEXT,model TEXT,started_at REAL,input_tokens INTEGER,output_tokens INTEGER,reasoning_tokens INTEGER); INSERT INTO sessions VALUES('s','audit-model',1778000000,0,5,100);`)
	configPath := filepath.Join(root, "config.json")
	sourceCLIWrite(t, configPath, `{"defaults":{"pricingOverrides":{"audit-model":{"inputCostPerToken":0,"outputCostPerToken":1}}}}`)
	payload := sourceCLIJSON(t, "hermes", "daily", "--data-path", root, "--config", configPath, "--json", "--offline")
	totals := payload["totals"].(map[string]any)
	if totals["totalTokens"] != 105.0 || totals["totalCost"] != 105.0 {
		t.Fatalf("extra reasoning underbilled: %#v", totals)
	}
}

// Copilot session credits missing from modelMetrics are stored-cost rows of
// the unknown model: --mode calculate reports them at $0, and the unified
// report keeps them although they have no tokens.
func TestCopilotSessionCreditsCLI(t *testing.T) {
	home := isolateSourceHomes(t)
	dir := filepath.Join(home, "copilot")
	sourceCLIWrite(t, filepath.Join(dir, "session-state/session-credits/events.jsonl"), strings.Join([]string{
		`{"type":"session.usage_checkpoint","id":"checkpoint-c-1","timestamp":"2026-02-03T01:00:00.000Z","data":{"totalNanoAiu":5000000000}}`,
		`{"type":"session.shutdown","id":"shutdown-c","timestamp":"2026-02-03T02:00:00.000Z","data":{"totalNanoAiu":9000000000,"modelMetrics":{"gpt-5.4":{"usage":{"inputTokens":40,"outputTokens":10},"requests":{"count":2},"totalNanoAiu":3000000000}}}}`,
		`{"type":"session.usage_checkpoint","id":"checkpoint-c-2","timestamp":"2026-02-04T01:00:00.000Z","data":{"totalNanoAiu":11000000000}}`,
	}, "\n"))
	t.Setenv("COPILOT_HOME", dir)
	// "date tokens messages model=cost..." per daily row.
	rows := func(args ...string) []string {
		payload := sourceCLIJSON(t, append(args, "--offline", "--json", "--timezone", "UTC")...)
		if unpriced := payload["totals"].(map[string]any)["unpricedModels"]; unpriced != nil {
			t.Fatalf("unpriced models %v", unpriced)
		}
		var out []string
		for _, row := range payload["daily"].([]any) {
			r := row.(map[string]any)
			line := fmt.Sprintf("%s %v %v", firstNonNil(r["date"], r["period"]), r["totalTokens"], firstNonNil(r["messageCount"], r["requestCount"]))
			for _, model := range r["modelBreakdowns"].([]any) {
				m := model.(map[string]any)
				line += fmt.Sprintf(" %s=%.5f", m["modelName"], m["cost"])
				if stored, ok := m["storedCost"]; ok {
					line += fmt.Sprintf("/%.5f", stored)
				}
			}
			out = append(out, line)
		}
		return out
	}
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"copilot", "daily"}, []string{"2026-02-03 50 2 unknown=0.06000/0.06000 gpt-5.4=0.00025", "2026-02-04 0 <nil> unknown=0.02000/0.02000"}},
		{[]string{"copilot", "daily", "--mode", "calculate"}, []string{"2026-02-03 50 2 gpt-5.4=0.00025 unknown=0.00000", "2026-02-04 0 <nil> unknown=0.00000"}},
		{[]string{"daily"}, []string{"2026-02-03 50 2 unknown=0.06000/0.06000 gpt-5.4=0.00025", "2026-02-04 0 0 unknown=0.02000/0.02000"}},
	} {
		if got := rows(tc.args...); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%v: got %q, want %q", tc.args, got, tc.want)
		}
	}
}

func firstNonNil(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

// Upstream's OpenCode loader has no price list in display mode, so a
// message shows the cost it recorded, or $0 when it recorded none.
func TestOpenCodeDisplayModeShowsRecordedCostsOnly(t *testing.T) {
	isolateSourceHomes(t)
	dir := t.TempDir()
	t.Setenv("OPENCODE_DATA_DIR", dir)
	db := sourceCLIDB(t, filepath.Join(dir, "opencode.db"), `CREATE TABLE message(id TEXT,session_id TEXT,data TEXT);`)
	for _, row := range []string{
		`('m1','s','{"role":"assistant","modelID":"gpt-5","providerID":"openai","time":{"created":1778000000000},"tokens":{"input":1000,"output":100},"cost":0.5}')`,
		`('m2','s','{"role":"assistant","modelID":"gpt-5","providerID":"openai","time":{"created":1778000001000},"tokens":{"input":1000,"output":100},"cost":0}')`,
		`('m3','s','{"role":"assistant","modelID":"unknown-opencode-model","providerID":"openai","time":{"created":1778000002000},"tokens":{"input":1000,"output":100}}')`,
	} {
		if _, err := db.Exec(`INSERT INTO message VALUES` + row); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"auto", "display"} {
		want := 0.50225
		if mode == "display" {
			want = 0.5
		}
		for _, args := range [][]string{{"opencode", "daily"}, {"opencode", "session"}, {"daily"}} {
			out, _, err := sourceCLIRun(append(args, "--mode", mode, "--json", "--offline", "--timezone", "UTC")...)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(out), &payload); err != nil {
				t.Fatal(err)
			}
			if cost, _ := payload["totals"].(map[string]any)["totalCost"].(float64); math.Abs(cost-want) > 1e-12 {
				t.Errorf("%v --mode %s: cost %v, want %v", args, mode, cost, want)
			}
			if flagged := strings.Contains(out, "missingPricing"); flagged != (mode == "auto") {
				t.Errorf("%v --mode %s: missing pricing flagged = %v\n%s", args, mode, flagged, out)
			}
		}
	}
}
