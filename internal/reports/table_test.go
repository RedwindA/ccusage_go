package reports

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

func TestTableMatchesCCUsageSnapshot(t *testing.T) {
	// Reference: ccusage's copilot_cli__focused_daily_table.snap. Keep the
	// expected output independent of this renderer to catch visual regressions.
	rows := []*Row{
		{Period: "2026-01-02", Models: []string{"claude-opus-4.6"}, Input: 70, Output: 50, CacheCreate: 20, CacheRead: 10, Total: 150},
		{Period: "2026-01-03", Models: []string{"claude-opus-4.6"}, Input: 93, Output: 44, CacheCreate: 10, CacheRead: 10, Total: 157},
		{Period: "2026-02-01", Models: []string{"gpt-5.4"}, Input: 65, Output: 30, CacheCreate: 15, CacheRead: 10, Total: 120},
		{Period: "2026-02-02", Models: []string{"gpt-5.4"}, Input: 7, Output: 8, CacheCreate: 0, CacheRead: 0, Total: 15},
	}
	var out bytes.Buffer
	if err := WriteTable(&out, io.Discard, rows, Options{Kind: "daily", Agent: "copilot", TerminalWidth: 120}); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/ccusage_copilot_daily.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()[strings.Index(out.String(), "┌"):]
	if got != string(want) {
		t.Fatalf("table differs from ccusage snapshot:\n%s\nexpected:\n%s", got, want)
	}
}

func TestCodexTableMatchesCCUsageSnapshot(t *testing.T) {
	// Reference: ccusage codex daily --no-cost at 120 columns. Codex tables
	// show reasoning even when it is zero, and split dates that would fit.
	rows := []*Row{
		{Period: "2026-10-20", Models: []string{"gpt-5"}, Input: 1000, Output: 300, Reasoning: 70, CacheRead: 200, Total: 1500},
		{Period: "2026-10-21", Models: []string{"gpt-5"}, Input: 500, Output: 40, Total: 540},
	}
	var out bytes.Buffer
	if err := WriteTable(&out, io.Discard, rows, Options{Kind: "daily", Agent: "codex", NoCost: true, TerminalWidth: 120}); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/ccusage_codex_daily.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()[strings.Index(out.String(), "┌"):]
	if got != string(want) {
		t.Fatalf("table differs from ccusage snapshot:\n%s\nexpected:\n%s", got, want)
	}
}

func TestNarrowTableMatchesCCUsageSnapshot(t *testing.T) {
	table := terminalTable{
		headers:     []string{"Date", "Models", "Input", "Output", "Cost (USD)"},
		textColumns: 2, width: 56, compactDates: true,
		rows: []terminalRow{{cells: []string{"2026-05-18", "- claude-sonnet-4-20250514\n- unusually-long-model-name-without-breaks", "123,456,789", "9,876,543", "$12345.67"}}},
	}
	want, err := os.ReadFile("testdata/ccusage_narrow.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := table.render(); got != string(want) {
		t.Fatalf("narrow table differs from ccusage snapshot:\n%s\nexpected:\n%s", got, want)
	}
}

func TestTableWidthsAndUnicode(t *testing.T) {
	rows := []*Row{{Period: "2026-09-25", Models: []string{"供应商/模型👩‍💻é超长名称-without-spaces", "gpt-5.4"}, Input: 12345, Output: 98765, CacheRead: 123456789, Total: 123567899, Cost: 1234.56}}
	for _, width := range []int{60, 80, 100, 120, 180} {
		for _, color := range []bool{false, true} {
			var out bytes.Buffer
			o := Options{Kind: "daily", Agent: "claude", TerminalWidth: width, Compact: width < 100, Color: color}
			if err := WriteTable(&out, io.Discard, rows, o); err != nil {
				t.Fatal(err)
			}
			result := out.String()
			if !utf8.ValidString(result) {
				t.Fatal("invalid UTF-8")
			}
			table := false
			tableWidth := 0
			for _, line := range strings.Split(strings.TrimSuffix(result, "\n"), "\n") {
				visible := ansi.StringWidth(line)
				if visible > width {
					t.Fatalf("line width %d exceeds %d: %s", visible, width, line)
				}
				if strings.HasPrefix(line, "┌") {
					table, tableWidth = true, visible
				}
				if table && visible != tableWidth {
					t.Fatalf("misaligned borders at width %d: %s", width, line)
				}
			}
			if !strings.Contains(result, "12,345") || !strings.Contains(result, "$1234.56") {
				t.Fatalf("numbers lost at width %d: %s", width, result)
			}
			if color && (!strings.Contains(result, "\x1b[34mDate") || !strings.Contains(result, "\x1b[33mTotal")) {
				t.Fatal("missing header/total styles")
			}
		}
	}
}

