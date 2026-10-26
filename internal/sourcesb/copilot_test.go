package sourcesb

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RedwindA/ccusage_go/internal/types"
)

func copilotChatSpan(trace, span string, end int64, response string, input int) string {
	return fmt.Sprintf(`{"type":"span","traceId":%q,"spanId":%q,"name":"chat test-model","endTime":[%d,0],"attributes":{"gen_ai.operation.name":"chat","gen_ai.response.model":"test-model","gen_ai.conversation.id":"conv-1","gen_ai.response.id":%q,"gen_ai.usage.input_tokens":%d,"gen_ai.usage.output_tokens":10}}`, trace, span, end, response, input)
}

func copilotIDs(t *testing.T, lines ...string) []string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "copilot.jsonl")
	write(t, file, strings.Join(lines, "\n"))
	entries, err := loadCopilot(file)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	return ids
}

func TestCopilotCountsReExportedChatSpansOnce(t *testing.T) {
	t.Run("re-export", func(t *testing.T) {
		// The CLI exports some calls a second time, with the response ID and
		// usage of the call but a new trace or span ID.
		ids := copilotIDs(t,
			copilotChatSpan("trace-1", "span-1", 1775934264, "resp-1", 100),
			copilotChatSpan("trace-1", "span-2", 1775934300, "resp-2", 200),
			copilotChatSpan("trace-2", "span-3", 1775934368, "resp-1", 100),
			copilotChatSpan("trace-1", "span-4", 1775934370, "resp-2", 200),
			copilotChatSpan("trace-3", "span-5", 1775934500, "resp-3", 300),
			copilotChatSpan("trace-1", "span-6", 1775934400, "resp-3", 300),
		)
		if want := []string{"trace-1:span-1", "trace-1:span-2", "trace-1:span-6"}; !reflect.DeepEqual(ids, want) {
			t.Fatalf("got %v, want %v", ids, want)
		}
	})
	t.Run("calls of one turn share a response ID", func(t *testing.T) {
		// VS Code reports the response ID of the turn on every call it makes.
		ids := copilotIDs(t,
			copilotChatSpan("trace-1", "span-1", 1775934264, "turn-1", 1000),
			copilotChatSpan("trace-1", "span-2", 1775934270, "turn-1", 1200),
			copilotChatSpan("trace-1", "span-3", 1775934280, "turn-1", 1400),
		)
		if want := []string{"trace-1:span-1", "trace-1:span-2", "trace-1:span-3"}; !reflect.DeepEqual(ids, want) {
			t.Fatalf("got %v, want %v", ids, want)
		}
	})
}

func TestCopilotSplitsOtelInputLikeSessionState(t *testing.T) {
	// Copilot counts cache reads and writes in the input tokens of both
	// sources; one call, as each source records it.
	dir := t.TempDir()
	otel := filepath.Join(dir, "copilot.jsonl")
	write(t, otel, `{"type":"span","traceId":"trace-1","spanId":"span-1","name":"chat claude-haiku-4.5","endTime":[1775934264,0],"attributes":{"gen_ai.operation.name":"chat","gen_ai.response.model":"claude-haiku-4.5","gen_ai.conversation.id":"session-1","gen_ai.usage.input_tokens":20000,"gen_ai.usage.output_tokens":300,"gen_ai.usage.cache_read.input_tokens":12000,"gen_ai.usage.cache_write.input_tokens":7990}}`)
	shutdown := filepath.Join(dir, "session-state/session-1/events.jsonl")
	write(t, shutdown, `{"type":"session.shutdown","id":"shutdown-1","timestamp":"2026-04-11T19:04:24.000Z","data":{"modelMetrics":{"claude-haiku-4.5":{"usage":{"inputTokens":20000,"outputTokens":300,"cacheReadTokens":12000,"cacheWriteTokens":7990},"requests":{"count":1}}}}}`)
	var splits [][4]int
	for _, file := range []string{otel, shutdown} {
		loaded, err := loadCopilot(file)
		var entries []types.UsageEntry
		for _, e := range loaded {
			if e.Raw["credit_snapshot"] == nil {
				entries = append(entries, e)
			}
		}
		if err != nil || len(entries) != 1 {
			t.Fatalf("%s: %+v %v", file, loaded, err)
		}
		e := entries[0]
		splits = append(splits, [4]int{e.InputTokens, e.CacheReadInputTokens, e.CacheCreationInputTokens, e.OutputTokens})
	}
	if want := [4]int{10, 12000, 7990, 300}; splits[0] != want || splits[1] != want {
		t.Fatalf("got %v, want %v for both", splits, want)
	}
}

