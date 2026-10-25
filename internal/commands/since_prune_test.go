package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pruneFixture isolates the source homes and points Claude at a fresh root.
func pruneFixture(t *testing.T) string {
	t.Helper()
	isolateSourceHomes(t)
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	t.Setenv("NO_COLOR", "1")
	return root
}

func pruneWrite(t *testing.T, root, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(root, name)
	sourceCLIWrite(t, path, strings.Join(lines, "\n"))
	return path
}

func pruneTouch(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// pruneUsageLine writes a Claude usage line; an empty request leaves out the
// request ID.
func pruneUsageLine(timestamp, session, message, request string, cost float64) string {
	line := fmt.Sprintf(`{"timestamp":%q,"sessionId":%q,"requestId":%q,"costUSD":%v,"message":{"id":%q,"model":"claude-sonnet-4-20250514","usage":{"input_tokens":10,"output_tokens":2}}}`, timestamp, session, request, cost, message)
	if request == "" {
		line = strings.Replace(line, `"requestId":"",`, "", 1)
	}
	return line
}

func pruneSidechainLine(timestamp, session, message, request string, cost float64) string {
	return strings.Replace(pruneUsageLine(timestamp, session, message, request, cost), "{", `{"isSidechain":true,`, 1)
}

func pruneRun(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	cmd := NewRootCommand("test")
	var out, diagnostics bytes.Buffer
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostics)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, diagnostics.String())
	}
	return out.String()
}

func pruneTotalCost(t *testing.T, output string) float64 {
	t.Helper()
	var payload struct {
		Totals struct {
			Cost float64 `json:"totalCost"`
		} `json:"totals"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, output)
	}
	return payload.Totals.Cost
}

// pruneSectionCost sums the cost of a unified report section's rows.
func pruneSectionCost(t *testing.T, output, section string) float64 {
	t.Helper()
	var payload map[string]json.RawMessage
	var rows []struct {
		Cost float64 `json:"totalCost"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, output)
	}
	if err := json.Unmarshal(payload[section], &rows); err != nil {
		t.Fatalf("no %s section: %v\n%s", section, err, output)
	}
	cost := 0.0
	for _, row := range rows {
		cost += row.Cost
	}
	return cost
}

func pruneTime(value string) time.Time {
	at, _ := time.Parse(time.RFC3339, value)
	return at
}

// 2026-09-12T12:00:00Z, later than every fixture entry below.
var pruneFreshMtime = pruneTime("2026-09-12T12:00:00Z")

// assertOutputIgnoresMtimes runs args with the fixture's own mtimes and
// again with every file touched to fresh: mtime pruning must never change a
// report.
func assertOutputIgnoresMtimes(t *testing.T, files []string, fresh time.Time, stdin string, normalize func(string) string, args ...string) string {
	t.Helper()
	stale := normalize(pruneRun(t, stdin, args...))
	for _, file := range files {
		pruneTouch(t, file, fresh)
	}
	got := normalize(pruneRun(t, stdin, args...))
	if stale != got {
		t.Fatalf("%v depends on file mtimes:\n%s\nwith fresh mtimes:\n%s", args, stale, got)
	}
	return got
}

func keepOutput(output string) string { return output }

var boundedArgs = []string{"--since", "20260910", "--timezone", "UTC", "--mode", "display", "--offline"}

