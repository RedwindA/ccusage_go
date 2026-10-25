package sourcesa

import (
	"fmt"
	"strings"
	"testing"

	"github.com/RedwindA/ccusage_go/internal/types"
)

// codexCompactionLog is a compaction request Codex recorded apart from its
// token counts, followed by the marker that pairs it.
func codexCompactionLog(ts, model, id string, input int) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"turn_context","payload":{"model":%q}}
{"timestamp":%[1]q,"type":"token_usage_record","payload":{"response_id":%[3]q,"usage":{"input_tokens":%[4]d,"output_tokens":20}}}
{"type":"compacted","payload":{"compaction_response_id":%[3]q}}`, ts, model, id, input)
}
func responseID(e types.UsageEntry) string { id, _ := e.Raw["response_id"].(string); return id }
func codexTotals(entries []types.UsageEntry) (total, compactions int) {
	for _, e := range entries {
		total += e.TotalTokens
		if responseID(e) != "" {
			compactions++
		}
	}
	return total, compactions
}

func TestCodexCountsOnlyUsageRecordsPairedWithCompactions(t *testing.T) {
	root := t.TempDir()
	totals := `{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":80,"output_tokens":20,"reasoning_output_tokens":0,"total_tokens":120}}}}`
	record := `{"timestamp":%q,"type":"token_usage_record","payload":{"response_id":%q,"usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":20,"reasoning_output_tokens":0,"total_tokens":%d}}}`
	marker := `{"timestamp":%q,"type":"compacted","payload":{"compaction_response_id":%q}}`
	fixture(t, root, "sessions/session.jsonl", strings.Join([]string{
		`{"timestamp":"2026-09-01T00:00:00.000Z","type":"turn_context","payload":{"model":"gpt-reserve"}}`,
		fmt.Sprintf(totals, "2026-09-01T00:00:01.000Z"),
		fmt.Sprintf(record, "2026-09-01T00:00:02.000Z", "compact-before", 300, 280, 320),
		fmt.Sprintf(marker, "2026-09-01T00:00:03.000Z", "compact-before"),
		fmt.Sprintf(marker, "2026-09-01T00:00:04.000Z", "compact-before"),
		fmt.Sprintf(marker, "2026-09-01T00:00:05.000Z", "compact-after"),
		fmt.Sprintf(record, "2026-09-01T00:00:06.000Z", "compact-after", 200, 180, 220),
		fmt.Sprintf(record, "2026-09-01T00:00:07.000Z", "not-a-compaction", 500, 480, 520),
		fmt.Sprintf(totals, "2026-09-01T00:00:08.000Z"),
	}, "\n"))
	entries := loadTest(t, "codex", root)
	if len(entries) != 3 {
		t.Fatal(entries)
	}
	requireTokens(t, entries[1], 20, 20, 280, 0, 320)
	requireTokens(t, entries[2], 20, 20, 180, 0, 220)
	if e := entries[1]; e.Model != "gpt-reserve" || e.Raw["is_fallback"] != true || responseID(e) != "compact-before" {
		t.Fatalf("%+v", e)
	}
}

func TestCodexUsageRecordModelsKeepTheTurnModel(t *testing.T) {
	for marker, want := range map[string]int{"response-1": 2, "different-response": 1} {
		root := t.TempDir()
		fixture(t, root, "sessions/session.jsonl", `{"type":"turn_context","payload":{"model":"gpt-5"}}
{"timestamp":"2026-09-01T00:00:01Z","type":"token_usage_record","payload":{"response_id":"response-1","model":"gpt-5-mini","usage":{"input_tokens":300,"output_tokens":30}}}
{"type":"compacted","payload":{"compaction_response_id":"`+marker+`"}}
{"timestamp":"2026-09-01T00:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":20}}}}`)
		entries := loadTest(t, "codex", root)
		if len(entries) != want {
			t.Fatal(marker, entries)
		}
		if e := entries[0]; want == 2 && (e.Model != "gpt-5-mini" || e.Raw["is_fallback"] != false) {
			t.Fatalf("%+v", e)
		}
		if e := entries[len(entries)-1]; e.Model != "gpt-5" || e.Raw["is_fallback"] != false {
			t.Fatalf("%s: %+v", marker, e)
		}
	}
}