// Like upstream's attr_number_first, a cache write, reasoning or total alias
// counts only when positive, so a zero one falls through to the next.
func TestCopilotAliasesFallThroughToAPositiveOne(t *testing.T) {
	file := filepath.Join(t.TempDir(), "copilot.jsonl")
	write(t, file, `{"type":"span","traceId":"t1","spanId":"s1","name":"chat claude-sonnet-4-6","endTime":[1791117000,0],"attributes":{"gen_ai.operation.name":"chat","gen_ai.response.model":"claude-sonnet-4-6","gen_ai.conversation.id":"sess-1","gen_ai.usage.input_tokens":20000,"gen_ai.usage.output_tokens":100,"gen_ai.usage.cache_read.input_tokens":12000,"gen_ai.usage.cache_write.input_tokens":0,"gen_ai.usage.cache_creation.input_tokens":7990,"gen_ai.usage.reasoning.output_tokens":0,"gen_ai.usage.reasoning_tokens":40,"gen_ai.usage.total_tokens":0,"gen_ai.usage.total.token_count":25000}}`)
	entries, err := loadCopilot(file)
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	e := entries[0]
	if e.InputTokens != 10 || e.CacheCreationInputTokens != 7990 || e.Raw["reasoning_output_tokens"] != 40 || e.TotalTokens != 25000 {
		t.Fatalf("%+v", e)
	}
}

// copilotShutdown is a shutdown whose test-model breakdown reports requests
// requests of 100 input tokens each.
func copilotShutdown(id, timestamp string, requests int) string {
	return fmt.Sprintf(`{"type":"session.shutdown","id":%q,"timestamp":%q,"data":{"modelMetrics":{"test-model":{"usage":{"inputTokens":%d,"outputTokens":%d},"requests":{"count":%d}}}}}`, id, timestamp, requests*100, requests*10, requests)
}

// copilotCall is one test-model call of session-1 with a single input token.
func copilotCall(span, timestamp string) string {
	at, _ := time.Parse(time.RFC3339, timestamp)
	return fmt.Sprintf(`{"type":"span","traceId":"trace-1","spanId":%q,"name":"chat test-model","endTime":[%d,0],"attributes":{"gen_ai.operation.name":"chat","gen_ai.response.model":"test-model","gen_ai.conversation.id":"session-1","gen_ai.usage.input_tokens":1,"gen_ai.usage.output_tokens":1}}`, span, at.Unix())
}

// copilotRows loads the shutdowns and OTel calls and returns "timestamp input"
// per entry: OTel calls have one input token, shutdown intervals 100 per
// request.
func copilotRows(t *testing.T, shutdowns, calls []string, until time.Time) []string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "session-state/session-1/events.jsonl"), strings.Join(shutdowns, "\n"))
	write(t, filepath.Join(dir, "otel/otel.jsonl"), strings.Join(calls, "\n"))
	return copilotDirRows(t, dir, until)
}

// copilotDirRows loads the Copilot home dir and returns "timestamp input" per
// entry.
func copilotDirRows(t *testing.T, dir string, until time.Time) []string {
	t.Helper()
	entries, err := LoadUntil(context.Background(), "copilot", []string{dir}, time.UTC, until)
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, e := range entries {
		rows = append(rows, fmt.Sprintf("%s %d", e.Timestamp.UTC().Format(time.RFC3339), e.InputTokens))
	}
	return rows
}