// A file last written before the window cannot hold usage inside it, so
// date-bounded reports do not read it. This fixture breaks that rule on
// purpose: only the reports that must read history before --since count the
// misdated file.
func TestDateBoundedReportsSkipFilesLastWrittenBeforeTheWindow(t *testing.T) {
	root := pruneFixture(t)
	fresh := pruneWrite(t, root, "projects/project-a/fresh.jsonl", pruneUsageLine("2026-09-11T12:00:00.000Z", "fresh", "msg-fresh", "req-fresh", 1))
	misdated := pruneWrite(t, root, "projects/project-a/misdated.jsonl", pruneUsageLine("2026-09-11T13:00:00.000Z", "misdated", "msg-misdated", "req-misdated", 2))
	pruneTouch(t, fresh, pruneTime("2026-09-11T12:00:00Z"))
	pruneTouch(t, misdated, pruneTime("2026-09-01T12:00:00Z"))

	for _, tc := range []struct {
		args []string
		want float64
	}{
		{[]string{"claude", "daily"}, 1},
		{[]string{"claude", "weekly"}, 1},
		{[]string{"claude", "monthly"}, 1},
		{[]string{"claude", "session"}, 1},
		{[]string{"claude", "model"}, 1},
		{[]string{"daily"}, 1},
		{[]string{"monthly", "--sections", "weekly"}, 1},
		{[]string{"daily", "--sections", "session"}, 1},
		// Unified sessions are dated by their latest activity and totaled
		// whole, and blocks anchor on earlier entries.
		{[]string{"session"}, 3},
		{[]string{"session", "--sections", "daily"}, 3},
		{[]string{"claude", "blocks"}, 3},
	} {
		args := append(append(tc.args, boundedArgs...), "--json")
		if got := pruneTotalCost(t, pruneRun(t, "", args...)); got != tc.want {
			t.Errorf("%v: totalCost = %v, want %v", tc.args, got, tc.want)
		}
	}
	// A session section reads the misdated file; the sections beside it
	// still skip it.
	for _, tc := range []struct {
		args     []string
		sections map[string]float64
	}{
		{[]string{"daily", "--sections", "session"}, map[string]float64{"daily": 1, "session": 3}},
		{[]string{"monthly", "--sections", "weekly,session"}, map[string]float64{"monthly": 1, "weekly": 1, "session": 3}},
		{[]string{"session", "--sections", "daily"}, map[string]float64{"session": 3, "daily": 1}},
	} {
		output := pruneRun(t, "", append(append(tc.args, boundedArgs...), "--json")...)
		for section, want := range tc.sections {
			if got := pruneSectionCost(t, output, section); got != want {
				t.Errorf("%v: %s cost = %v, want %v", tc.args, section, got, want)
			}
		}
	}
	if got := pruneTotalCost(t, pruneRun(t, "", "claude", "daily", "--timezone", "UTC", "--mode", "display", "--offline", "--json")); got != 3 {
		t.Errorf("unbounded claude daily: totalCost = %v, want 3", got)
	}

	// The statusline totals the session over the whole history, but reads
	// only recent files for today and the active block, whatever the cost
	// source.
	now := time.Now()
	zone := pruneNoonZone(t, now)
	at := func(hoursAgo int) string {
		return now.Add(-time.Duration(hoursAgo) * time.Hour).UTC().Format(time.RFC3339Nano)
	}
	status := pruneFixture(t)
	transcript := pruneWrite(t, status, "projects/project-a/session-now.jsonl",
		pruneUsageLine(at(20), "session-now", "msg-earlier", "req-earlier", 4),
		pruneUsageLine(at(1), "session-now", "msg-now", "req-now", 1))
	misdated = pruneWrite(t, status, "projects/project-a/misdated.jsonl", pruneUsageLine(at(2), "misdated", "msg-misdated", "req-misdated", 2))
	pruneTouch(t, misdated, now.AddDate(0, 0, -40))
	hook, noCost := pruneStatuslineHook(transcript, true), pruneStatuslineHook(transcript, false)
	for _, tc := range []struct{ source, hook, want string }{
		{"cc", hook, "💰 $0.00 session / $1.00 today / $1.00 block"},
		{"auto", hook, "💰 $0.00 session / $1.00 today / $1.00 block"},
		{"ccusage", hook, "💰 $5.00 session / $1.00 today / $1.00 block"},
		{"both", hook, "💰 ($0.00 cc / $5.00 ccusage) session / $1.00 today / $1.00 block"},
		{"auto", noCost, "💰 $5.00 session / $1.00 today / $1.00 block"},
	} {
		if output := pruneRun(t, tc.hook, "statusline", "--offline", "--no-cache", "--cost-source", tc.source, "--timezone", zone); !strings.Contains(output, tc.want) {
			t.Errorf("statusline --cost-source %s: %s, want %s", tc.source, output, tc.want)
		}
	}
}