func TestNonDateReportsUseAvailableIdentifierWidth(t *testing.T) {
	for _, kind := range []string{"session", "workspace", "model", "blocks"} {
		t.Run(kind, func(t *testing.T) {
			rows := []*Row{
				{Period: "/workspace/example-alpha/" + strings.Repeat("subdirectory/", 5), Models: []string{"gpt-5.4"}, Input: 1234, Total: 1234},
				{Period: "/workspace/example-beta/" + strings.Repeat("subdirectory/", 5), Models: []string{"gpt-5.4"}, Input: 5678, Total: 5678},
			}
			var out bytes.Buffer
			if err := WriteTable(&out, io.Discard, rows, Options{Kind: kind, Agent: "claude", TerminalWidth: 160}); err != nil {
				t.Fatal(err)
			}
			for _, identifier := range []string{"/workspace/example-alpha/", "/workspace/example-beta/"} {
				if !strings.Contains(out.String(), identifier) {
					t.Fatalf("identifier was shortened despite available space: %s", out.String())
				}
			}
			for _, line := range strings.Split(out.String(), "\n") {
				if ansi.StringWidth(line) > 160 {
					t.Fatalf("table exceeds terminal width: %s", line)
				}
			}
		})
	}
}

func TestTableBreakdownsDoNotDoubleTotals(t *testing.T) {
	model := Model{Name: "claude-sonnet-4-6", Input: 1234, Output: 50, Total: 1284, Cost: 1.25}
	row := &Row{Period: "2026-09-25", Agent: "all", Input: 1234, Output: 50, Total: 1284, Cost: 1.25, Models: []string{model.Name}, Breakdowns: []Model{model}, Agents: []*Row{{Agent: "claude", Models: []string{model.Name}, Input: 1234, Output: 50, Total: 1284, Cost: 1.25, Breakdowns: []Model{model}}}}
	var out bytes.Buffer
	if err := WriteTable(&out, io.Discard, []*Row{row}, Options{Kind: "daily", ByAgent: true, Breakdown: true, Color: true, TerminalWidth: 180, DetectedAgents: []string{"claude", "codex"}}); err != nil {
		t.Fatal(err)
	}
	result := out.String()
	for _, want := range []string{"Detected: Claude, Codex", "- Claude", "└─ sonnet-4-6", "\x1b[90m", "\x1b[33m1,234", "\x1b[33m$1.25"} {
		if !strings.Contains(result, want) {
			t.Errorf("missing %q: %s", want, result)
		}
	}
	if strings.Count(result, "└─ sonnet-4-6") != 1 {
		t.Fatal("duplicated model breakdown")
	}
}

func TestFocusedBreakdownLabelsRemainDistinct(t *testing.T) {
	row := &Row{Period: "2026-09-25", Models: []string{"claude-opus-4-6", "claude-opus-4-6-fast"}, Input: 3000, Output: 300, Total: 3300, Cost: 3,
		Breakdowns: []Model{
			{Name: "claude-opus-4-6", Input: 1000, Output: 100, Total: 1100, Cost: 1},
			{Name: "claude-opus-4-6-fast", Input: 2000, Output: 200, Total: 2200, Cost: 2},
		},
	}
	var out bytes.Buffer
	if err := WriteTable(&out, io.Discard, []*Row{row}, Options{Kind: "daily", Agent: "claude", Breakdown: true, TerminalWidth: 120}); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"└─ opus-4-6", "└─ opus-4-6-fast"} {
		if !strings.Contains(out.String(), model) {
			t.Fatalf("focused model identity lost: %s", out.String())
		}
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "│") && strings.Contains(line, "└─") {
			cells := strings.Split(line, "│")
			if strings.TrimSpace(cells[1]) != "" || !strings.Contains(cells[2], "└─ opus-4-6") {
				t.Fatalf("breakdown must use Models column: %s", line)
			}
		}
	}
}

