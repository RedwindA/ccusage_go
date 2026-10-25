package sourcesb

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
		entries, err := loadCopilot(file)
		if err != nil || len(entries) != 1 {
			t.Fatalf("%s: %+v %v", file, entries, err)
		}
		e := entries[0]
		splits = append(splits, [4]int{e.InputTokens, e.CacheReadInputTokens, e.CacheCreationInputTokens, e.OutputTokens})
	}
	if want := [4]int{10, 12000, 7990, 300}; splits[0] != want || splits[1] != want {
		t.Fatalf("got %v, want %v for both", splits, want)
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