// The first process shuts down cleanly after two calls, the second makes three
// calls and dies without a shutdown, and the third restores the breakdown of
// the first shutdown, makes one call and shuts down with three requests: the
// three calls of the second process are in no shutdown.
var copilotCrashedShutdowns = []string{
	copilotShutdown("shutdown-1", "2026-01-02T10:00:00Z", 2),
	copilotShutdown("shutdown-2", "2026-01-03T10:00:00Z", 3),
}
var copilotCrashedCalls = []string{
	copilotCall("first-1", "2026-01-02T09:00:00Z"),
	copilotCall("first-2", "2026-01-02T09:30:00Z"),
	copilotCall("crashed-1", "2026-01-02T20:00:00Z"),
	copilotCall("crashed-2", "2026-01-02T21:00:00Z"),
	copilotCall("crashed-3", "2026-01-03T08:00:00Z"),
	copilotCall("restored-1", "2026-01-03T09:00:00Z"),
	copilotCall("after-shutdown", "2026-01-03T11:00:00Z"),
}

func TestCopilotOtelReplacesShutdownsMissingCalls(t *testing.T) {
	for _, tc := range []struct {
		name             string
		shutdowns, calls []string
		want             []string
	}{
		{
			name:      "process ended without a shutdown",
			shutdowns: copilotCrashedShutdowns,
			calls:     copilotCrashedCalls,
			want: []string{
				"2026-01-02T09:00:00Z 1", "2026-01-02T09:30:00Z 1", "2026-01-02T20:00:00Z 1", "2026-01-02T21:00:00Z 1",
				"2026-01-03T08:00:00Z 1", "2026-01-03T09:00:00Z 1", "2026-01-03T11:00:00Z 1",
			},
		},
		{
			name:      "shutdown usage from before the OTel export started",
			shutdowns: copilotCrashedShutdowns,
			calls: []string{
				copilotCall("crashed-1", "2026-01-02T20:00:00Z"),
				copilotCall("crashed-2", "2026-01-02T21:00:00Z"),
				copilotCall("restored-1", "2026-01-03T09:00:00Z"),
			},
			want: []string{"2026-01-02T10:00:00Z 200", "2026-01-02T20:00:00Z 1", "2026-01-02T21:00:00Z 1", "2026-01-03T09:00:00Z 1"},
		},
		{
			name:      "OTel has no calls the shutdowns miss",
			shutdowns: copilotCrashedShutdowns,
			calls: []string{
				copilotCall("first-1", "2026-01-02T09:00:00Z"),
				copilotCall("first-2", "2026-01-02T09:30:00Z"),
				copilotCall("second-1", "2026-01-03T09:00:00Z"),
				copilotCall("after-shutdown", "2026-01-03T11:00:00Z"),
			},
			want: []string{"2026-01-02T10:00:00Z 200", "2026-01-03T10:00:00Z 100", "2026-01-03T11:00:00Z 1"},
		},
		{
			// Process A resumes after shutdown-1 and makes two calls. Process
			// B resumes the same session, makes none and shuts down first,
			// with the breakdown of shutdown-1; A's later shutdown reports
			// A's two calls.
			name: "concurrent process shuts down with the calls",
			shutdowns: []string{
				copilotShutdown("shutdown-1", "2026-01-02T10:00:00Z", 1),
				copilotShutdown("shutdown-b", "2026-01-02T10:55:00Z", 1),
				copilotShutdown("shutdown-a", "2026-01-02T11:00:00Z", 3),
			},
			calls: []string{
				copilotCall("first-1", "2026-01-02T09:00:00Z"),
				copilotCall("a-1", "2026-01-02T10:40:00Z"),
				copilotCall("a-2", "2026-01-02T10:50:00Z"),
			},
			want: []string{"2026-01-02T10:00:00Z 100", "2026-01-02T11:00:00Z 200"},
		},
		{
			name:      "shutdowns without request counts",
			shutdowns: []string{`{"type":"session.shutdown","id":"shutdown-1","timestamp":"2026-01-02T10:00:00Z","data":{"modelMetrics":{"test-model":{"usage":{"inputTokens":100}}}}}`},
			calls:     []string{copilotCall("first-1", "2026-01-02T09:00:00Z"), copilotCall("first-2", "2026-01-02T09:30:00Z")},
			want:      []string{"2026-01-02T10:00:00Z 100"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := copilotRows(t, tc.shutdowns, tc.calls, time.Time{}); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCopilotOtelReplacementCountsEachSpanOnce(t *testing.T) {
	// The report keeps one row per span ID, so a span exported twice, or a
	// copy of the export under another otel/ path, is one call: OTel holds
	// as many calls as the shutdowns report.
	calls := []string{
		copilotCall("first-1", "2026-01-02T09:00:00Z"),
		copilotCall("first-2", "2026-01-02T09:30:00Z"),
		copilotCall("second-1", "2026-01-03T09:00:00Z"),
	}
	want := []string{"2026-01-02T10:00:00Z 200", "2026-01-03T10:00:00Z 100"}
	t.Run("span exported twice", func(t *testing.T) {
		if got := copilotRows(t, copilotCrashedShutdowns, append(append([]string{}, calls...), calls...), time.Time{}); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("copy of the export", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "session-state/session-1/events.jsonl"), strings.Join(copilotCrashedShutdowns, "\n"))
		write(t, filepath.Join(dir, "otel/otel.jsonl"), strings.Join(calls, "\n"))
		write(t, filepath.Join(dir, "otel/archive/otel.jsonl"), strings.Join(calls, "\n"))
		if got := copilotDirRows(t, dir, time.Time{}); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestCopilotOtelReplacementIgnoresUntil(t *testing.T) {
	// The report drops rows after --until; the rows it keeps match the
	// unbounded load.
	onFirstDay := func(rows []string) []string {
		var out []string
		for _, row := range rows {
			if strings.HasPrefix(row, "2026-01-02") {
				out = append(out, row)
			}
		}
		return out
	}
	want := onFirstDay(copilotRows(t, copilotCrashedShutdowns, copilotCrashedCalls, time.Time{}))
	got := onFirstDay(copilotRows(t, copilotCrashedShutdowns, copilotCrashedCalls, time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCopilotParsesCreditTotals(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session-state/session-1/events.jsonl")
	write(t, file, strings.Join([]string{
		`{"type":"session.usage_checkpoint","id":"checkpoint-1","timestamp":"2026-04-15T09:50:00.000Z","data":{"totalNanoAiu":51322830000,"promptCacheBreakState":[]}}`,
		`{"type":"session.shutdown","id":"shutdown-1","timestamp":"2026-04-15T09:52:27.352Z","data":{"totalNanoAiu":93524970000.0,"modelMetrics":{"test-model":{"usage":{"inputTokens":100,"outputTokens":50},"totalNanoAiu":42202140000.0}}}}`,
		`{"type":"session.shutdown","id":"shutdown-2","timestamp":"2026-04-16T09:00:00.000Z","data":{"totalNanoAiu":93524970000,"modelMetrics":{}}}`,
		// Shutdowns missing either total cannot be reconciled.
		`{"type":"session.shutdown","id":"without-model-total","timestamp":"2026-04-17T09:52:27.352Z","data":{"totalNanoAiu":93524970000,"modelMetrics":{"test-model":{"usage":{"inputTokens":100,"outputTokens":50}}}}}`,
		`{"type":"session.shutdown","id":"without-session-total","timestamp":"2026-04-18T09:52:27.352Z","data":{"modelMetrics":{}}}`,
	}, "\n"))
	entries, err := loadCopilot(file)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, fmt.Sprintf("%s %v %v", e.ID, e.Raw["credit_snapshot"], e.Raw["nano_aiu"]))
	}
	want := []string{
		"credits:session-1:checkpoint-1 session.usage_checkpoint 51322830000",
		"shutdown:session-1:shutdown-1:test-model <nil> 42202140000",
		"credits:session-1:shutdown-1 session.shutdown 93524970000",
		"credits:session-1:shutdown-2 session.shutdown 93524970000",
		"shutdown:session-1:without-model-total:test-model <nil> 0",
		"credits:session-1:without-model-total session.shutdown <nil>",
		"credits:session-1:without-session-total session.shutdown <nil>",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

const copilotNanoAIUPerCredit = 1_000_000_000

func copilotCheckpoint(id, timestamp string, credits int) string {
	return fmt.Sprintf(`{"type":"session.usage_checkpoint","id":%q,"timestamp":%q,"data":{"totalNanoAiu":%d}}`, id, timestamp, credits*copilotNanoAIUPerCredit)
}

// copilotCreditShutdown is a shutdown with a session total of credits and a
// test-model breakdown of 150 tokens and modelCredits.
func copilotCreditShutdown(id, timestamp string, credits, modelCredits int) string {
	return fmt.Sprintf(`{"type":"session.shutdown","id":%q,"timestamp":%q,"data":{"totalNanoAiu":%d,"modelMetrics":{"test-model":{"usage":{"inputTokens":100,"outputTokens":50},"totalNanoAiu":%d}}}}`, id, timestamp, credits*copilotNanoAIUPerCredit, modelCredits*copilotNanoAIUPerCredit)
}

func TestCopilotReportsSessionCreditsMissingFromModelMetrics(t *testing.T) {
	for _, tc := range []struct {
		name         string
		files        map[string][]string
		since, until string
		// "date model tokens cost-in-whole-credits" per entry.
		want []string
	}{
		{
			name: "credits missing from modelMetrics",
			files: map[string][]string{"session-state/session-1/events.jsonl": {
				copilotCheckpoint("checkpoint-1", "2026-01-02T10:00:00Z", 51),
				copilotCheckpoint("checkpoint-2", "2026-01-02T10:10:00Z", 73),
				copilotCreditShutdown("shutdown-1", "2026-01-02T10:20:00Z", 93, 42),
			}},
			want: []string{"2026-01-02 test-model 150 0", "2026-01-02 unknown 0 51"},
		},
		{
			name: "checkpoints of sessions without a later shutdown",
			files: map[string][]string{
				"session-state/session-1/events.jsonl": {
					copilotCreditShutdown("shutdown-1", "2026-01-02T10:00:00Z", 10, 10),
					copilotCheckpoint("checkpoint-1", "2026-01-02T11:00:00Z", 25),
					copilotCheckpoint("checkpoint-2", "2026-01-03T11:00:00Z", 40),
				},
				"session-state/session-2/events.jsonl": {copilotCheckpoint("checkpoint-3", "2026-01-03T12:00:00Z", 7)},
			},
			want: []string{"2026-01-02 test-model 150 0", "2026-01-02 unknown 0 15", "2026-01-03 unknown 0 15", "2026-01-03 unknown 0 7"},
		},
		{
			name: "resumed session whose last shutdown has no modelMetrics",
			files: map[string][]string{"session-state/session-1/events.jsonl": {
				copilotCheckpoint("checkpoint-1", "2026-01-02T10:00:00Z", 1072),
				`{"type":"session.resume","timestamp":"2026-01-03T09:00:00.000Z","data":{}}`,
				fmt.Sprintf(`{"type":"session.shutdown","id":"shutdown-1","timestamp":"2026-01-03T10:00:00.000Z","data":{"totalNanoAiu":%d,"modelMetrics":{}}}`, 1072*copilotNanoAIUPerCredit),
			}},
			want: []string{"2026-01-03 unknown 0 1072"},
		},
		{
			name: "snapshots before --since",
			files: map[string][]string{"session-state/session-1/events.jsonl": {
				copilotCreditShutdown("shutdown-1", "2026-01-02T10:00:00Z", 30, 10),
				copilotCheckpoint("checkpoint-1", "2026-01-03T10:00:00Z", 50),
			}},
			since: "2026-01-03",
			want:  []string{"2026-01-03 unknown 0 20"},
		},
		{
			name: "credits a later shutdown attributes",
			files: map[string][]string{"session-state/session-1/events.jsonl": {
				copilotCheckpoint("checkpoint-1", "2026-01-02T09:00:00Z", 30),
				copilotCreditShutdown("shutdown-1", "2026-01-02T10:00:00Z", 30, 30),
				copilotCheckpoint("checkpoint-2", "2026-01-02T11:00:00Z", 50),
				copilotCreditShutdown("shutdown-2", "2026-01-02T12:00:00Z", 50, 35),
				copilotCreditShutdown("shutdown-3", "2026-01-02T13:00:00Z", 50, 50),
			}},
			want: []string{"2026-01-02 test-model 150 0"},
		},
		{
			name: "credits a shutdown after --until attributes",
			files: map[string][]string{"session-state/session-1/events.jsonl": {
				copilotCheckpoint("checkpoint-1", "2026-01-02T09:00:00Z", 30),
				copilotCreditShutdown("shutdown-1", "2026-01-02T10:00:00Z", 30, 30),
				copilotCheckpoint("checkpoint-2", "2026-01-02T11:00:00Z", 50),
				copilotCreditShutdown("shutdown-2", "2026-01-02T12:00:00Z", 50, 35),
				copilotCreditShutdown("shutdown-3", "2026-01-03T10:00:00Z", 50, 50),
			}},
			until: "2026-01-02",
			want:  []string{"2026-01-02 test-model 150 0"},
		},
		{
			// Copilot versions without checkpoints restart both totals on resume.
			name: "sessions without checkpoints",
			files: map[string][]string{"session-state/session-1/events.jsonl": {
				copilotCreditShutdown("shutdown-1", "2026-01-02T10:00:00Z", 60, 40),
				copilotCreditShutdown("shutdown-2", "2026-01-03T10:00:00Z", 25, 25),
			}},
			want: []string{"2026-01-02 test-model 150 0"},
		},
		{
			name: "shutdowns without a model total",
			files: map[string][]string{"session-state/session-1/events.jsonl": {
				copilotCheckpoint("checkpoint-1", "2026-01-02T09:00:00Z", 20),
				fmt.Sprintf(`{"type":"session.shutdown","id":"shutdown-1","timestamp":"2026-01-02T10:00:00Z","data":{"totalNanoAiu":%d,"modelMetrics":{"test-model":{"usage":{"inputTokens":100,"outputTokens":50}}}}}`, 93*copilotNanoAIUPerCredit),
				copilotCheckpoint("checkpoint-2", "2026-01-02T11:00:00Z", 120),
			}},
			want: []string{"2026-01-02 test-model 150 0"},
		},
		{
			name: "sessions with OTel rows",
			files: map[string][]string{
				"session-state/session-1/events.jsonl": {copilotCheckpoint("checkpoint-1", "2026-01-02T10:00:00Z", 20)},
				"otel/otel.jsonl":                      {copilotCall("span-1", "2026-01-02T10:00:00Z")},
			},
			want: []string{"2026-01-02 test-model 2 0"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for path, lines := range tc.files {
				write(t, filepath.Join(dir, path), strings.Join(lines, "\n"))
			}
			var until time.Time
			if tc.until != "" {
				until, _ = time.Parse("2006-01-02", tc.until)
				until = until.AddDate(0, 0, 1)
			}
			entries, err := LoadUntil(context.Background(), "copilot", []string{dir}, time.UTC, until)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, e := range entries {
				// The report drops rows outside --since and --until.
				if tc.since != "" && e.DateKey < tc.since || tc.until != "" && e.DateKey > tc.until {
					continue
				}
				got = append(got, fmt.Sprintf("%s %s %d %d", e.DateKey, e.Model, e.TotalTokens, int(math.Round(e.Cost*100))))
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