func TestModelNamesOnlyStripActualDateSuffixes(t *testing.T) {
	for input, want := range map[string]string{
		"kimi-k2-thinking":                   "kimi-k2-thinking",
		"kimi-k2":                            "kimi-k2",
		"claude-opus-4-20250514":             "opus-4",
		"anthropic/claude-sonnet-4-20250514": "sonnet-4",
		"provider-model-12345678":            "provider-model-12345678",
	} {
		if got := shortTableModel(input); got != want {
			t.Errorf("shortTableModel(%q) = %q; want %q", input, got, want)
		}
	}
	if got := tableModels([]string{"kimi-k2-thinking", "kimi-k2"}); got != "- kimi-k2\n- kimi-k2-thinking" {
		t.Fatalf("distinct models were collapsed: %s", got)
	}
}

func TestTableOptionsAndUntrustedText(t *testing.T) {
	row := &Row{Period: "会话\n\x1b[31m", Agent: "codebuff", User: "用户\tA", Project: "项目\r名称", Models: []string{"claude-opus-4-20250514", "claude-opus-4", "bad\x1b[2Jmodel"}, Input: 1000, Total: 2000, Cost: 1, Credits: 2.5}
	for _, compact := range []bool{false, true} {
		var out bytes.Buffer
		err := WriteTable(&out, io.Discard, []*Row{row}, Options{Kind: "session", Compact: compact, NoCost: true, Instances: true, TerminalWidth: 240})
		if err != nil {
			t.Fatal(err)
		}
		result := out.String()
		if strings.ContainsAny(result, "\x1b\t\r") || strings.Contains(result, "Cost") || strings.Contains(result, "$") {
			t.Fatalf("unsafe text or costs leaked: %s", result)
		}
		if strings.Count(result, "- opus-4") != 1 {
			t.Fatal("display model names must be deduplicated")
		}
		for _, header := range []string{"User", "Project", "Agent", "Credits"} {
			if !strings.Contains(result, header) {
				t.Errorf("missing %s", header)
			}
		}
		if strings.Contains(result, "Cache Create") == compact || strings.Contains(result, "Total Tokens") == compact {
			t.Fatal("incorrect compact columns")
		}
	}
}

type failingTableWriter struct{ err error }

func (w failingTableWriter) Write([]byte) (int, error) { return 0, w.err }

func TestTableEmptyAndWriteError(t *testing.T) {
	// ccusage reports an empty table on stderr, after the title of a unified
	// report, and names some sources in the message.
	for _, tc := range []struct {
		o              Options
		stdout, stderr string
	}{
		{Options{Kind: "daily", DetectedAgents: []string{"claude"}}, "\n╭────────────────────────────────────────────╮\n│                                            │\n│  Coding (Agent) CLI Usage Report - Daily   │\n│              Detected: Claude              │\n│                                            │\n╰────────────────────────────────────────────╯\n\n", "No usage data found.\n"},
		{Options{Kind: "daily", Agent: "claude"}, "", "No usage data found.\n"},
		{Options{Kind: "session", Agent: "codex"}, "", "No Codex usage data found.\n"},
		{Options{Kind: "daily", Agent: "copilot"}, "", "No GitHub Copilot CLI usage data found.\nSession-state events are read from ${COPILOT_HOME:-~/.copilot}/session-state/<session-id>/events.jsonl; OpenTelemetry file export remains supported.\nSee https://ccusage.com/guide/copilot/#data-source\n"},
	} {
		var out, diagnostics bytes.Buffer
		if err := WriteTable(&out, &diagnostics, nil, tc.o); err != nil || out.String() != tc.stdout || diagnostics.String() != tc.stderr {
			t.Errorf("empty %s %s report: %v stdout=%q stderr=%q", tc.o.Agent, tc.o.Kind, err, out.String(), diagnostics.String())
		}
	}
	want := errors.New("closed output")
	if err := WriteTable(failingTableWriter{want}, io.Discard, []*Row{{Period: "today"}}, Options{Kind: "daily"}); !errors.Is(err, want) {
		t.Fatalf("write error lost: %v", err)
	}
}