// pruneNoonZone names a fixed-offset zone where now falls around noon, so
// entries a few hours old are dated today and those 20 hours old yesterday.
func pruneNoonZone(t *testing.T, now time.Time) string {
	t.Helper()
	zone := fmt.Sprintf("Etc/GMT%+d", now.UTC().Hour()-12)
	if _, err := time.LoadLocation(zone); err != nil {
		t.Skip(err)
	}
	return zone
}

// pruneStatuslineHook is a statusline hook for session-now; withCost adds a
// hook cost of zero.
func pruneStatuslineHook(transcript string, withCost bool) string {
	cost := ""
	if withCost {
		cost = `"cost":{"total_cost_usd":0},`
	}
	return fmt.Sprintf(`{"session_id":"session-now","transcript_path":%q,"model":{"display_name":"Model"},%s"context_window":{"total_input_tokens":1,"context_window_size":100}}`, transcript, cost)
}

// Today's cost reads the files a load bounded to today keeps, so a file last
// written over a day before today counts toward the block, whose window
// reaches back further, but not toward today.
func TestStatuslineTodaySkipsFilesLastWrittenADayBeforeToday(t *testing.T) {
	root := pruneFixture(t)
	now := time.Now()
	zone := pruneNoonZone(t, now)
	loc, _ := time.LoadLocation(zone)
	at := func(hoursAgo int) string {
		return now.Add(-time.Duration(hoursAgo) * time.Hour).UTC().Format(time.RFC3339Nano)
	}
	transcript := pruneWrite(t, root, "projects/project-a/session-now.jsonl",
		pruneUsageLine(at(20), "session-now", "msg-earlier", "req-earlier", 4),
		pruneUsageLine(at(1), "session-now", "msg-now", "req-now", 1))
	edge := pruneWrite(t, root, "projects/project-a/edge.jsonl", pruneUsageLine(at(2), "edge", "msg-edge", "req-edge", 2))
	y, m, d := now.In(loc).Date()
	pruneTouch(t, edge, time.Date(y, m, d, 0, 0, 0, 0, loc).Add(-30*time.Hour))
	hook := pruneStatuslineHook(transcript, true)
	for _, source := range []string{"cc", "auto", "ccusage", "both"} {
		if output := pruneRun(t, hook, "statusline", "--offline", "--no-cache", "--cost-source", source, "--timezone", zone); !strings.Contains(output, "$1.00 today / $3.00 block") {
			t.Errorf("statusline --cost-source %s: %s, want $1.00 today / $3.00 block", source, output)
		}
	}
}

func TestClaudeDailySinceTotalsMatchAfterPruningStaleFiles(t *testing.T) {
	root := pruneFixture(t)
	entry := func(timestamp, id string, cost float64) string {
		return pruneUsageLine(timestamp, id, "message-"+id, "request-"+id, cost)
	}
	// Written long before --since: holds only pre-window history.
	stale := pruneWrite(t, root, "projects/project-a/stale.jsonl", entry("2026-09-01T12:00:00.000Z", "stale", 4))
	// Its mtime is inside the 24 hour margin, so it is still read even though
	// its entry was flushed after it happened.
	margin := pruneWrite(t, root, "projects/project-a/margin.jsonl", entry("2026-09-10T08:00:00.000Z", "margin", 2))
	fresh := pruneWrite(t, root, "projects/project-a/fresh.jsonl", entry("2026-09-09T23:00:00.000Z", "fresh-before", 8), entry("2026-09-11T12:00:00.000Z", "fresh", 1))
	pruneTouch(t, stale, pruneTime("2026-09-01T12:00:00Z"))
	pruneTouch(t, margin, pruneTime("2026-09-09T06:00:00Z"))
	pruneTouch(t, fresh, pruneTime("2026-09-11T12:00:00Z"))

	var report struct {
		Daily []struct {
			Date string  `json:"date"`
			Cost float64 `json:"totalCost"`
		} `json:"daily"`
		Totals struct {
			Cost   float64 `json:"totalCost"`
			Tokens int     `json:"totalTokens"`
		} `json:"totals"`
	}
	output := pruneRun(t, "", append(append([]string{"claude", "daily"}, boundedArgs...), "--json")...)
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, output)
	}
	if len(report.Daily) != 2 || report.Daily[0].Date != "2026-09-10" || report.Daily[0].Cost != 2 || report.Daily[1].Date != "2026-09-11" || report.Daily[1].Cost != 1 || report.Totals.Cost != 3 || report.Totals.Tokens != 24 {
		t.Fatalf("unexpected daily report: %s", output)
	}

	table := pruneRun(t, "", append([]string{"claude", "daily"}, boundedArgs...)...)
	for _, want := range []string{"2026-09-10", "2026-09-11", "$3.00"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %s:\n%s", want, table)
		}
	}
	for _, unwanted := range []string{"2026-09-01", "2026-09-09"} {
		if strings.Contains(table, unwanted) {
			t.Errorf("table shows %s:\n%s", unwanted, table)
		}
	}
}