func TestCodexReadsCompactionLinesWithJSONWhitespace(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "sessions/session.jsonl", `{"type": "turn_context", "payload": {"model": "gpt-5-mini"}}
{"timestamp": "2026-09-01T00:00:01Z", "type": "event_msg", "payload": {"type": "token_count", "info": {"total_token_usage": {"input_tokens": 100, "output_tokens": 20}}}}
{ "timestamp" : "2026-09-01T00:00:02Z", "type" : "token_usage_record", "payload" : { "type" : "request", "response_id" : "r", "usage" : { "input_tokens" : 300, "output_tokens" : 30 } } }
{ "type" : "compacted", "payload" : { "type" : "summary", "compaction_response_id" : "r" } }`)
	entries := loadTest(t, "codex", root)
	if len(entries) != 2 || entries[0].TotalTokens != 120 || entries[1].TotalTokens != 330 || entries[1].Model != "gpt-5-mini" {
		t.Fatal(entries)
	}
}

func TestCodexCompactionUsageKeepsCumulativeBaseline(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "sessions/session.jsonl", `{"type":"turn_context","payload":{"model":"gpt-5"}}
{"timestamp":"2026-09-01T00:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":20}}}}
{"timestamp":"2026-09-01T00:00:02Z","type":"token_usage_record","payload":{"response_id":"compaction-1","usage":{"input_tokens":300,"cached_input_tokens":200,"output_tokens":30}}}
{"timestamp":"2026-09-01T00:00:03Z","type":"compacted","payload":{"compaction_response_id":"compaction-1"}}
{"timestamp":"2026-09-01T00:00:04Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150,"output_tokens":30}}}}`)
	entries := loadTest(t, "codex", root)
	if len(entries) != 3 || entries[0].TotalTokens != 120 || entries[2].TotalTokens != 60 {
		t.Fatal(entries)
	}
	requireTokens(t, entries[1], 100, 30, 200, 0, 330)
	if e := entries[1]; e.Model != "gpt-5" || e.Raw["is_fallback"] != true {
		t.Fatalf("%+v", e)
	}
}

// Local compaction advances the cumulative counts between the usage record
// and its marker, so the record is already counted; only the latest valid
// record can be the one a snapshot covers.
func TestCodexCumulativeCountsCoverLocalCompaction(t *testing.T) {
	old := `{"timestamp":"2026-09-01T00:00:01Z","type":"token_usage_record","payload":{"response_id":"old-response","usage":{"input_tokens":100,"output_tokens":20}}}`
	gap := func(line string) string {
		return strings.Join([]string{`{"type":"turn_context","payload":{"model":"gpt-5"}}`, old,
			`{"timestamp":"2026-09-01T00:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":20}}}}`,
			`{"type":"compacted","payload":{"compaction_response_id":"old-response"}}`,
			`{"timestamp":"2026-09-01T00:00:03Z","type":"token_usage_record","payload":{"response_id":"new-response","usage":{"input_tokens":300,"output_tokens":30}}}`,
			line,
			`{"timestamp":"2026-09-01T00:00:05Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":400,"output_tokens":50}}}}`,
			`{"type":"compacted","payload":{"compaction_response_id":"new-response"}}`}, "\n")
	}
	thread := `{"timestamp":"2026-09-01T00:00:01Z","type":"token_usage_record","payload":{"response_id":"response-1","usage":{"input_tokens":300,"output_tokens":30},"thread_token_usage":{"input_tokens":400,"output_tokens":50}}}
{"timestamp":"2026-09-01T00:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":400,"output_tokens":50}}}}
{"timestamp":"2026-09-01T00:00:03Z","type":"compacted","payload":{"compaction_response_id":"response-1"}}`
	for _, tc := range []struct {
		name, log          string
		total, compactions int
	}{
		{"local", `{"type":"turn_context","payload":{"model":"gpt-5"}}
{"timestamp":"2026-09-01T00:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":20}}}}
{"timestamp":"2026-09-01T00:00:02Z","type":"token_usage_record","payload":{"response_id":"response-1","usage":{"input_tokens":300,"output_tokens":30}}}
{"timestamp":"2026-09-01T00:00:03Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":400,"output_tokens":50},"last_token_usage":{"input_tokens":300,"output_tokens":30}}}}
{"timestamp":"2026-09-01T00:00:04Z","type":"compacted","payload":{"compaction_response_id":"response-1"}}
{"timestamp":"2026-09-01T00:00:05Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":450,"output_tokens":60}}}}`, 510, 0},
		{"thread totals", thread, 450, 0},
		{"no thread totals", strings.Replace(thread, `,"thread_token_usage":{"input_tokens":400,"output_tokens":50}`, "", 1), 780, 1},
		{"blank ID gap", gap(`{"type":"token_usage_record","payload":{"response_id":" "}}`), 450, 0},
		{"undated gap", gap(`{"type":"token_usage_record","payload":{"response_id":"invalid-response","usage":{"input_tokens":1}}}`), 450, 0},
		{"empty usage gap", gap(`{"timestamp":"2026-09-01T00:00:04Z","type":"token_usage_record","payload":{"response_id":"empty-usage","usage":{}}}`), 450, 0},
		{"counted record gap", gap(old), 450, 0},
		{"unrelated record gap", gap(`{"timestamp":"2026-09-01T00:00:04Z","type":"token_usage_record","payload":{"response_id":"other","usage":{"input_tokens":5}}}`), 780, 1},
	} {
		root := t.TempDir()
		fixture(t, root, "sessions/session.jsonl", tc.log)
		if total, compactions := codexTotals(loadTest(t, "codex", root)); total != tc.total || compactions != tc.compactions {
			t.Errorf("%s: total=%d compactions=%d", tc.name, total, compactions)
		}
	}
}

