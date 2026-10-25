package loader

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RedwindA/ccusage_go/internal/types"
)

func TestDebugParseErrorUsesEncodingJSONText(t *testing.T) {
	for _, line := range []string{`{"type":`, `{"a":1}x`, `{"a":tru}`, `[1]`, `{"a":1e999}`} {
		path := filepath.Join(t.TempDir(), "s.jsonl")
		if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		l := New()
		l.SetDebug(true)
		parsed := l.parseFile(path)
		if parsed.err != nil {
			t.Fatal(parsed.err)
		}
		var raw map[string]interface{}
		want := fmt.Sprintf("Line 1: JSON parse error: %v", json.Unmarshal([]byte(line), &raw))
		if msg := parsed.entryDebugMsg; !strings.Contains(msg, want) {
			t.Fatalf("%s: debug message %q does not contain %q", line, msg, want)
		}
	}
}

// Entry loads count an AgentProgress line as a parse error; daily loads read
// the record it wraps, so their first error is a later line.
func TestDebugParseErrorsCountAgentProgressLinesOnlyOutsideDailyLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{
		`{"type":"progress","timestamp":"2026-09-12T04:00:01.500Z","sessionId":"s","data":{"message":{"timestamp":"2026-09-12T04:00:01.000Z","requestId":"req_1","message":{"id":"msg_1","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":2}}}}}`,
		`{"type":"assistant","timestamp":"2026-09-12T04:00:02.000Z","sessionId":"s","message":{"id":"msg_2"}}`,
		`{"type":`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		daily bool
		want  string
	}{
		{false, "Debug: File s.jsonl had 3 parse errors\n  First error: Line 1: Entry parse error: missing required message object\n"},
		{true, "Debug: File s.jsonl had 2 parse errors\n  First error: Line 2: Entry parse error: missing required message.usage object\n"},
	} {
		l := New()
		l.SetDebug(true)
		if tc.daily {
			l.SetDailyLoader(func(e *types.UsageEntry) float64 { return e.Cost })
		}
		if got := captureStderr(t, func() { _, _ = l.LoadParallel(context.Background(), []string{path}) }); got != tc.want {
			t.Errorf("daily %v: debug output %q, want %q", tc.daily, got, tc.want)
		}
	}
}