// Replays that carry a new request ID only match a parent at its own
// timestamp, so the replays below are requestless to match across the window.

func TestBoundedReportsDedupeSidechainReplaysOfParentsInStaleFiles(t *testing.T) {
	root := pruneFixture(t)
	parent := pruneWrite(t, root, "projects/project-a/session-a.jsonl", pruneUsageLine("2026-09-01T12:00:00.000Z", "session-a", "msg-parent", "", 1))
	sidechain := pruneWrite(t, root, "projects/project-a/session-a/subagents/agent-a.jsonl",
		pruneSidechainLine("2026-09-11T12:00:00.000Z", "session-a", "msg-parent", "", 2),
		pruneSidechainLine("2026-09-11T12:05:00.000Z", "session-a", "msg-answer", "req-answer", 1))
	for _, report := range []string{"daily", "weekly", "monthly", "session"} {
		pruneTouch(t, parent, pruneTime("2026-09-01T12:00:00Z"))
		pruneTouch(t, sidechain, pruneTime("2026-09-11T12:05:00Z"))
		output := assertOutputIgnoresMtimes(t, []string{parent, sidechain}, pruneFreshMtime, "", keepOutput, append(append([]string{"claude", report}, boundedArgs...), "--json")...)
		if got := pruneTotalCost(t, output); got != 1 {
			t.Errorf("claude %s: totalCost = %v, want 1", report, got)
		}
	}
}

func TestBoundedUnifiedSessionsKeepOldSubagentFiles(t *testing.T) {
	root := pruneFixture(t)
	parent := pruneWrite(t, root, "projects/project-a/session-b.jsonl", pruneUsageLine("2026-09-11T12:00:00.000Z", "session-b", "msg-recent", "req-recent", 1))
	subagent := pruneWrite(t, root, "projects/project-a/session-b/subagents/agent-b.jsonl", pruneSidechainLine("2026-09-01T12:00:00.000Z", "session-b", "msg-old", "req-old", 2))
	pruneTouch(t, parent, pruneTime("2026-09-11T12:00:00Z"))
	pruneTouch(t, subagent, pruneTime("2026-09-01T12:00:00Z"))

	output := assertOutputIgnoresMtimes(t, []string{parent, subagent}, pruneFreshMtime, "", keepOutput, append(append([]string{"session"}, boundedArgs...), "--json")...)
	if got := pruneTotalCost(t, output); got != 3 {
		t.Fatalf("totalCost = %v, want 3\n%s", got, output)
	}
}

// writeBlockChain writes a continuous chain of usage entries 4 hours apart,
// never idle long enough to reset a 5 hour block. Files pair entries so they
// straddle block boundaries: dropping whole early files re-anchors every
// later block.
func writeBlockChain(t *testing.T, root string, last time.Time, entries int) []string {
	t.Helper()
	timestamps := make([]time.Time, entries)
	for i := range timestamps {
		timestamps[i] = last.Add(-time.Duration(entries-1-i) * 4 * time.Hour)
	}
	// The earliest entry opens block 0 alone in its file; later files each
	// hold the second entry of one block and the first entry of the next.
	chunks := [][]time.Time{timestamps[:1]}
	for i := 1; i < len(timestamps); i += 2 {
		chunks = append(chunks, timestamps[i:min(i+2, len(timestamps))])
	}
	var files []string
	for i, chunk := range chunks {
		var lines []string
		for _, at := range chunk {
			ms := fmt.Sprint(at.UnixMilli())
			lines = append(lines, pruneUsageLine(at.UTC().Format("2006-01-02T15:04:05.000Z"), "session-chain", "msg-"+ms, "req-"+ms, 1))
		}
		file := pruneWrite(t, root, fmt.Sprintf("projects/chain/part-%03d.jsonl", i), lines...)
		pruneTouch(t, file, chunk[len(chunk)-1])
		files = append(files, file)
	}
	return files
}