func TestCodexIgnoresUnpairedOrInvalidCompactionRecords(t *testing.T) {
	record := `{"timestamp":"2026-09-01T00:00:01Z","type":"token_usage_record","payload":{"response_id":"response-1","usage":{"input_tokens":300,"output_tokens":20}}}`
	marker := `{"type":"compacted","payload":{"compaction_response_id":"response-1"}}`
	for _, tc := range []struct {
		log   string
		total int
	}{
		{marker + "\n" + record, 320},
		{record, 0},
		{marker + "\n" + strings.Replace(record, `"2026-09-01T00:00:01Z"`, "null", 1), 0},
		{marker + "\n" + strings.Replace(record, `"response-1"`, `" "`, 1), 0},
		{marker + "\n" + strings.Replace(record, `"input_tokens":300,"output_tokens":20`, `"input_tokens":0,"output_tokens":0`, 1), 0},
		{marker + "\n" + strings.Replace(record, `{"input_tokens":300,"output_tokens":20}`, "null", 1), 0},
		{marker, 0},
	} {
		root := t.TempDir()
		fixture(t, root, "sessions/session.jsonl", tc.log)
		if total, _ := codexTotals(loadTest(t, "codex", root)); total != tc.total {
			t.Errorf("%s: total=%d", tc.log, total)
		}
	}
}

func TestCodexCompactionsDoNotConsumeReplayPrefix(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "a-parent.jsonl", `{"timestamp":"2026-09-01T00:00:00Z","type":"session_meta","payload":{"id":"parent"}}`+"\n"+codexUsageLog("2026-09-01T00:00:01Z", 100))
	fixture(t, root, "b-child.jsonl", `{"timestamp":"2026-09-01T00:00:30Z","type":"session_meta","payload":{"id":"child","forked_from_id":"parent"}}`+"\n"+codexCompactionLog("2026-09-01T00:00:31Z", "gpt-5", "response-1", 100)+"\n"+codexUsageLog("2026-09-01T00:00:32Z", 100)+"\n"+codexUsageLog("2026-09-01T00:01:00Z", 50))
	var child []types.UsageEntry
	for _, e := range loadTest(t, "codex", root) {
		if e.SessionID == "b-child" {
			child = append(child, e)
		}
	}
	if len(child) != 2 || responseID(child[0]) != "response-1" || child[0].TotalTokens != 120 || child[1].TotalTokens != 70 {
		t.Fatal(child)
	}
}

