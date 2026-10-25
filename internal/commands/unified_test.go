package commands

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func isolateSourceHomes(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
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
	// The fixture's one transcript is one session, whatever sessionIds its
	// lines record.
	if len(payload["daily"].([]any)) != 2 || len(payload["monthly"].([]any)) != 1 || len(payload["session"].([]any)) != 1 {
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

// Pi records a cost of 0 for custom models; an override naming the store's
// prefixed label or the bare model must price the tokens in auto mode, while
// display mode keeps showing the recorded cost.
func TestPiPricingOverrideRecalculatesRecordedCostInAutoMode(t *testing.T) {
	record := `{"type":"message","timestamp":"2026-01-02T00:00:00.000Z","message":{"role":"assistant","model":"my-custom-model","usage":{"input":1000,"output":2000,"cost":{"total":0}}}}`
	for _, tc := range []struct {
		key     string
		pi, omp float64
	}{{"[pi] my-custom-model", 5, 0}, {"[omp] my-custom-model", 0, 5}, {"my-custom-model", 5, 5}} {
		home := isolateSourceHomes(t)
		primary, omp := filepath.Join(home, "primary"), filepath.Join(home, "omp")
		t.Setenv("PI_AGENT_DIR", primary)
		sourceCLIWrite(t, filepath.Join(primary, "project-a/agent_session-a.jsonl"), record)
		sourceCLIWrite(t, filepath.Join(omp, "project-a/agent_session-a.jsonl"), record)
		cfg := filepath.Join(home, "config.json")
		data, _ := json.Marshal(map[string]any{
			"pi":       map[string]any{"stores": []map[string]string{{"name": "omp", "path": omp}}},
			"defaults": map[string]any{"pricingOverrides": map[string]any{tc.key: map[string]float64{"inputCostPerToken": 0.001, "outputCostPerToken": 0.002}}},
		})
		sourceCLIWrite(t, cfg, string(data))
		for _, mode := range []string{"auto", "display"} {
			want := map[string]float64{"[pi] my-custom-model": tc.pi, "[omp] my-custom-model": tc.omp}
			if mode == "display" {
				want = map[string]float64{"[pi] my-custom-model": 0, "[omp] my-custom-model": 0}
			}
			p := sourceCLIJSON(t, "daily", "--json", "--offline", "--config", cfg, "--mode", mode)
			got := map[string]float64{}
			for _, b := range p["daily"].([]any)[0].(map[string]any)["modelBreakdowns"].([]any) {
				model := b.(map[string]any)
				if model["missingPricing"] != nil {
					t.Errorf("%s %s: %v reported missing pricing", tc.key, mode, model["modelName"])
				}
				got[model["modelName"].(string)] = model["cost"].(float64)
			}
			for name, cost := range want {
				if math.Abs(got[name]-cost) > 1e-9 {
					t.Errorf("%s %s: %s cost %v, want %v", tc.key, mode, name, got[name], cost)
				}
			}
		}
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
	canonicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Home != canonicalHome {
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

// A source directory that is a symlink, such as Claude's projects moved to
// another disk, is read like upstream's read_dir reads it, and its files keep
// the names they have under the link: sessions, projects and Qwen's
// projects/<project>/chats layout come from those names.
func TestSymlinkedSourceRootsAreRead(t *testing.T) {
	home := isolateSourceHomes(t)
	link := func(target, name string) {
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, name); err != nil {
			t.Skip(err)
		}
	}
	claude := filepath.Join(home, "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	sourceCLIWrite(t, filepath.Join(home, "claude-store/project-a/session-a.jsonl"), pruneUsageLine("2026-09-11T12:00:00.000Z", "session-a", "msg-1", "req-1", 1))
	sourceCLIWrite(t, filepath.Join(home, "claude-store/project-a/session-a/subagents/agent-x.jsonl"), pruneSidechainLine("2026-09-11T12:05:00.000Z", "session-a", "msg-2", "req-2", 2))
	link(filepath.Join(home, "claude-store"), filepath.Join(claude, "projects"))
	codex := filepath.Join(home, "codex")
	t.Setenv("CODEX_HOME", codex)
	sourceCLIWrite(t, filepath.Join(home, "codex-store/2026/09/11/rollout.jsonl"), sourceCodexRollout("2026-09-11T12:00:00Z", 100, 10))
	link(filepath.Join(home, "codex-store"), filepath.Join(codex, "sessions"))
	t.Setenv("PI_AGENT_DIR", filepath.Join(home, "pi"))
	sourceCLIWrite(t, filepath.Join(home, "pi-store/work/s.jsonl"), sourcePiMessage("2026-09-11T12:00:00Z", 10, 5))
	link(filepath.Join(home, "pi-store"), filepath.Join(home, "pi"))
	qwen := filepath.Join(home, "qwen")
	t.Setenv("QWEN_DATA_DIR", qwen)
	sourceCLIWrite(t, filepath.Join(home, "qwen-store/work/chats/s.jsonl"), `{"type":"assistant","sessionId":"s","model":"test-model-1","timestamp":"2026-09-11T12:00:00Z","usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"totalTokenCount":120}}`)
	link(filepath.Join(home, "qwen-store"), filepath.Join(qwen, "projects"))

	for _, tc := range []struct {
		args   []string
		tokens float64
	}{{[]string{"claude", "daily"}, 24}, {[]string{"codex", "daily"}, 110}, {[]string{"pi", "daily"}, 15}, {[]string{"qwen", "daily"}, 120}, {[]string{"daily"}, 269}} {
		totals := sourceCLIJSON(t, append(tc.args, "--json", "--offline", "--timezone", "UTC")...)["totals"].(map[string]any)
		if totals["totalTokens"] != tc.tokens {
			t.Errorf("%v: totals %v, want %v tokens", tc.args, totals, tc.tokens)
		}
	}
	sessions := sourceCLIJSON(t, "claude", "session", "--json", "--offline", "--timezone", "UTC")["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("claude sessions %v, want session-a with its subagent", sessions)
	}
	if s := sessions[0].(map[string]any); s["sessionId"] != "session-a" || s["projectPath"] != filepath.Join(claude, "projects", "project-a") {
		t.Errorf("claude session %v", s)
	}
}