func TestBoundedBlocksKeepAnchorsFromOlderFiles(t *testing.T) {
	root := pruneFixture(t)
	// Ends 2026-09-11T21:00:00Z; 71 entries reach back roughly 12 days.
	files := writeBlockChain(t, root, pruneTime("2026-09-11T21:00:00Z"), 71)

	assertOutputIgnoresMtimes(t, files, pruneFreshMtime, "", keepOutput, "claude", "blocks", "--since", "20260911", "--timezone", "UTC", "--mode", "display", "--offline", "--json")
}

// statuslineBlockSegment keeps only the block cost; the remaining time moves
// with the clock.
func statuslineBlockSegment(output string) string {
	for _, segment := range strings.Split(output, " / ") {
		if strings.Contains(segment, "block") {
			return strings.SplitN(segment, " (", 2)[0]
		}
	}
	return output
}

func TestStatuslineActiveBlockKeepsAnchorsFromOlderFiles(t *testing.T) {
	root := pruneFixture(t)
	now := time.Now()
	files := writeBlockChain(t, root, now.Add(-time.Hour), 71)
	hook := fmt.Sprintf(`{"session_id":"session-chain","transcript_path":%q,"model":{"display_name":"Model"},"cost":{"total_cost_usd":0},"context_window":{"total_input_tokens":1,"context_window_size":100}}`, files[len(files)-1])

	block := assertOutputIgnoresMtimes(t, files, now, hook, statuslineBlockSegment, "statusline", "--offline", "--no-cache", "--cost-source", "cc", "--timezone", "UTC")
	if block != "$1.00 block" {
		t.Fatalf("block = %q", block)
	}
}

// Entries dated before a statusline window may miss the sessions it skipped,
// so they must not anchor its blocks: session-b sits between session-a's
// pause and its chain, so dropping it would pair the chain's entries anew.
func TestStatuslineAnchorsBlocksOnlyOnEntriesInsideItsWindow(t *testing.T) {
	root := pruneFixture(t)
	now := time.Now()
	since, _ := time.Parse("2006-01-02", statuslineBlockSince(now, time.UTC, 1))
	stamp := func(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }
	// A chain 4 hours apart from an hour ago back to before the window.
	var chainTimes []time.Time
	for at := now.Add(-time.Hour); ; at = at.Add(-4 * time.Hour) {
		chainTimes = append([]time.Time{at}, chainTimes...)
		if !at.After(since.Add(-26 * time.Hour)) {
			break
		}
	}
	// Written more than a day before the window, so its first load skips it.
	other := chainTimes[0].Add(-3 * time.Hour)
	lines := []string{pruneUsageLine(stamp(other.Add(-10*time.Hour)), "session-a", "msg-pause", "req-pause", 1)}
	for i, at := range chainTimes {
		lines = append(lines, pruneUsageLine(stamp(at), "session-a", fmt.Sprintf("msg-a%d", i), fmt.Sprintf("req-a%d", i), 1))
	}
	chain := pruneWrite(t, root, "projects/project-a/session-a.jsonl", lines...)
	between := pruneWrite(t, root, "projects/project-a/session-b.jsonl", pruneUsageLine(stamp(other), "session-b", "msg-b", "req-b", 1))
	pruneTouch(t, between, other)
	hook := fmt.Sprintf(`{"session_id":"session-a","transcript_path":%q,"model":{"display_name":"Model"},"cost":{"total_cost_usd":0},"context_window":{"total_input_tokens":1,"context_window_size":100}}`, chain)

	assertOutputIgnoresMtimes(t, []string{chain, between}, now, hook, statuslineBlockSegment, "statusline", "--offline", "--no-cache", "--cost-source", "cc", "--timezone", "UTC")
}

