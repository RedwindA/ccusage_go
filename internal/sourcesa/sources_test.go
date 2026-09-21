package sourcesa

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RedwindA/ccusage_go/internal/types"
)

// Fixtures mirror the native Rust adapters' regression cases. They deliberately
// exercise schemas that cannot be handled by a generic usage-field reader.
func fixture(t *testing.T, root, path, body string) string {
	t.Helper()
	p := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}
func loadTest(t *testing.T, name, root string) []types.UsageEntry {
	t.Helper()
	got, err := Load(context.Background(), name, []string{root}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func requireTokens(t *testing.T, e types.UsageEntry, input, output, read, write, totalCount int) {
	t.Helper()
	if e.InputTokens != input || e.OutputTokens != output || e.CacheReadInputTokens != read || e.CacheCreationInputTokens != write || e.TotalTokens != totalCount {
		t.Fatalf("unexpected usage: %+v", e)
	}
}

func TestAmpLedgerTakesPrecedenceAndJoinsCache(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "threads/a.json", `{"id":"T-a","messages":[{"role":"assistant","messageId":42,"usage":{"inputTokens":999,"cacheCreationInputTokens":30,"cacheReadInputTokens":40}}],"usageLedger":{"events":[{"id":"event-a","timestamp":"2026-01-02T00:00:00Z","model":"gpt-5","toMessageId":42,"tokens":{"input":10,"output":20},"credits":0.25}]}}`)
	entries := loadTest(t, "amp", root)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	requireTokens(t, entries[0], 10, 20, 40, 30, 100)
	if entries[0].Credits != .25 || entries[0].HasCost {
		t.Fatal(entries[0])
	}
}
func TestAmpMalformedElementsAndTotalFallback(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "threads/a.json", `{"id":"T-a","usageLedger":"bad","messages":["bad",{"role":"assistant","usage":{"model":"gpt-5","timestamp":"2026-01-02T00:00:00Z","totalTokens":345}}]}`)
	entries := loadTest(t, "amp", root)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	requireTokens(t, entries[0], 0, 345, 0, 0, 345)
}
func TestDroidSettingsAndSidecar(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	fixture(t, root, ".factory/settings.json", `{"customModels":[{"id":"custom:mine","model":"Claude Sonnet 4.5 [custom]","provider":"anthropic"}]}`)
	fixture(t, root, "sessions/encoded/s.settings.json", `{"model":"custom:mine","providerLockTimestamp":"2026-01-02T00:00:00Z","tokenUsage":{"inputTokens":11,"outputTokens":12,"cacheCreationTokens":13,"cacheReadTokens":14,"thinkingTokens":15}}`)
	fixture(t, root, "sessions/encoded/s.jsonl", `{"type":"session_start","payload":{"cwd":"/work/project"}}`)
	entries := loadTest(t, "droid", filepath.Join(root, "sessions"))
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	e := entries[0]
	requireTokens(t, e, 11, 12, 14, 13, 65)
	if e.Model != "claude-sonnet-4-5" || e.ProjectPath != "/work/project" || e.ReasoningTokens != 15 {
		t.Fatal(e)
	}
}
func TestGeminiDistinguishesDirectAndStatsCacheSemantics(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "chat.json", `{"sessionId":"a","messages":[{"type":"gemini","id":"m1","model":"gemini-2.5-pro","timestamp":"2026-01-02T00:00:00Z","tokens":{"input":100,"output":20,"cached":40,"thoughts":10,"total":130}},{"type":"gemini","id":"m2","model":"gemini-2.5-pro","timestamp":"2026-01-02T00:01:00Z","tokens":{"input":100,"output":20,"cached":40,"thoughts":10,"total":170}}]}`)
	fixture(t, root, "stats.json", `{"sessionId":"b","timestamp":"2026-01-02T00:02:00Z","stats":{"models":{"gemini-2.5-flash":{"tokens":{"prompt":100,"candidates":20,"cached":40,"thoughts":10}}}}}`)
	entries := loadTest(t, "gemini", root)
	if len(entries) != 3 {
		t.Fatal(entries)
	}
	requireTokens(t, entries[0], 60, 20, 40, 0, 130)
	requireTokens(t, entries[1], 100, 20, 40, 0, 170)
	requireTokens(t, entries[2], 60, 20, 40, 0, 130)
}
func TestGeminiJSONLReplacesPartialMessage(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "chat.jsonl", `{"sessionId":"a","model":"gemini-2.5-pro"}
{"type":"gemini","id":"same","timestamp":"2026-01-02T00:00:00Z","tokens":{"input":10,"output":2}}
malformed
{"type":"gemini","id":"same","timestamp":"2026-01-02T00:00:00Z","tokens":{"input":10,"output":7}}`)
	entries := loadTest(t, "gemini", root)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	requireTokens(t, entries[0], 10, 7, 0, 0, 17)
}
func TestQwenNativeChatLayoutAndIndependentCache(t *testing.T) {
	root := t.TempDir()
	record := `{"type":"assistant","sessionId":"s","model":"qwen3-coder","timestamp":"2026-01-02T00:00:00Z","usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"cachedContentTokenCount":40,"thoughtsTokenCount":10}}`
	fixture(t, root, "projects/project-a/chats/session.jsonl", record+"\n"+record)
	fixture(t, root, "projects/project-a/unrelated.jsonl", record)
	entries := loadTest(t, "qwen", root)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	requireTokens(t, entries[0], 100, 20, 40, 0, 170)
	if entries[0].ProjectPath != "project-a" {
		t.Fatal(entries[0])
	}
}
func TestGrokModelUsageTicksAndReasoningSubset(t *testing.T) {
	root := t.TempDir()
	record := `{"timestamp":1750000000,"params":{"sessionId":"s","update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"grok-4.5-build":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":40,"cacheCreationTokens":25,"reasoningTokens":10,"costUsdTicks":185192000},"other":{"inputTokens":5,"outputTokens":1}}}},"_meta":{"eventId":"event","agentTimestampMs":1785328986355}}}`
	fixture(t, root, "sessions/%2Fwork%2Fproject/s/updates.jsonl", record+"\n"+record)
	fixture(t, root, "logs/unified.jsonl", record)
	entries := loadTest(t, "grok", root)
	if len(entries) != 2 {
		t.Fatal(entries)
	}
	e := entries[0]
	requireTokens(t, e, 35, 20, 40, 25, 120)
	if e.Cost != .0185192 || !e.HasCost || e.ReasoningTokens != 10 || e.ProjectPath != "/work/project" || e.Timestamp.UnixMilli() != 1785328986355 {
		t.Fatal(e)
	}
}
func TestOpenClawArchivesTrackModelsAndZeroCost(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "agents/main/sessions/s.jsonl.reset.2026-03-20", `{"type":"custom","customType":"model-snapshot","data":{"modelId":"gpt-5.2","provider":"openai-codex"}}
{"type":"message","message":{"role":"assistant","usage":{"input":1660,"output":55,"cacheRead":108928,"cost":{"total":0}},"timestamp":1769753935279}}`)
	entries := loadTest(t, "openclaw", root)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	e := entries[0]
	requireTokens(t, e, 1660, 55, 108928, 0, 110643)
	if e.Model != "gpt-5.2" || !e.HasCost || e.Cost != 0 || e.SessionID != "s" {
		t.Fatal(e)
	}
}
func TestOpenClawSQLiteWinsMigrationAndResetsModelState(t *testing.T) {
	root := t.TempDir()
	p := fixture(t, root, "agents/main/agent/openclaw-agent.sqlite", "")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE transcript_events (session_id TEXT,seq INTEGER,event_json TEXT,created_at INTEGER)`)
	if err != nil {
		t.Fatal(err)
	}
	records := []struct {
		session string
		seq     int
		raw     string
	}{{"s", 0, `{"type":"model_change","modelId":"gpt-5"}`}, {"s", 1, `{"id":"event","type":"message","message":{"role":"assistant","usage":{"input":10,"output":2,"cost":{"total":0.2}},"timestamp":1769753935279}}`}, {"t", 0, `{"type":"message","message":{"role":"assistant","usage":{"input":5}},"timestamp":1769753935279}`}}
	for _, r := range records {
		if _, err = db.Exec("INSERT INTO transcript_events VALUES (?,?,?,?)", r.session, r.seq, r.raw, 1769753935279); err != nil {
			t.Fatal(err)
		}
	}
	fixture(t, root, "agents/main/sessions/s.jsonl", `{"type":"message","message":{"role":"assistant","model":"gpt-5","usage":{"input":10,"output":2,"cost":{"total":0.1}},"timestamp":1769753935279}}`)
	entries := loadTest(t, "openclaw", root)
	if len(entries) != 2 {
		t.Fatal(entries)
	}
	for _, e := range entries {
		if e.SessionID == "s" && e.Cost != .2 {
			t.Fatal(e)
		}
		if e.SessionID == "t" && e.Model != "unknown" {
			t.Fatal(e)
		}
	}
}
func TestCodexCumulativeDeltasAndRecordedSpeed(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "sessions/s.jsonl", `{"type":"session_meta","timestamp":"2026-08-01T00:00:00Z","payload":{"id":"s","cwd":"/work"}}
{"type":"turn_context","payload":{"model":"gpt-5.4"}}
{"type":"event_msg","timestamp":"2026-08-01T00:00:01Z","payload":{"type":"thread_settings_applied","thread_settings":{"service_tier":"priority"}}}
{"type":"event_msg","timestamp":"2026-08-01T00:00:02Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"cache_write_input_tokens":25,"output_tokens":20,"reasoning_output_tokens":10},"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"cache_write_input_tokens":25,"output_tokens":20,"reasoning_output_tokens":10}}}}
{"type":"event_msg","timestamp":"2026-08-01T00:00:03Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":20},"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"cache_write_input_tokens":25,"output_tokens":20,"reasoning_output_tokens":10}}}}
{"type":"event_msg","timestamp":"2026-08-01T00:00:04Z","payload":{"type":"thread_settings_applied","thread_settings":{"service_tier":"flex"}}}
{"type":"event_msg","timestamp":"2026-08-01T00:00:05Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":110,"cached_input_tokens":40,"cache_write_input_tokens":25,"output_tokens":25,"reasoning_output_tokens":10}}}}`)
	entries := loadTest(t, "codex", root)
	if len(entries) != 2 {
		t.Fatal(entries)
	}
	requireTokens(t, entries[0], 35, 20, 40, 25, 120)
	requireTokens(t, entries[1], 10, 5, 0, 0, 15)
	if entries[0].Speed != "fast" || entries[1].Speed != "" || entries[0].ProjectPath != "/work" {
		t.Fatal(entries)
	}
}
func TestCodexHeadlessAliasesAndAutoReviewFallback(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "sessions/exec.jsonl", `{"result":{"model":"codex-auto-review","timestamp":"2026-08-01T00:00:00Z","usage":{"prompt_tokens":100,"cached_tokens":80,"cache_creation_input_tokens":100,"completion_tokens":20,"reasoning_tokens":10}}}`)
	entries := loadTest(t, "codex", root)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	requireTokens(t, entries[0], 0, 20, 80, 20, 120)
	if entries[0].Model != "gpt-5.6-luna" {
		t.Fatal(entries)
	}
}
func codexLine(t *testing.T, ts string, input int) string {
	t.Helper()
	b, err := json.Marshal(object{"type": "event_msg", "timestamp": ts, "payload": object{"type": "token_count", "info": object{"last_token_usage": object{"input_tokens": input, "output_tokens": 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestCodexForkSuppressesParentReplayAndArchiveCopy(t *testing.T) {
	root := t.TempDir()
	parent := `{"type":"session_meta","timestamp":"2026-08-01T00:00:00Z","payload":{"id":"parent"}}` + "\n" + codexLine(t, "2026-08-01T00:00:01Z", 10)
	fixture(t, root, "sessions/parent.jsonl", parent)
	fixture(t, root, "archived_sessions/parent.jsonl", parent)
	child := `{"type":"session_meta","timestamp":"2026-08-01T00:01:00Z","payload":{"id":"child","forked_from_id":"parent"}}` + "\n" + codexLine(t, "2026-08-01T00:01:00Z", 10) + "\n" + codexLine(t, "2026-08-01T00:01:05Z", 20)
	fixture(t, root, "sessions/child.jsonl", child)
	entries := loadTest(t, "codex", root)
	if len(entries) != 2 {
		t.Fatal(entries)
	}
	if entries[1].InputTokens != 20 || !entries[1].IsSidechain {
		t.Fatal(entries)
	}
}
func piLine(id, parent, ts string, input int) string {
	r := object{"type": "message", "timestamp": ts, "message": object{"role": "assistant", "model": "gpt-5", "usage": object{"input": input, "output": 1}}}
	if id != "" {
		r["id"] = id
		if parent != "" {
			r["parentId"] = parent
		}
	}
	b, _ := json.Marshal(r)
	return string(b)
}
func TestPiReplayUsesActiveBranchAndSkipsDebugCopies(t *testing.T) {
	root := t.TempDir()
	header := `{"type":"session","timestamp":"2026-08-01T00:00:00Z"}`
	a := piLine("a", "", "2026-08-01T00:00:01Z", 10)
	b := piLine("b", "a", "2026-08-01T00:00:02Z", 20)
	c := piLine("c", "a", "2026-08-01T00:00:03Z", 30)
	fixture(t, root, "sessions/project/parent.jsonl", strings.Join([]string{header, a, b, c}, "\n"))
	childHeader := `{"type":"session","timestamp":"2026-08-01T00:01:00Z","parentSession":"parent.jsonl"}`
	fixture(t, root, "sessions/project/child.jsonl", strings.Join([]string{childHeader, a, c, piLine("d", "c", "2026-08-01T00:01:01Z", 40)}, "\n"))
	fixture(t, root, "sessions/project/subagent-artifacts/copy.jsonl", a)
	entries := loadTest(t, "pi", filepath.Join(root, "sessions"))
	if len(entries) != 4 {
		t.Fatal(entries)
	}
	sum := 0
	for _, e := range entries {
		sum += e.InputTokens
	}
	if sum != 100 {
		t.Fatal(entries)
	}
}
func TestPiInvalidLineagePreservesUsage(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "sessions/p/a.jsonl", `{"type":"session","timestamp":"2026-08-01T00:01:00Z","parentSession":"a.jsonl"}`+"\n"+piLine("a", "", "2026-08-01T00:00:01Z", 10))
	entries := loadTest(t, "pi", filepath.Join(root, "sessions"))
	if len(entries) != 1 {
		t.Fatal(entries)
	}
}
func TestDefaultsContextAndTimezone(t *testing.T) {
	t.Setenv("GROK_HOME", " /custom,grok ")
	if p := DefaultPaths("grok", "/home"); len(p) != 1 || p[0] != "/custom,grok" {
		t.Fatal(p)
	}
	t.Setenv("PI_AGENT_DIR", " ")
	if p := DefaultPaths("pi", "/home"); len(p) != 1 || p[0] != filepath.Join("/home", ".pi", "agent", "sessions") {
		t.Fatal(p)
	}
	t.Setenv("AMP_DATA_DIR", " /first, /second ")
	if p := DefaultPathsForHome("amp", "/other"); p[0] != filepath.Join("/other", ".local", "share", "amp") {
		t.Fatal(p)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Load(ctx, "amp", []string{t.TempDir()}, nil)
	if err != context.Canceled {
		t.Fatalf("cancel error: %v", err)
	}
	root := t.TempDir()
	fixture(t, root, "threads/a.json", `{"id":"a","messages":[{"role":"assistant","usage":{"timestamp":"2026-01-02T00:00:00Z","model":"gpt-5","inputTokens":1}}]}`)
	entries, err := Load(context.Background(), "amp", []string{root}, time.FixedZone("west", -7*3600))
	if err != nil || len(entries) != 1 || entries[0].DateKey != "2026-01-01" {
		t.Fatal(entries, err)
	}
}

func TestPiCalculateModeReplayIgnoresRecordedCost(t *testing.T) {
	root := t.TempDir()
	parentMessage := `{"type":"message","timestamp":"2026-08-01T00:00:01Z","message":{"role":"assistant","model":"gpt-5","usage":{"input":10,"output":1,"cost":{"total":0.1}}}}`
	childMessage := strings.Replace(parentMessage, `0.1`, `0.2`, 1)
	fixture(t, root, "sessions/p/parent.jsonl", `{"type":"session","timestamp":"2026-08-01T00:00:00Z"}`+"\n"+parentMessage)
	fixture(t, root, "sessions/p/child.jsonl", `{"type":"session","timestamp":"2026-08-01T00:01:00Z","parentSession":"parent.jsonl"}`+"\n"+childMessage+"\n"+piLine("", "", "2026-08-01T00:01:01Z", 20))
	for _, mode := range []string{"auto", "display", "calculate"} {
		entries, err := LoadWithMode(context.Background(), "pi", []string{filepath.Join(root, "sessions")}, time.UTC, mode)
		if err != nil {
			t.Fatal(err)
		}
		want := 3
		if mode == "calculate" {
			want = 2
		}
		if len(entries) != want {
			t.Fatalf("%s: %+v", mode, entries)
		}
	}
}
func TestPiProjectScopedDedup(t *testing.T) {
	root := t.TempDir()
	r := piLine("", "", "2026-08-01T00:00:01Z", 10)
	fixture(t, root, "sessions/project-a/same.jsonl", r)
	fixture(t, root, "sessions/project-b/same.jsonl", r)
	entries := loadTest(t, "pi", filepath.Join(root, "sessions"))
	if len(entries) != 2 {
		t.Fatal(entries)
	}
}

func TestDamagedFilesReturnErrorsAlongsideUsableEntries(t *testing.T) {
	t.Run("JSON", func(t *testing.T) {
		root := t.TempDir()
		fixture(t, root, "threads/good.json", `{"id":"s","messages":[{"role":"assistant","usage":{"timestamp":"2026-01-02T00:00:00Z","model":"gpt-5","inputTokens":1}}]}`)
		bad := fixture(t, root, "threads/bad.json", `{"id":`)
		entries, err := Load(context.Background(), "amp", []string{root}, time.UTC)
		if len(entries) != 1 || err == nil || !strings.Contains(err.Error(), bad) {
			t.Fatalf("entries=%+v err=%v", entries, err)
		}
	})
	t.Run("SQLite", func(t *testing.T) {
		root := t.TempDir()
		bad := fixture(t, root, "agents/main/agent/openclaw-agent.sqlite", "this is not sqlite")
		fixture(t, root, "agents/main/sessions/good.jsonl", `{"type":"message","timestamp":"2026-01-02T00:00:00Z","message":{"role":"assistant","model":"gpt-5","usage":{"input":1}}}`)
		entries, err := Load(context.Background(), "openclaw", []string{root}, time.UTC)
		if len(entries) != 1 || err == nil || !strings.Contains(err.Error(), bad) {
			t.Fatalf("entries=%+v err=%v", entries, err)
		}
	})
	t.Run("JSONL recovery", func(t *testing.T) {
		root := t.TempDir()
		fixture(t, root, "sessions/p/s.jsonl", "{partial\n"+piLine("", "", "2026-01-02T00:00:00Z", 1)+"\n{partial")
		entries, err := Load(context.Background(), "pi", []string{filepath.Join(root, "sessions")}, time.UTC)
		if len(entries) != 1 || err != nil {
			t.Fatalf("entries=%+v err=%v", entries, err)
		}
	})
	t.Run("unreadable Pi file", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing.jsonl")
		_, err := loadPi(context.Background(), []string{missing}, time.UTC, "auto")
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatal(err)
		}
	})
	t.Run("unreadable Codex file", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing.jsonl")
		_, err := loadCodex(context.Background(), []string{missing}, time.UTC)
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatal(err)
		}
	})
}
