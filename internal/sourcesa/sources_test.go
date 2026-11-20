package sourcesa

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	t.Setenv("USERPROFILE", root)
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

// Like upstream's normalize_droid_provider, a providerLock is matched in any
// case and with hyphens for underscores when it names the model.
func TestDroidProviderLockNamesTheModelWithHyphens(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	for session, provider := range map[string]string{"a": "vertex-ai", "b": "X-AI", "c": "google-ai"} {
		fixture(t, root, "sessions/w/"+session+".settings.json", `{"cwd":"/w","providerLock":"`+provider+`","providerLockTimestamp":"2026-09-10T10:01:00.000Z","tokenUsage":{"inputTokens":1000,"outputTokens":100}}`)
	}
	models := map[string]string{}
	for _, e := range loadTest(t, "droid", filepath.Join(root, "sessions")) {
		models[e.SessionID] = e.Model
	}
	if want := map[string]string{"a": "gemini-unknown", "b": "grok-unknown", "c": "gemini-unknown"}; !reflect.DeepEqual(models, want) {
		t.Fatalf("models %v, want %v", models, want)
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

// Like upstream, totalTokens is a recorded total, or the tokens shown when
// they exceed it, so thoughts the total covers are priced as output but not
// counted again; without a recorded total they count.
func TestGeminiTotalTokensKeepTheRecordedTotal(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "chat.json", `{"sessionId":"a","messages":[{"type":"gemini","id":"m1","model":"gemini-2.5-pro","timestamp":"2026-01-02T00:00:00Z","tokens":{"input":1000,"output":100,"thoughts":50,"total":1100}},{"type":"gemini","id":"m2","model":"gemini-2.5-pro","timestamp":"2026-01-02T00:01:00Z","tokens":{"input":1000,"output":100,"thoughts":50,"total":900}},{"type":"gemini","id":"m3","model":"gemini-2.5-pro","timestamp":"2026-01-02T00:02:00Z","tokens":{"input":1000,"output":100,"thoughts":50}}]}`)
	entries := loadTest(t, "gemini", root)
	if len(entries) != 3 {
		t.Fatal(entries)
	}
	requireTokens(t, entries[0], 1000, 100, 0, 0, 1100)
	requireTokens(t, entries[1], 1000, 100, 0, 0, 1100)
	requireTokens(t, entries[2], 1000, 100, 0, 0, 1150)
	for _, e := range entries {
		if e.Raw["extra_output_tokens"] != 50 {
			t.Fatalf("thoughts priced: %v", e.Raw["extra_output_tokens"])
		}
	}
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

// As upstream, a record without tokens is no event, so a later zero-token
// record of a message keeps the usage recorded for it earlier.
func TestGeminiJSONLZeroRecordKeepsEarlierUsage(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "chat.jsonl", `{"sessionId":"a","model":"gemini-2.5-pro"}
{"type":"gemini","id":"same","timestamp":"2026-01-02T00:00:00Z","tokens":{"input":10,"output":2}}
{"type":"gemini","id":"same","timestamp":"2026-01-02T00:00:01Z","tokens":{"input":0,"output":0}}
{"type":"gemini","id":"later","timestamp":"2026-01-02T00:00:02Z","tokens":{"input":0,"output":0}}
{"type":"gemini","id":"later","timestamp":"2026-01-02T00:00:03Z","tokens":{"input":5,"output":1}}`)
	entries := loadTest(t, "gemini", root)
	if len(entries) != 2 {
		t.Fatal(entries)
	}
	requireTokens(t, entries[0], 10, 2, 0, 0, 12)
	requireTokens(t, entries[1], 5, 1, 0, 0, 6)
}

// Like upstream's deserialize_optional_u64_lossy, a Codex token count is an
// unsigned integer or a string holding one; any other value, such as
// 1000.0, 1e1 or -1, falls through to the next alias.
func TestCodexTokenCountsReadLikeUpstream(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "sessions/s.jsonl", `{"type":"turn_context","payload":{"model":"gpt-5"}}
{"timestamp":"2026-09-20T00:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":" 1000 ","cached_input_tokens":"+100","output_tokens":"50"}}}}
{"timestamp":"2026-09-20T00:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000.0,"prompt_tokens":7,"output_tokens":1e1,"completion_tokens":"3","reasoning_output_tokens":-1,"reasoning_tokens":"1.5"}}}}
{"timestamp":"2026-09-20T00:00:03Z","type":"response_item","data":{"usage":{"input_tokens":"20","output_tokens":2}}}`)
	entries := loadTest(t, "codex", root)
	if len(entries) != 3 {
		t.Fatal(entries)
	}
	requireTokens(t, entries[0], 900, 50, 100, 0, 1050)
	requireTokens(t, entries[1], 7, 3, 0, 0, 10)
	requireTokens(t, entries[2], 20, 2, 0, 0, 22)
	if entries[1].ReasoningTokens != 0 {
		t.Fatal(entries[1])
	}
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

// Upstream reads a null message timestamp as none, so the record's dates the
// message instead of the file's modification time.
func TestOpenClawNullMessageTimestampFallsBackToTheRecords(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "agents/main/sessions/s.jsonl", `{"type":"message","timestamp":"2026-09-17T13:43:59.762Z","message":{"role":"assistant","timestamp":null,"model":"claude-sonnet-4-5","usage":{"input":1000,"output":100}}}`)
	entries := loadTest(t, "openclaw", root)
	if len(entries) != 1 || !entries[0].Timestamp.Equal(time.Date(2026, 9, 17, 13, 43, 59, 762e6, time.UTC)) {
		t.Fatal(entries)
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
{"type":"event_msg","timestamp":"2026-08-01T00:00:04Z","payload":{"type":"thread_settings_applied","thread_settings":{"service_tier":"unknown"}}}
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
func TestCodexRecordsFlexTierAndKeepsExplicitCopyTiers(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	rollout := func(tier string, second int) string {
		settings := ""
		if tier != "" {
			settings = `{"type":"event_msg","timestamp":"2026-07-22T00:00:04Z","payload":{"type":"thread_settings_applied","thread_settings":{"service_tier":"` + tier + `"}}}` + "\n"
		}
		return settings + fmt.Sprintf(`{"type":"event_msg","timestamp":"2026-07-22T00:00:%02dZ","payload":{"type":"token_count","info":{"model":"gpt-5.4","last_token_usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}}`, second)
	}
	// Each session is copied into a second Codex home, which is read later.
	want := map[string]string{}
	for i, tc := range []struct{ session, first, second, want string }{
		{"flex", "flex", "", "flex"}, {"unmarked-then-flex", "", "flex", "flex"}, {"flex-then-fast", "flex", "priority", "fast"}, {"fast-then-flex", "priority", "flex", "fast"}, {"flex-then-standard", "flex", "default", "standard"},
	} {
		fixture(t, first, "sessions/"+tc.session+".jsonl", rollout(tc.first, 5+i))
		if tc.second != "" {
			fixture(t, second, "sessions/"+tc.session+".jsonl", rollout(tc.second, 5+i))
		}
		want[tc.session] = tc.want
	}
	entries, err := Load(context.Background(), "codex", []string{first, second}, time.UTC)
	if err != nil || len(entries) != len(want) {
		t.Fatal(entries, err)
	}
	for _, e := range entries {
		if e.Speed != want[e.SessionID] || e.InputTokens != 10 {
			t.Errorf("%s: speed %q, want %q", e.SessionID, e.Speed, want[e.SessionID])
		}
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
func TestCodexSessionIDIsPathUnderScannedDirectory(t *testing.T) {
	// A home without sessions/ is scanned itself, so rollouts sharing a name
	// stay apart; a sessions directory above the home is not the scanned one,
	// and a rollout given directly is found in its own directory.
	flat := t.TempDir()
	file := fixture(t, flat, "2026/10/14/run.jsonl", codexUsageLog("2026-10-14T00:00:00Z", 100))
	fixture(t, flat, "2026/10/15/run.jsonl", codexUsageLog("2026-10-15T00:00:00Z", 100))
	nested := filepath.Join(t.TempDir(), "sessions", "home")
	fixture(t, nested, "sessions/2026/10/14/run.jsonl", codexUsageLog("2026-10-14T00:00:00Z", 100))
	for root, want := range map[string]map[string]int{
		flat:   {"2026/10/14/run": 120, "2026/10/15/run": 120},
		nested: {"2026/10/14/run": 120},
		file:   {"run": 120},
	} {
		if _, bySession := sumTokens(t, root); !reflect.DeepEqual(bySession, want) {
			t.Errorf("%s: %v, want %v", root, bySession, want)
		}
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
func TestCodexRepeatedSnapshotDoesNotEndRewrittenBurst(t *testing.T) {
	root := t.TempDir()
	tokenCount := func(ts string, input, totalInput int) string {
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":%d,"output_tokens":1,"total_tokens":%d},"total_token_usage":{"input_tokens":%d,"output_tokens":1,"total_tokens":%d},"model":"gpt-5.5"}}}`, ts, input, input+1, totalInput, totalInput+1)
	}
	// The replay repeats a snapshot, which must not end the burst; the
	// child's own turn follows a real pause.
	fixture(t, root, "child.jsonl", `{"type":"session_meta","payload":{"id":"child","forked_from_id":"missing-parent"}}`+"\n"+tokenCount("2026-07-10T08:00:00.100Z", 100, 100)+"\n"+tokenCount("2026-07-10T08:00:00.200Z", 100, 100)+"\n"+tokenCount("2026-07-10T08:00:08.000Z", 50, 50))
	entries := loadTest(t, "codex", root)
	if len(entries) != 1 || entries[0].InputTokens != 50 {
		t.Fatal(entries)
	}
}

func TestCodexSpawnedSiblingsKeepOwnRequestsAtTheForkInstant(t *testing.T) {
	root := t.TempDir()
	const at = "2026-11-18T12:00:00Z"
	header := func(id string) string {
		return fmt.Sprintf(`{"type":"session_meta","timestamp":%q,"payload":{"id":%q,"source":{"subagent":{"thread_spawn":{"parent_thread_id":"missing-parent"}}}}}`, at, id)
	}
	record := func(id string) string {
		return fmt.Sprintf(`{"type":"token_usage_record","timestamp":%q,"payload":{"response_id":%q,"usage":{"input_tokens":10,"output_tokens":1}}}`, at, id)
	}
	count := codexLine(t, at, 10)
	for _, id := range []string{"a", "b"} {
		fixture(t, root, "sessions/"+id+".jsonl", strings.Join([]string{
			header(id), codexLine(t, at, 30), record(id + "-1"), count, record(id + "-2"), count,
		}, "\n"))
	}
	// Even when the unnamed copy is found first, the requests belong to a.
	fixture(t, root, "sessions/0-copy.jsonl", count+"\n"+count)
	fixture(t, root, "archived_sessions/a-copy.jsonl", strings.Join([]string{
		header("a-copy"), record("a-1"), count, record("a-2"), count,
	}, "\n"))
	want := map[string]int{"a": 22, "b": 22}
	if _, got := sumTokens(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens by session %v, want %v", got, want)
	}
}

func TestCodexMissingParentDoesNotHideLateFirstResponse(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "child.jsonl", `{"type":"session_meta","timestamp":"2026-11-18T12:00:00Z","payload":{"id":"child","forked_from_id":"missing"}}`+"\n"+
		codexLine(t, "2026-11-18T12:00:04Z", 10)+"\n"+codexLine(t, "2026-11-18T12:00:04.100Z", 20))
	if total, _ := sumTokens(t, root); total != 32 {
		t.Fatalf("counted %d tokens, want 32", total)
	}
}

func TestCodexTotalOnlySnapshotDoesNotStartReplayBurst(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "child.jsonl", `{"type":"session_meta","timestamp":"2026-11-18T12:00:00Z","payload":{"id":"child","forked_from_id":"missing"}}
{"type":"event_msg","timestamp":"2026-11-18T12:00:00Z","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":50}}}}`+"\n"+
		codexLine(t, "2026-11-18T12:00:00.100Z", 10))
	if total, _ := sumTokens(t, root); total != 11 {
		t.Fatalf("counted %d tokens, want 11", total)
	}
}

func TestCodexRecordedAndLegacyCopiesUseFullOccurrenceOrder(t *testing.T) {
	root := t.TempDir()
	const at = "2026-11-18T12:00:00Z"
	count := codexLine(t, at, 10)
	settings := func(tier string) string {
		return fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":"thread_settings_applied","thread_settings":{"service_tier":%q}}}`, at, tier)
	}
	fixture(t, root, "sessions/active.jsonl", strings.Join([]string{
		settings("ultrafast"), count, settings("standard"), codexRecordLine(at, "recorded", 10), count,
	}, "\n"))
	// The archive lacks recorded tiers and response IDs.
	fixture(t, root, "archived_sessions/copy.jsonl", count+"\n"+count)
	entries := loadTest(t, "codex", root)
	if len(entries) != 2 || entries[0].SessionID != "active" || entries[1].SessionID != "active" ||
		entries[0].Speed != "ultrafast" || entries[1].Speed != "standard" {
		t.Fatalf("tiers=%+v", entries)
	}
}

func TestCodexPartialArchiveUsesRecordedPosition(t *testing.T) {
	root := t.TempDir()
	const at = "2026-11-18T12:00:00Z"
	count := codexLine(t, at, 10)
	recorded := codexRecordLine(at, "second", 10) + "\n" + count
	fixture(t, root, "sessions/active.jsonl", count+"\n"+recorded+"\n"+count)
	fixture(t, root, "archived_sessions/partial.jsonl", recorded+"\n"+count)
	want := map[string]int{"active": 33}
	if _, got := sumTokens(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens by session %v, want %v", got, want)
	}
}

func TestCodexPartialRolloutsKeepOccurrencesAroundSharedIDs(t *testing.T) {
	const at = "2026-11-18T12:00:00Z"
	count := codexLine(t, at, 10)
	recorded := func(id string) string { return codexRecordLine(at, id, 10) + "\n" + count }
	for _, tc := range []struct {
		name         string
		first, later []string
		want         int
	}{
		{"gap before anchor", []string{count, recorded("b"), recorded("c")}, []string{recorded("a"), recorded("c")}, 33},
		{"gap between anchors", []string{recorded("b"), recorded("d")}, []string{recorded("a"), recorded("b"), count, recorded("d")}, 44},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fixture(t, root, "sessions/early.jsonl", strings.Join(tc.first, "\n"))
			fixture(t, root, "sessions/later.jsonl", strings.Join(tc.later, "\n"))
			if total, sessions := sumTokens(t, root); total != tc.want {
				t.Fatalf("counted %d tokens, want %d: %v", total, tc.want, sessions)
			}
		})
	}
}

func TestCodexMixedCopiesRetainRecordedTier(t *testing.T) {
	root := t.TempDir()
	const at = "2026-11-18T12:00:00Z"
	const settings = `{"type":"event_msg","timestamp":"2026-11-18T12:00:00Z","payload":{"type":"thread_settings_applied","thread_settings":{"service_tier":"ultrafast"}}}`
	count := codexLine(t, at, 10)
	fixture(t, root, "sessions/active.jsonl", settings+"\n"+count)
	fixture(t, root, "archived_sessions/copy.jsonl", codexRecordLine(at, "recorded", 10)+"\n"+count)
	entries := loadTest(t, "codex", root)
	if len(entries) != 1 || entries[0].Speed != "ultrafast" {
		t.Fatalf("expected one Ultrafast request, got %+v", entries)
	}
}

func TestCodexMixedSiblingRequestsRetainTheirOwnTiers(t *testing.T) {
	root := t.TempDir()
	const at = "2026-11-18T12:00:00Z"
	const settings = `{"type":"event_msg","timestamp":"2026-11-18T12:00:00Z","payload":{"type":"thread_settings_applied","thread_settings":{"service_tier":"ultrafast"}}}`
	header := func(id string) string {
		return fmt.Sprintf(`{"type":"session_meta","timestamp":%q,"payload":{"id":%q,"forked_from_id":"parent","history_base":{}}}`, at, id)
	}
	count := codexLine(t, at, 10)
	fixture(t, root, "sessions/legacy.jsonl", header("legacy")+"\n"+settings+"\n"+count)
	fixture(t, root, "sessions/recorded.jsonl", header("recorded")+"\n"+codexRecordLine(at, "response", 10)+"\n"+count)
	entries := loadTest(t, "codex", root)
	if len(entries) != 2 || entries[0].SessionID != "legacy" || entries[0].Speed != "ultrafast" ||
		entries[1].SessionID != "recorded" || entries[1].Speed != "" {
		t.Fatalf("each sibling should retain its own tier: %+v", entries)
	}
}

func TestCodexReplayShadowPreservesIndependentReferenceFork(t *testing.T) {
	root := t.TempDir()
	const at = "2026-11-18T12:00:00Z"
	count := codexLine(t, at, 10)
	fixture(t, root, "sessions/parent.jsonl", `{"type":"session_meta","timestamp":"2026-11-18T11:59:00Z","payload":{"id":"parent"}}`+"\n"+codexLine(t, "2026-11-18T11:59:00Z", 10))
	fixture(t, root, "sessions/fork.jsonl", `{"type":"session_meta","timestamp":"2026-11-18T12:00:00Z","payload":{"id":"fork","forked_from_id":"parent"}}`+"\n"+count)
	fixture(t, root, "sessions/reference.jsonl", `{"type":"session_meta","timestamp":"2026-11-18T12:00:00Z","payload":{"id":"reference","forked_from_id":"parent","history_base":{}}}`+"\n"+count)
	fixture(t, root, "archived_sessions/copy.jsonl", count)
	want := map[string]int{"parent": 11, "reference": 11}
	if _, got := sumTokens(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens by session %v, want %v", got, want)
	}
}

func TestCodexForkMatchingUsesRecordedRequests(t *testing.T) {
	root := t.TempDir()
	const start = "2026-11-17T12:00:00Z"
	const fork = "2026-11-18T12:00:00Z"
	head := func(at, id, extra string) string {
		return fmt.Sprintf(`{"type":"session_meta","timestamp":%q,"payload":{"id":%q%s}}`, at, id, extra)
	}
	parentOpening := codexRecordLine(start, "replayed", 20) + "\n" + codexLine(t, start, 20)
	childOpening := codexRecordLine(fork, "replayed", 20) + "\n" + codexLine(t, fork, 20)
	for _, parentRecorded := range []bool{false, true} {
		at := "2026-11-18T12:01:00Z"
		if parentRecorded {
			at = fork
		}
		parentTail := codexLine(t, at, 10)
		if parentRecorded {
			parentTail = codexRecordLine(fork, "parent-local", 10) + "\n" + parentTail
		}
		fixture(t, root, "sessions/parent.jsonl", head(start, "parent", "")+"\n"+parentOpening+"\n"+parentTail)
		fixture(t, root, "sessions/fork.jsonl", head(fork, "fork", `,"forked_from_id":"parent"`)+"\n"+childOpening+"\n"+codexRecordLine(at, "fork-local", 10)+"\n"+codexLine(t, at, 10))
		// A legacy copy of the entire fork cannot identify its second
		// request. Its first is a known replay with or without its ID.
		want := map[string]int{"parent": 32, "fork": 11}
		for _, opening := range []string{codexLine(t, fork, 20), childOpening} {
			fixture(t, root, "archived_sessions/fork-copy.jsonl", opening+"\n"+codexLine(t, at, 10))
			if _, got := sumTokens(t, root); !reflect.DeepEqual(got, want) {
				t.Errorf("parent recorded=%t: got %v, want %v", parentRecorded, got, want)
			}
		}
	}
}

func codexRecordLine(at, id string, input int) string {
	return fmt.Sprintf(`{"type":"token_usage_record","timestamp":%q,"payload":{"response_id":%q,"usage":{"input_tokens":%d,"output_tokens":1}}}`, at, id, input)
}

func TestCodexReferenceForkKeepsUsageAtTheForkInstant(t *testing.T) {
	root := t.TempDir()
	const at = "2026-11-18T12:00:00Z"
	count := codexLine(t, at, 10)
	fixture(t, root, "parent.jsonl", `{"type":"session_meta","timestamp":"2026-11-18T11:59:00Z","payload":{"id":"parent"}}`+"\n"+count)
	fixture(t, root, "child.jsonl", `{"type":"session_meta","timestamp":"2026-11-18T12:00:00Z","payload":{"id":"child","forked_from_id":"parent","history_base":{"thread_id":"parent"}}}`+"\n"+count)
	want := map[string]int{"child": 11, "parent": 11}
	if _, got := sumTokens(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens by session %v, want %v", got, want)
	}
}

func TestCodexReferenceForkKeepsUsageFromHeaderlessParent(t *testing.T) {
	root := t.TempDir()
	const at = "2026-11-18T12:00:00Z"
	count := codexLine(t, at, 10)
	fixture(t, root, "sessions/parent.jsonl", count)
	fixture(t, root, "sessions/child.jsonl", `{"type":"session_meta","timestamp":"2026-11-18T12:00:00Z","payload":{"id":"child","forked_from_id":"parent","history_base":{}}}`+"\n"+count)
	want := map[string]int{"child": 11, "parent": 11}
	if _, got := sumTokens(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens by session %v, want %v", got, want)
	}
}

func TestCodexReplacementIsNotAPhysicalReplay(t *testing.T) {
	const thread = "11111111-1111-4111-8111-111111111111"
	const replacement = "22222222-2222-4222-8222-222222222222"
	const start = "2026-11-18T11:59:00Z"
	const fork = "2026-11-18T12:00:00Z"
	for _, tc := range []struct {
		name, extra string
		want        int
	}{
		{"rollout-2026-11-18T12-00-00-" + thread + "_" + replacement + ".jsonl", "", 22},
		{"child.jsonl", `,"history_base":false`, 11},
	} {
		root := t.TempDir()
		fixture(t, root, "parent.jsonl", `{"type":"session_meta","timestamp":"2026-11-18T11:59:00Z","payload":{"id":"parent"}}`+"\n"+codexLine(t, start, 10))
		meta := fmt.Sprintf(`{"type":"session_meta","timestamp":%q,"payload":{"id":%q,"forked_from_id":"parent"%s}}`, fork, thread, tc.extra)
		fixture(t, root, tc.name, meta+"\n"+codexLine(t, fork, 10))
		if total, _ := sumTokens(t, root); total != tc.want {
			t.Errorf("%s counted %d tokens, want %d", tc.name, total, tc.want)
		}
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

// Like upstream, a file that cannot be read or parsed is skipped, as is a
// directory that cannot be read, except an OpenClaw one, which fails the load.
func TestDamagedFilesAreSkipped(t *testing.T) {
	t.Run("JSON", func(t *testing.T) {
		root := t.TempDir()
		fixture(t, root, "threads/good.json", `{"id":"s","messages":[{"role":"assistant","usage":{"timestamp":"2026-01-02T00:00:00Z","model":"gpt-5","inputTokens":1}}]}`)
		fixture(t, root, "threads/bad.json", `{"id":`)
		entries, err := Load(context.Background(), "amp", []string{root}, time.UTC)
		if len(entries) != 1 || err != nil {
			t.Fatalf("entries=%+v err=%v", entries, err)
		}
	})
	t.Run("SQLite", func(t *testing.T) {
		root := t.TempDir()
		fixture(t, root, "agents/main/agent/openclaw-agent.sqlite", "this is not sqlite")
		fixture(t, root, "agents/main/sessions/good.jsonl", `{"type":"message","timestamp":"2026-01-02T00:00:00Z","message":{"role":"assistant","model":"gpt-5","usage":{"input":1}}}`)
		entries, err := Load(context.Background(), "openclaw", []string{root}, time.UTC)
		if len(entries) != 1 || err != nil {
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
		if _, err := loadPi(context.Background(), []string{missing}, time.UTC, "auto"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unreadable Codex file", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing.jsonl")
		if _, err := loadCodex(context.Background(), []string{missing}, []string{filepath.Dir(missing)}, time.UTC); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unreadable directory", func(t *testing.T) {
		root := t.TempDir()
		fixture(t, root, "threads/good.json", `{"id":"s","messages":[{"role":"assistant","usage":{"timestamp":"2026-01-02T00:00:00Z","model":"gpt-5","inputTokens":1}}]}`)
		fixture(t, root, "agents/main/sessions/good.jsonl", `{"type":"message","timestamp":"2026-01-02T00:00:00Z","message":{"role":"assistant","model":"gpt-5","usage":{"input":1}}}`)
		for _, dir := range []string{"threads/locked", "agents/locked"} {
			locked := filepath.Join(root, dir)
			if err := os.Mkdir(locked, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(locked, 0700) })
			if _, err := os.ReadDir(locked); err == nil {
				t.Skip("file permissions do not keep this user from reading, as for root or on Windows")
			}
		}
		if entries, err := Load(context.Background(), "amp", []string{root}, time.UTC); len(entries) != 1 || err != nil {
			t.Fatalf("amp: entries=%+v err=%v", entries, err)
		}
		if _, err := Load(context.Background(), "openclaw", []string{root}, time.UTC); err == nil || !strings.Contains(err.Error(), "agents/locked") {
			t.Fatalf("openclaw: err=%v", err)
		}
	})
}

// Two rollouts' copies of one event count once and use the strongest
// recorded tier. Attribution stays with the first rollout.
func TestCodexMergesMatchingUsageFromDistinctSessions(t *testing.T) {
	event := func(session, speed string) types.UsageEntry {
		return types.UsageEntry{SessionID: session, SourceFile: session + ".jsonl", Timestamp: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Model: "gpt-5", InputTokens: 90, CacheReadInputTokens: 10, OutputTokens: 50, TotalTokens: 150, Speed: speed}
	}
	for _, tc := range []struct{ first, second, want string }{
		{"", "", ""}, {"", "fast", "fast"}, {"fast", "", "fast"},
		{"standard", "fast", "standard"}, {"fast", "standard", "standard"},
		{"", "ultrafast", "ultrafast"}, {"fast", "ultrafast", "ultrafast"},
		{"ultrafast", "flex", "ultrafast"}, {"ultrafast", "standard", "standard"},
	} {
		merged := mergeCodexCopies([]types.UsageEntry{event("session-a", tc.first), event("session-b", tc.second)}, nil, nil)
		if len(merged) != 1 || merged[0].SessionID != "session-a" || merged[0].Speed != tc.want {
			t.Errorf("tiers %q then %q: %+v", tc.first, tc.second, merged)
		}
	}
}

// Ports upstream's dedupes_copied_branch_history_across_session_files: a
// branch rollout that copied its parent's history without naming the parent
// counts only its own usage in every report.
func TestCodexMergesCopiedBranchHistoryAcrossSessionFiles(t *testing.T) {
	root := t.TempDir()
	tokenCount := func(ts string, input, cached, output, reasoning, total int) string {
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":%d,"total_tokens":%d}}}}`, ts, input, cached, output, reasoning, total)
	}
	parent := `{"timestamp":"2026-05-12T08:00:00.000Z","type":"turn_context","payload":{"model":"gpt-5.2"}}` + "\n" + tokenCount("2026-05-12T08:01:00.000Z", 1000, 100, 200, 20, 1200)
	fixture(t, root, "2026-05-12T08-00-00-parent.jsonl", parent)
	fixture(t, root, "2026-05-12T08-02-00-branch.jsonl", parent+"\n"+tokenCount("2026-05-12T08:02:00.000Z", 1600, 300, 450, 40, 2050))
	merged := loadTest(t, "codex", root)
	if len(merged) != 2 || merged[0].SessionID != "2026-05-12T08-00-00-parent" || merged[1].SessionID != "2026-05-12T08-02-00-branch" {
		t.Fatal(merged)
	}
	requireTokens(t, merged[0], 900, 200, 100, 0, 1200)
	requireTokens(t, merged[1], 400, 250, 200, 0, 850)
	if merged[0].ReasoningTokens != 20 || merged[1].ReasoningTokens != 20 {
		t.Fatal(merged)
	}
}