func TestBoundedUnifiedReportsStillDetectClaudeWithOnlyStaleFiles(t *testing.T) {
	root := pruneFixture(t)
	old := pruneWrite(t, root, "projects/project-a/old.jsonl", pruneUsageLine("2026-09-01T12:00:00.000Z", "session-old", "msg-old", "req-old", 1))
	pruneTouch(t, old, pruneTime("2026-09-01T12:00:00Z"))
	// Another source fills the window, so the report shows its title.
	codex := filepath.Join(t.TempDir(), "codex")
	t.Setenv("CODEX_HOME", codex)
	sourceCLIWrite(t, filepath.Join(codex, "sessions/2026/09/11/rollout.jsonl"), sourceCodexRollout("2026-09-11T12:00:00Z", 100, 10))

	output := assertOutputIgnoresMtimes(t, []string{old}, pruneFreshMtime, "", keepOutput, append([]string{"daily"}, boundedArgs...)...)
	if !strings.Contains(output, "Detected: Claude, Codex") {
		t.Fatal(output)
	}
}

// Like upstream, loads skip unreadable files, so a bounded one must not fail
// when the only files it keeps are unreadable but a skipped one has usage.
func TestBoundedReportsTolerateUnreadableKeptFiles(t *testing.T) {
	root := pruneFixture(t)
	old := pruneWrite(t, root, "projects/project-a/old.jsonl", pruneUsageLine("2026-09-01T12:00:00.000Z", "session-old", "msg-old", "req-old", 1))
	pruneTouch(t, old, pruneTime("2026-09-01T12:00:00Z"))
	unreadable := pruneWrite(t, root, "projects/project-a/unreadable.jsonl", pruneUsageLine("2026-09-11T12:00:00.000Z", "session-new", "msg-new", "req-new", 1))
	pruneTouch(t, unreadable, pruneFreshMtime)
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(unreadable); err == nil {
		t.Skip("file permissions do not keep this user from reading, as for root or on Windows")
	}
	codex := filepath.Join(t.TempDir(), "codex")
	t.Setenv("CODEX_HOME", codex)
	sourceCLIWrite(t, filepath.Join(codex, "sessions/2026/09/11/rollout.jsonl"), sourceCodexRollout("2026-09-11T12:00:00Z", 100, 10))

	if got := pruneTotalCost(t, pruneRun(t, "", append(append([]string{"claude", "daily"}, boundedArgs...), "--json")...)); got != 0 {
		t.Fatalf("totalCost = %v, want 0", got)
	}

	cmd := NewRootCommand("test")
	var out, diagnostics bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostics)
	cmd.SetArgs(append([]string{"daily"}, boundedArgs...))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Detected: Claude, Codex") || diagnostics.Len() > 0 {
		t.Fatalf("%s\nstderr:\n%s", out.String(), diagnostics.String())
	}
}

func TestStatuslineKeepsTheCurrentBlockBeforeFutureDatedEntries(t *testing.T) {
	root := pruneFixture(t)
	now := time.Now()
	at := func(hours int) string {
		return now.Add(time.Duration(hours) * time.Hour).UTC().Format(time.RFC3339Nano)
	}
	// A skewed clock wrote an entry eight hours ahead, after a long pause.
	transcript := pruneWrite(t, root, "projects/project-a/session-now.jsonl",
		pruneUsageLine(at(-1), "session-now", "msg-now", "req-now", 1),
		pruneUsageLine(at(8), "session-now", "msg-future", "req-future", 9))
	hook := fmt.Sprintf(`{"session_id":"session-now","transcript_path":%q,"model":{"display_name":"Model"},"cost":{"total_cost_usd":0},"context_window":{"total_input_tokens":1,"context_window_size":100}}`, transcript)

	if output := pruneRun(t, hook, "statusline", "--offline", "--no-cache", "--cost-source", "cc", "--timezone", "UTC"); !strings.Contains(output, "$1.00 block") {
		t.Fatal(output)
	}
}