func TestCodexParentCompactionsDoNotChangeReplayPrefix(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "a-parent.jsonl", `{"type":"session_meta","payload":{"id":"parent"}}`+"\n"+codexCompactionLog("2026-09-01T00:00:01Z", "gpt-5", "response-1", 300)+"\n"+codexUsageLog("2026-09-01T00:00:02Z", 100))
	fixture(t, root, "b-child.jsonl", `{"timestamp":"2026-09-01T00:01:00Z","type":"session_meta","payload":{"id":"child","forked_from_id":"parent"}}`+"\n"+codexUsageLog("2026-09-01T00:01:00Z", 100)+"\n"+codexUsageLog("2026-09-01T00:02:00Z", 50))
	if total, bySession := sumTokens(t, root); total != 510 || bySession["b-child"] != 70 {
		t.Fatal(total, bySession)
	}
}

// A fork replays its parent's compaction records. Reports from the fork's day
// count only its own requests, whichever copy is discovered first and even
// when the parent's cumulative counts covered the request.
func TestCodexForkSkipsReplayedParentCompactions(t *testing.T) {
	meta := `{"type":"session_meta","payload":{"id":"parent"}}`
	fork := `{"timestamp":"2026-09-02T00:00:00Z","type":"session_meta","payload":{"id":"child","forked_from_id":"parent"}}`
	for _, tc := range []struct {
		name, parent, child string
		total               int
	}{
		{"emitted", meta + "\n" + codexCompactionLog("2026-09-01T00:00:01Z", "gpt-5", "response-1", 300) + "\n" + codexUsageLog("2026-09-01T00:00:02Z", 100),
			strings.Join([]string{fork, codexCompactionLog("2026-09-02T00:00:01Z", "gpt-5", "response-1", 300), codexUsageLog("2026-09-02T00:00:02Z", 100),
				codexCompactionLog("2026-09-02T00:01:00Z", "gpt-5", "child-response", 50), codexUsageLog("2026-09-02T00:02:00Z", 20)}, "\n"), 110},
		{"covered", meta + `
{"type":"turn_context","payload":{"model":"gpt-5"}}
{"timestamp":"2026-09-01T00:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":20}}}}
{"timestamp":"2026-09-01T00:00:02Z","type":"token_usage_record","payload":{"response_id":"response-1","usage":{"input_tokens":300,"output_tokens":20}}}
{"timestamp":"2026-09-01T00:00:03Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":400,"output_tokens":40},"last_token_usage":{"input_tokens":300,"output_tokens":20}}}}
{"type":"compacted","payload":{"compaction_response_id":"response-1"}}`,
			strings.Join([]string{fork, codexUsageLog("2026-09-02T00:00:01Z", 100), codexCompactionLog("2026-09-02T00:00:02Z", "gpt-5", "response-1", 300),
				codexCompactionLog("2026-09-02T00:01:00Z", "gpt-5", "child-response", 50), codexUsageLog("2026-09-02T00:02:00Z", 30)}, "\n"), 120},
	} {
		for _, parentPath := range []string{"sessions/2026/09/01/parent.jsonl", "archived_sessions/parent.jsonl"} {
			root := t.TempDir()
			fixture(t, root, parentPath, tc.parent)
			fixture(t, root, "sessions/2026/09/02/child.jsonl", tc.child)
			var day []types.UsageEntry
			for _, e := range loadTest(t, "codex", root) {
				if e.DateKey >= "2026-09-02" {
					day = append(day, e)
				}
			}
			if total, _ := codexTotals(day); total != tc.total || responseID(day[0]) != "child-response" {
				t.Errorf("%s with %s: %+v", tc.name, parentPath, day)
			}
		}
	}
}

func TestCodexKeepsFirstCompactionCopy(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "sessions/a-first.jsonl", codexCompactionLog("2026-09-01T00:00:01Z", "gpt-5", "response-1", 300))
	fixture(t, root, "sessions/b-copy.jsonl", `{"timestamp":"2026-09-02T00:00:00Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"service_tier":"priority"}}}`+"\n"+codexCompactionLog("2026-09-02T00:00:01Z", "gpt-5-mini", "response-1", 500))
	entries := loadTest(t, "codex", root)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	requireTokens(t, entries[0], 300, 20, 0, 0, 320)
	if e := entries[0]; e.SessionID != "a-first" || e.Model != "gpt-5" || e.DateKey != "2026-09-01" || e.Speed != "fast" {
		t.Fatalf("%+v", e)
	}
}
