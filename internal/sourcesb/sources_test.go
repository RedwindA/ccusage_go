package sourcesb

import (
	"context"
	"database/sql"
	"encoding/binary"
	"github.com/RedwindA/ccusage_go/internal/types"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, p, body string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
}
func database(t *testing.T, p, schema string) *sql.DB {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite", p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(schema); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func mustLoad(t *testing.T, name string, paths ...string) []types.UsageEntry {
	t.Helper()
	e, err := Load(context.Background(), name, paths, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// These schemas and buckets mirror the corresponding upstream Rust adapter fixtures.
func TestSQLiteSources(t *testing.T) {
	t.Run("hermes", func(t *testing.T) {
		dir := t.TempDir()
		database(t, filepath.Join(dir, "state.db"), `CREATE TABLE sessions(id TEXT,model TEXT,billing_provider TEXT,started_at REAL,message_count INTEGER,input_tokens INTEGER,output_tokens INTEGER,cache_read_tokens INTEGER,cache_write_tokens INTEGER,reasoning_tokens INTEGER,estimated_cost_usd REAL,actual_cost_usd REAL); INSERT INTO sessions VALUES('s','claude-sonnet-4-6','anthropic',1778000000,3,100,20,30,4,5,9,0);`)
		e := mustLoad(t, "hermes", dir)
		if len(e) != 1 || e[0].TotalTokens != 159 || !e[0].HasCost || e[0].Cost != 0 || n(e[0].Raw["request_count"]) != 3 {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("goose", func(t *testing.T) {
		dir := t.TempDir()
		database(t, filepath.Join(dir, "sessions.db"), `CREATE TABLE sessions(id TEXT,model_config_json TEXT,provider_name TEXT,created_at TEXT,total_tokens INTEGER,input_tokens INTEGER,output_tokens INTEGER,accumulated_total_tokens INTEGER,accumulated_input_tokens INTEGER,accumulated_output_tokens INTEGER); INSERT INTO sessions VALUES('s','{"model_name":"gpt-5"}','openai','2026-05-05 12:00:00',20,10,5,170,100,50);`)
		e := mustLoad(t, "goose", dir)
		if len(e) != 1 || e[0].TotalTokens != 170 || e[0].ReasoningTokens != 20 || e[0].HasCost {
			t.Fatalf("%+v", e)
		}
	})
	for _, name := range []string{"opencode", "kilo"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			db := database(t, filepath.Join(dir, name+".db"), `CREATE TABLE message(id TEXT,session_id TEXT,data TEXT);`)
			payload := `{"role":"assistant","modelID":"claude-sonnet-4-6","providerID":"anthropic","time":{"created":1778000000000},"tokens":{"input":100,"output":20,"reasoning":5,"cache":{"read":30,"write":4},"total":170},"cost":0}`
			if _, err := db.Exec("INSERT INTO message VALUES(?,?,?)", "m1", "s1", payload); err != nil {
				t.Fatal(err)
			}
			if name == "opencode" {
				write(t, filepath.Join(dir, "storage/message/s1/m1.json"), `{"id":"m1","sessionID":"s1","modelID":"claude-sonnet-4-6","providerID":"anthropic","time":{"created":1778000000000},"tokens":{"input":999}}`)
			}
			e := mustLoad(t, name, dir)
			if len(e) != 1 || e[0].TotalTokens != 170 || e[0].InputTokens != 100 || e[0].ReasoningTokens != 16 || !e[0].HasCost {
				t.Fatalf("%+v", e)
			}
		})
	}
	t.Run("zcode", func(t *testing.T) {
		dir := t.TempDir()
		database(t, filepath.Join(dir, "cli/db/db.sqlite"), `CREATE TABLE session(id TEXT,directory TEXT,version TEXT); CREATE TABLE model_usage(id TEXT,session_id TEXT,started_at INTEGER,model_id TEXT,status TEXT,input_tokens INTEGER,output_tokens INTEGER,cache_creation_input_tokens INTEGER,cache_read_input_tokens INTEGER,computed_total_tokens INTEGER,provider_id TEXT); INSERT INTO session VALUES('s','/work/project','1'); INSERT INTO model_usage VALUES('m','s',1778000000000,'glm-5','completed',100,20,30,90,130,'zai'); INSERT INTO model_usage VALUES('pending','s',1778000000000,'glm-5','pending',500,500,0,0,1000,'zai');`)
		e := mustLoad(t, "zcode", dir)
		if len(e) != 1 || e[0].InputTokens != 0 || e[0].CacheReadInputTokens != 90 || e[0].CacheCreationInputTokens != 10 || e[0].TotalTokens != 130 || e[0].Workspace != "/work/project" {
			t.Fatalf("%+v", e)
		}
	})
}
func TestOpenCodeV2AndSessionFallback(t *testing.T) {
	dir := t.TempDir()
	database(t, filepath.Join(dir, "opencode.db"), `CREATE TABLE session_message(id TEXT,session_id TEXT,type TEXT,time_created INTEGER,data TEXT); CREATE TABLE session_v2(id TEXT,time_created INTEGER,cost REAL,tokens_input INTEGER,tokens_output INTEGER,tokens_cache_read INTEGER,tokens_cache_write INTEGER,tokens_reasoning INTEGER,model TEXT); INSERT INTO session_message VALUES('m','s','assistant',1778000000000,'{"model":{"id":"gpt-5","providerID":"openai"},"tokens":{"input":10,"output":5}}'); INSERT INTO session_v2 VALUES('s',1778000000000,9,999,0,0,0,0,'gpt-5'); INSERT INTO session_v2 VALUES('fallback',1778000000000,2,20,4,3,2,1,'{"id":"gpt-5","providerID":"openai"}');`)
	e := mustLoad(t, "opencode", dir)
	if len(e) != 2 {
		t.Fatalf("%+v", e)
	}
	for _, x := range e {
		if x.SessionID == "s" && (x.TotalTokens != 15 || x.Raw["session_aggregate"] == true) {
			t.Fatalf("%+v", x)
		}
		if x.SessionID == "fallback" && (x.TotalTokens != 30 || x.Raw["session_aggregate"] != true) {
			t.Fatalf("%+v", x)
		}
	}
}
func TestKimiFormatsAndDedup(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.json"), `{"model":"kimi-k2.5"}`)
	write(t, filepath.Join(dir, "sessions/ws/old/wire.jsonl"), `{"timestamp":1778000000,"message":{"type":"StatusUpdate","payload":{"message_id":"m","token_usage":{"input_other":100,"output":20,"input_cache_read":40,"input_cache_creation":5,"total":170}}}}
{"timestamp":1778000000,"message":{"type":"StatusUpdate","payload":{"message_id":"m","token_usage":{"input_other":100,"output":20,"input_cache_read":40,"input_cache_creation":5,"total":170}}}}
malformed
`)
	write(t, filepath.Join(dir, "sessions/ws/new/agents/main/wire.jsonl"), `{"type":"usage.record","usageScope":"session","time":1778000000000,"usage":{"inputOther":900}}
{"type":"usage.record","usageScope":"turn","model":"kimi-code/kimi-k2.6","time":1778000000000,"usage":{"inputOther":10,"output":3,"inputCacheRead":7}}`)
	e := mustLoad(t, "kimi", dir)
	if len(e) != 2 {
		t.Fatalf("%+v", e)
	}
	for _, x := range e {
		if x.SessionID == "old" && (x.Model != "kimi-k2.5" || x.TotalTokens != 170) {
			t.Fatalf("%+v", x)
		}
		if x.SessionID == "new" && (x.Model != "kimi-k2.6" || x.TotalTokens != 20) {
			t.Fatalf("%+v", x)
		}
	}
}
func TestCodebuffMetadataAndRunState(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "projects/project/chats/2026-05-05T12-30-00.000Z/chat-messages.json"), `[{"id":"one","variant":"ai","credits":2,"metadata":{"model":"gpt-5","usage":{"inputTokens":100,"outputTokens":20,"promptTokensDetails":{"cachedTokens":30},"totalTokens":170}}},{"variant":"agent","metadata":{"runState":{"sessionState":{"mainAgentState":{"messageHistory":[{"role":"assistant","providerOptions":{"codebuff":{"model":"claude-sonnet-4-6","usage":{"input_tokens":5,"output_tokens":2,"credits":3}}}}]}}}}},{"role":"user","credits":99}]`)
	e := mustLoad(t, "codebuff", dir)
	if len(e) != 2 || e[0].DateKey != "2026-05-05" || e[0].TotalTokens != 170 || e[0].Credits != 2 || e[1].Model != "claude-sonnet-4-6" || e[1].Credits != 3 {
		t.Fatalf("%+v", e)
	}
}
func TestCopilotReconciliation(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "session-state/s/events.jsonl"), `{"type":"session.shutdown","id":"a","timestamp":"2026-05-05T10:00:00Z","data":{"modelMetrics":{"gpt-5-1m":{"usage":{"inputTokens":100,"outputTokens":20,"cacheReadTokens":30,"cacheWriteTokens":10,"reasoningTokens":5},"requests":{"count":2}}}}}
{"type":"session.shutdown","id":"b","timestamp":"2026-05-06T10:00:00Z","data":{"modelMetrics":{"gpt-5":{"usage":{"inputTokens":150,"outputTokens":30,"cacheReadTokens":45,"cacheWriteTokens":15},"requests":{"count":3}}}}}`)
	write(t, filepath.Join(dir, "otel/log.jsonl"), `{"type":"span","name":"chat gpt-5","traceId":"trace","spanId":"span","timestamp":1778065200000,"attributes":{"gen_ai.operation.name":"chat","gen_ai.conversation.id":"s","gen_ai.response.model":"gpt-5","gen_ai.usage.input_tokens":10,"gen_ai.usage.output_tokens":5}}
{"type":"log","timestamp":1778065200000,"attributes":{"event.name":"gen_ai.client.inference.operation.details","gen_ai.conversation.id":"s","gen_ai.response.model":"gpt-5","gen_ai.usage.input_tokens":10,"gen_ai.usage.output_tokens":5},"spanContext":{"traceId":"trace"}}`)
	e := mustLoad(t, "copilot", dir)
	if len(e) != 3 {
		t.Fatalf("%+v", e)
	}
	total := 0
	for _, x := range e {
		total += x.TotalTokens
	}
	if total != 195 {
		t.Fatalf("total=%d %+v", total, e)
	}
	if e[0].InputTokens != 60 || e[1].InputTokens != 30 || n(e[1].Raw["request_count"]) != 1 {
		t.Fatalf("%+v", e)
	}
}
func varint(k, v uint64) []byte {
	b := binary.AppendUvarint(nil, k<<3)
	return binary.AppendUvarint(b, v)
}
func blob(k uint64, v []byte) []byte {
	b := binary.AppendUvarint(nil, k<<3|2)
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}
func TestAntigravityProtobufAndAliases(t *testing.T) {
	dir := t.TempDir()
	db := database(t, filepath.Join(dir, "conversations/s.db"), `CREATE TABLE gen_metadata(idx INTEGER PRIMARY KEY,data BLOB); CREATE TABLE steps(idx INTEGER PRIMARY KEY,metadata BLOB); CREATE TABLE trajectory_metadata_blob(data BLOB);`)
	usage := append(varint(2, 100), varint(3, 30)...)
	usage = append(usage, varint(5, 50)...)
	usage = append(usage, varint(9, 10)...)
	usage = append(usage, blob(11, []byte("response-1"))...)
	model := blob(19, []byte("model_placeholder_m318"))
	model = append(model, blob(4, usage)...)
	model = append(model, blob(9, blob(4, varint(1, 1778000000)))...)
	if _, e := db.Exec("INSERT INTO gen_metadata VALUES(1,?)", blob(1, model)); e != nil {
		t.Fatal(e)
	}
	step := blob(9, usage)
	step = append(step, blob(8, varint(1, 1778000001))...)
	if _, e := db.Exec("INSERT INTO steps VALUES(1,?)", step); e != nil {
		t.Fatal(e)
	}
	entries := mustLoad(t, "antigravity", dir)
	if len(entries) != 1 || entries[0].TotalTokens != 180 || entries[0].OutputTokens != 20 || entries[0].ReasoningTokens != 10 || entries[0].Model != "gemini-3.8-flash-high" || entries[0].Timestamp.Unix() != 1778000000 {
		t.Fatalf("%+v", entries)
	}
	if _, err := proto([]byte{10, 255}); err == nil {
		t.Fatal("accepted truncated protobuf")
	}
}
func TestEnvironmentAndCancellation(t *testing.T) {
	t.Setenv("KIMI_DATA_DIR", "/first, /second,/first")
	p := DefaultPaths("kimi", "/home/person")
	if len(p) != 3 || p[1] != "/second" {
		t.Fatal(p)
	}
	p = DefaultPathsForHome("kimi", "/home/person")
	if len(p) != 2 || p[0] != filepath.Join("/home/person", ".kimi") {
		t.Fatal(p)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Load(ctx, "kimi", []string{t.TempDir()}, time.UTC)
	if err != context.Canceled {
		t.Fatalf("got %v", err)
	}
}

func TestCopilotUntilKeepsHistoricalOtel(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "session-state/s/events.jsonl"), `{"type":"session.shutdown","id":"first","timestamp":"2026-05-05T10:00:00Z","data":{"modelMetrics":{"gpt-5":{"usage":{"inputTokens":100}}}}}
{"type":"session.shutdown","id":"second","timestamp":"2026-05-07T10:00:00Z","data":{"modelMetrics":{"gpt-5":{"usage":{"inputTokens":300}}}}}`)
	write(t, filepath.Join(dir, "otel/events.jsonl"), `{"type":"span","name":"chat gpt-5","timestamp":1778065200000,"attributes":{"gen_ai.conversation.id":"s","gen_ai.response.model":"gpt-5","gen_ai.usage.input_tokens":50}}`)
	until := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	entries, err := LoadUntil(context.Background(), "copilot", []string{dir}, time.UTC, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].TotalTokens+entries[1].TotalTokens != 150 {
		t.Fatalf("%+v", entries)
	}
}
func TestDiscoveryExcludesNestedCopilotAndInvalidKimiLayouts(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "sessions/a/b/c/wire.jsonl"), `{"type":"usage.record","usageScope":"turn","usage":{"inputOther":50}}`)
	if e := mustLoad(t, "kimi", dir); len(e) != 0 {
		t.Fatal(e)
	}
	write(t, filepath.Join(dir, "session-state/nested/deep/events.jsonl"), `{"type":"session.shutdown","timestamp":"2026-05-05T10:00:00Z","data":{"modelMetrics":{"gpt-5":{"usage":{"inputTokens":100}}}}}`)
	if e := mustLoad(t, "copilot", dir); len(e) != 0 {
		t.Fatal(e)
	}
}
func TestOpenCodeChannelAndConfiguredOrder(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for i, dir := range []string{a, b} {
		db := database(t, filepath.Join(dir, "opencode-preview.db"), `CREATE TABLE message(id TEXT,session_id TEXT,data TEXT);`)
		data := `{"modelID":"gpt-5","providerID":"openai","tokens":{"input":10},"cost":1}`
		if i == 1 {
			data = `{"modelID":"gpt-5","providerID":"openai","tokens":{"input":20},"cost":2}`
		}
		if _, err := db.Exec("INSERT INTO message VALUES('same','s',?)", data); err != nil {
			t.Fatal(err)
		}
	}
	e := mustLoad(t, "opencode", b, a)
	if len(e) != 1 || e[0].InputTokens != 20 {
		t.Fatal(e)
	}
}
func TestAntigravityRetriesScalarOrderAndSchemaErrors(t *testing.T) {
	p, err := proto(append(varint(2, 1), varint(2, 100)...))
	if err != nil || pn(p, 2) != 100 {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = proto(append(blob(19, []byte("first")), blob(19, []byte("last"))...))
	if err != nil || pt(p, 19) != "last" || string(pb(p, 19)) != "first" {
		t.Fatalf("%+v %v", p, err)
	}
	usage := append(varint(1, 1318), varint(2, 50)...)
	m, err := antiMetadata(blob(28, blob(2, usage)), true)
	if err != nil || len(m.usages) != 1 {
		t.Fatalf("%+v %v", m, err)
	}
	dir := t.TempDir()
	database(t, filepath.Join(dir, "broken.db"), `CREATE TABLE unrelated(id TEXT);`)
	if _, err := Load(context.Background(), "antigravity", []string{dir}, time.UTC); err == nil {
		t.Fatal("missing gen_metadata must fail")
	}
}