func TestBoundedReportsDedupeReplaysFiledUnderAnotherSessionDirectory(t *testing.T) {
	root := pruneFixture(t)
	parent := pruneWrite(t, root, "projects/project-a/session-a.jsonl", pruneUsageLine("2026-09-01T12:00:00.000Z", "session-a", "msg-parent", "", 1))
	// Workflow transcripts can live under one session and record another.
	workflow := pruneWrite(t, root, "projects/project-a/session-b/subagents/workflows/wf_1/agent-a.jsonl",
		pruneSidechainLine("2026-09-11T12:00:00.000Z", "session-a", "msg-parent", "", 2),
		pruneSidechainLine("2026-09-11T12:05:00.000Z", "session-a", "msg-answer", "req-answer", 1))
	pruneTouch(t, parent, pruneTime("2026-09-01T12:00:00Z"))
	pruneTouch(t, workflow, pruneTime("2026-09-11T12:05:00Z"))

	output := assertOutputIgnoresMtimes(t, []string{parent, workflow}, pruneFreshMtime, "", keepOutput, append(append([]string{"claude", "daily"}, boundedArgs...), "--json")...)
	if got := pruneTotalCost(t, output); got != 1 {
		t.Fatalf("totalCost = %v, want 1\n%s", got, output)
	}
}

// Claude's own --since from the config bounds the files a unified report
// skips, not the unified one.
func TestBoundedUnifiedReportsSkipFilesByTheClaudeWindow(t *testing.T) {
	root := pruneFixture(t)
	early := pruneWrite(t, root, "projects/project-a/early.jsonl", pruneUsageLine("2026-09-05T12:00:00.000Z", "early", "msg-early", "req-early", 2))
	late := pruneWrite(t, root, "projects/project-a/late.jsonl", pruneUsageLine("2026-09-11T12:00:00.000Z", "late", "msg-late", "req-late", 1))
	pruneTouch(t, early, pruneTime("2026-09-05T12:00:00Z"))
	pruneTouch(t, late, pruneTime("2026-09-11T12:00:00Z"))
	cfg := filepath.Join(t.TempDir(), "ccusage.json")
	sourceCLIWrite(t, cfg, `{"defaults":{"since":"20260910"},"claude":{"defaults":{"since":"20260901"}}}`)

	output := assertOutputIgnoresMtimes(t, []string{early, late}, pruneFreshMtime, "", keepOutput, "daily", "--config", cfg, "--timezone", "UTC", "--mode", "display", "--offline", "--json")
	if got := pruneTotalCost(t, output); got != 3 {
		t.Fatalf("totalCost = %v, want 3\n%s", got, output)
	}
}

func TestStatuslineBlockSinceReachesBehindTheSessionWindow(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skip(err)
	}
	// 2026-05-22T01:27:00+09:00
	now := time.UnixMilli(1_779_380_820_000)

	if got := statuslineBlockSince(now, tokyo, 1); got != "2026-05-20" {
		t.Errorf("1 day: %s", got)
	}
	if got := statuslineBlockSince(now, tokyo, 7); got != "2026-05-14" {
		t.Errorf("7 days: %s", got)
	}
}

func TestStatuslineBlockAnchorFollowsTheLatestLongPause(t *testing.T) {
	now := time.UnixMilli(1_779_380_820_000)
	at := func(hoursAgo ...int) []time.Time {
		var out []time.Time
		for _, h := range hoursAgo {
			out = append(out, now.Add(-time.Duration(h)*time.Hour))
		}
		return out
	}
	session := 5 * time.Hour
	for _, tc := range []struct {
		name       string
		timestamps []time.Time
		anchor     blockAnchor
		start      time.Time
	}{
		{"empty window", nil, noActiveBlock, time.Time{}},
		{"idle for a session", at(30, 6), noActiveBlock, time.Time{}},
		// Only the latest pause longer than a session counts.
		{"latest pause", at(40, 30, 20, 18, 1), anchoredBlock, at(1)[0]},
		{"chain after a pause", at(30, 20, 16, 12, 8, 4), anchoredBlock, at(20)[0]},
		// A pause of exactly one session keeps the chain going.
		{"pause of one session", at(9, 4, 1), needsMoreHistory, time.Time{}},
		// A gap before a future-dated entry must not hide the current block.
		{"future-dated entry", at(1, -8), needsFullHistory, time.Time{}},
	} {
		anchor, start := statuslineBlockAnchor(tc.timestamps, now, session)
		if anchor != tc.anchor || !start.Equal(tc.start) {
			t.Errorf("%s: got %v %v, want %v %v", tc.name, anchor, start, tc.anchor, tc.start)
		}
	}
}
