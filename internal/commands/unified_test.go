package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func isolateSourceHomes(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "DROID_SESSIONS_DIR", "AMP_DATA_DIR", "PI_AGENT_DIR", "OPENCLAW_DIR", "GEMINI_DATA_DIR", "GROK_HOME", "QWEN_DATA_DIR", "OPENCODE_DATA_DIR", "CODEBUFF_DATA_DIR", "HERMES_HOME", "KILO_DATA_DIR", "KIMI_DATA_DIR", "ANTIGRAVITY_DATA_DIR", "ZCODE_HOME", "GOOSE_PATH_ROOT", "COPILOT_HOME", "COPILOT_OTEL_FILE_EXPORTER_PATH", "XDG_DATA_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(key, filepath.Join(home, "absent"))
	}
	return home
}

func TestUnifiedDefaultSectionsAndAgentBreakdown(t *testing.T) {
	isolateSourceHomes(t)
	path := fixtureReport(t)
	t.Setenv("CLAUDE_CONFIG_DIR", path)
	out, err := executeReport("--offline", "--json", "--by-agent", "--sections", "monthly,session", "--timezone", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("not JSON: %v %s", err, out)
	}
	if len(payload["daily"].([]any)) != 2 || len(payload["monthly"].([]any)) != 1 || len(payload["session"].([]any)) != 3 {
		t.Fatal(out)
	}
	if payload["totals"].(map[string]any)["totalCost"] != float64(9) {
		t.Fatal(out)
	}
	if len(payload["daily"].([]any)[0].(map[string]any)["agents"].([]any)) != 1 {
		t.Fatal(out)
	}
}

func TestNamedPiStoreOverlapRejected(t *testing.T) {
	home := isolateSourceHomes(t)
	pi := filepath.Join(home, "pi")
	t.Setenv("PI_AGENT_DIR", pi)
	cfg := filepath.Join(home, "config.json")
	data, _ := json.Marshal(map[string]any{"pi": map[string]any{"stores": []map[string]string{{"name": "archive", "path": filepath.Join(pi, "nested")}}}})
	if err := os.WriteFile(cfg, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := executeReport("daily", "--offline", "--json", "--config", cfg); err == nil {
		t.Fatal("overlapping pi store accepted")
	}
}

func TestSystemUserHomesDeduplicated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("passwd records use Unix paths")
	}
	home := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(home, alias); err != nil {
		t.Skip(err)
	}
	users := parseSystemUsers("alice:x:123:123::" + home + ":/bin/sh\nbob:x:124:124::" + alias + ":/bin/sh\nmissing:x:9:9::/this-does-not-exist:/bin/sh")
	if len(users) != 1 || users[0].Home != home {
		t.Fatalf("bad users: %+v", users)
	}
}

func TestNamedPiStoreAddsUsageWithDistinctAttribution(t *testing.T) {
	home := isolateSourceHomes(t)
	primary := filepath.Join(home, "primary")
	archive := filepath.Join(home, "archive")
	t.Setenv("PI_AGENT_DIR", primary)
	fixture := `{"type":"session","id":"same","timestamp":"2026-08-01T00:00:00Z"}
{"type":"message","id":"same","timestamp":"2026-08-01T00:00:01Z","message":{"role":"assistant","model":"test-model","usage":{"input":10,"output":5,"cost":{"total":1}}}}`
	sourceCLIWrite(t, filepath.Join(primary, "work/s.jsonl"), fixture)
	sourceCLIWrite(t, filepath.Join(archive, "work/s.jsonl"), fixture)
	cfg := filepath.Join(home, "config.json")
	data, _ := json.Marshal(map[string]any{"pi": map[string]any{"stores": []map[string]string{{"name": "archive", "path": archive}}}})
	sourceCLIWrite(t, cfg, string(data))
	p := sourceCLIJSON(t, "daily", "--json", "--offline", "--config", cfg, "--by-agent", "--sections", "session")
	if p["totals"].(map[string]any)["totalTokens"] != 30.0 || len(p["daily"].([]any)) != 1 {
		t.Fatalf("bad totals %#v", p)
	}
	rows := p["daily"].([]any)
	agents := rows[0].(map[string]any)["agents"].([]any)
	if len(agents) != 2 || agents[0].(map[string]any)["agent"] != "archive" || agents[1].(map[string]any)["agent"] != "pi" {
		t.Fatalf("bad attribution %#v", agents)
	}
	if len(p["session"].([]any)) != 2 {
		t.Fatalf("stores collapsed session IDs %#v", p)
	}
}
