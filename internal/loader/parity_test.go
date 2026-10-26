package loader

import (
	"context"
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

func nativeRecord(session, message, request string, input int, side bool) map[string]interface{} {
	r := map[string]interface{}{"type": "assistant", "timestamp": "2026-09-12T04:38:42.296Z", "sessionId": session, "isSidechain": side, "message": map[string]interface{}{"id": message, "model": "claude-sonnet-4-5", "usage": map[string]interface{}{"input_tokens": input, "output_tokens": 2}}}
	if request != "" {
		r["requestId"] = request
	}
	return r
}
func writeNative(t *testing.T, root, name string, records ...map[string]interface{}) string {
	t.Helper()
	var lines []string
	for _, r := range records {
		b, e := json.Marshal(r)
		if e != nil {
			t.Fatal(e)
		}
		lines = append(lines, string(b))
	}
	p := filepath.Join(root, name)
	if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0644); e != nil {
		t.Fatal(e)
	}
	return p
}
func mustNative(t *testing.T, paths ...string) []types.UsageEntry {
	t.Helper()
	l := New()
	l.SetMaxWorkers(4)
	entries, err := l.LoadParallel(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
func TestRicherUsageReplacesStreamingSnapshotAcrossFiles(t *testing.T) {
	root := t.TempDir()
	a := writeNative(t, root, "projects/p/a.jsonl", nativeRecord("s", "m", "r", 1, false))
	b := writeNative(t, root, "projects/p/b.jsonl", nativeRecord("s", "m", "r", 100, false))
	entries := mustNative(t, b, a)
	if len(entries) != 1 || entries[0].InputTokens != 100 {
		t.Fatal(entries)
	}
}
func TestClaudeSidechainReplayAndSessionAliases(t *testing.T) {
	root := t.TempDir()
	a := writeNative(t, root, "projects/p/a.jsonl", nativeRecord("a", "m", "r", 1, false))
	b := writeNative(t, root, "projects/p/b.jsonl", nativeRecord("b", "m", "r", 10, false))
	c := writeNative(t, root, "projects/p/c.jsonl", nativeRecord("a", "m", "replay", 100, true))
	entries := mustNative(t, c, b, a)
	if len(entries) != 1 || entries[0].InputTokens != 10 || entries[0].IsSidechain {
		t.Fatal(entries)
	}
}
func TestClaudeRealCallsSameMessageDifferentRequestsRemain(t *testing.T) {
	root := t.TempDir()
	p := writeNative(t, root, "projects/p/s.jsonl", nativeRecord("s", "m", "r1", 10, false), nativeRecord("s", "m", "r2", 20, false))
	entries := mustNative(t, p)
	if len(entries) != 2 {
		t.Fatal(entries)
	}
}
func TestClaudeRequestlessDedupeIsSessionScoped(t *testing.T) {
	root := t.TempDir()
	a := writeNative(t, root, "projects/p/a.jsonl", nativeRecord("a", "m", "", 10, false), nativeRecord("a", "m", "", 20, false))
	b := writeNative(t, root, "projects/p/b.jsonl", nativeRecord("b", "m", "", 30, false))
	entries := mustNative(t, a, b)
	if len(entries) != 2 || entries[0].InputTokens+entries[1].InputTokens != 50 {
		t.Fatal(entries)
	}
}
func requestlessRecord(timestamp, session, message string, output int, side bool) map[string]interface{} {
	r := nativeRecord(session, message, "", 100, side)
	r["timestamp"] = timestamp
	r["message"].(map[string]interface{})["usage"].(map[string]interface{})["output_tokens"] = output
	return r
}

// Gateways can reuse one message ID for every response, so only repeated
// writes at one timestamp collapse, unless the ID is a real Anthropic or
// OpenAI Responses ID, whose content blocks are written at distinct timestamps.
func TestClaudeRequestlessDedupeIsTimestampScoped(t *testing.T) {
	for _, tc := range []struct {
		message, second string
		entries, output int
		cost            float64
	}{
		{"gateway_123", "2025-01-10T10:00:01.000Z", 2, 275, 0.011},
		{"gateway_123", "2025-01-10T10:00:00.000Z", 1, 250, 0.01},
		{"msg_123", "2025-01-10T10:00:01.000Z", 1, 250, 0.01},
		{"resp_0123456789abcdef", "2025-01-10T10:00:00.500Z", 1, 250, 0.01},
	} {
		first := requestlessRecord("2025-01-10T10:00:00.000Z", "s", tc.message, 25, false)
		first["costUSD"] = 0.001
		second := requestlessRecord(tc.second, "s", tc.message, 250, false)
		second["costUSD"] = 0.01
		second["message"].(map[string]interface{})["usage"].(map[string]interface{})["speed"] = "standard"
		entries := mustNative(t, writeNative(t, t.TempDir(), "projects/p/s.jsonl", first, second))
		output, cost := 0, 0.0
		for _, e := range entries {
			output += e.OutputTokens
			cost += e.Cost
		}
		if len(entries) != tc.entries || output != tc.output || cost != tc.cost {
			t.Errorf("%s then %s: %d entries, %d output tokens, cost %v", tc.message, tc.second, len(entries), output, cost)
		}
	}
}

// A requestless replay matches its parent across timestamps, but its own
// timestamp must not then pull a later response at that instant into the
// parent: the two parents are separate gateway calls.
func TestClaudeRequestlessReplayDoesNotMergeParents(t *testing.T) {
	root := t.TempDir()
	replay := writeNative(t, root, "projects/p/a-side.jsonl", requestlessRecord("2026-09-11T12:05:00.000Z", "s", "g", 2, true))
	parents := writeNative(t, root, "projects/p/b-main.jsonl",
		requestlessRecord("2026-09-11T12:00:00.000Z", "s", "g", 2, false),
		requestlessRecord("2026-09-11T12:05:00.000Z", "s", "g", 1000, false))
	entries := mustNative(t, replay, parents)
	if len(entries) != 2 || entries[0].OutputTokens != 2 || entries[1].OutputTokens != 1000 || entries[0].IsSidechain || entries[1].IsSidechain {
		t.Fatal(entries)
	}
}

// A replaced survivor leaves its old exact key in the index. Upstream
// re-checks the entry now in that slot, so a later entry under the old key
// is matched like any other new entry instead of merging into the survivor.
func TestClaudeExactDedupeMatchesOnlyTheCurrentSurvivor(t *testing.T) {
	later := nativeRecord("s2", "m", "r2", 100, true)
	later["timestamp"] = "2026-09-12T04:48:42.296Z"
	for _, tc := range []struct {
		name    string
		records []map[string]interface{}
		total   int
	}{
		// The parent replaces the sidechain, whose (m, r2) key stays behind.
		{"request", []map[string]interface{}{
			nativeRecord("s1", "m", "r2", 1, true),
			nativeRecord("s1", "m", "r1", 10, false),
			later,
		}, 12 + 102},
		// The requestless sidechain's key stays behind the same way.
		{"requestless", []map[string]interface{}{
			requestlessRecord("2026-09-12T04:38:42.296Z", "s", "g", 1, true),
			nativeRecord("s", "g", "r1", 10, false),
			requestlessRecord("2026-09-12T04:38:42.296Z", "s", "g", 100, false),
		}, 12 + 200},
	} {
		root := t.TempDir()
		var paths []string
		for i, r := range tc.records {
			paths = append(paths, writeNative(t, root, fmt.Sprintf("projects/p/%c.jsonl", 'a'+i), r))
		}
		entries := mustNative(t, paths...)
		total := 0
		for _, e := range entries {
			total += e.TotalTokens
		}
		if len(entries) != 2 || total != tc.total || entries[0].IsSidechain || entries[0].Raw["request_id"] != "r1" {
			t.Errorf("%s: %d entries, %d tokens: %+v", tc.name, len(entries), total, entries)
		}
	}
}

// A replay route entry follows the session of the entry now in its slot, so
// once a copy from another session wins, the slot is only found again under
// that session's alias, which is added after the routes it was already on.
func TestClaudeReplayRoutesFollowTheSurvivorsSession(t *testing.T) {
	record := func(timestamp, session, request string, input int, side bool) map[string]interface{} {
		r := nativeRecord(session, "g", request, input, side)
		r["timestamp"] = timestamp
		r["message"].(map[string]interface{})["usage"].(map[string]interface{})["output_tokens"] = 1
		return r
	}
	root := t.TempDir()
	entries := mustNative(t,
		writeNative(t, root, "projects/p/a.jsonl", record("2026-09-11T12:00:00.000Z", "s1", "r1", 10, false)),
		writeNative(t, root, "projects/p/b.jsonl", record("2026-09-11T12:05:00.000Z", "s1", "", 20, true)),
		// The s2 copy of r1 replaces the s1 parent.
		writeNative(t, root, "projects/p/c.jsonl", record("2026-09-11T12:00:00.000Z", "s2", "r1", 100, false)),
		// This s1 replay now skips the parent's slot and replaces the
		// requestless sidechain instead.
		writeNative(t, root, "projects/p/d.jsonl", record("2026-09-11T12:00:00.000Z", "s1", "", 1000, true)))
	total := 0
	for _, e := range entries {
		total += e.TotalTokens
	}
	if len(entries) != 2 || total != 101+1001 {
		t.Fatalf("%d entries, %d tokens: %+v", len(entries), total, entries)
	}
}

// Upstream dedupes files in path order and each file line by line, so a tie
// keeps the first candidate in that order rather than the earliest one, and
// which candidate a replay matches depends on what came before it.
func TestClaudeDedupeFollowsPathAndLineOrder(t *testing.T) {
	late := requestlessRecord("2026-09-12T00:00:00.100Z", "s1", "g", 2, true)
	early := requestlessRecord("2026-09-11T11:00:00.000Z", "s1", "g", 2, true)
	root := t.TempDir()
	for name, entries := range map[string][]types.UsageEntry{
		// "s1.jsonl" sorts before "s1/subagents/agent-a.jsonl".
		"files": mustNative(t,
			writeNative(t, root, "projects/p/s1/subagents/agent-a.jsonl", early),
			writeNative(t, root, "projects/p/s1.jsonl", late)),
		"lines": mustNative(t, writeNative(t, t.TempDir(), "projects/p/s1.jsonl", late, early)),
	} {
		if len(entries) != 1 || !entries[0].Timestamp.Equal(time.Date(2026, 9, 12, 0, 0, 0, 100e6, time.UTC)) {
			t.Errorf("%s: want the first tied copy to survive, got %+v", name, entries)
		}
	}

	// In path order the s2 replay meets the s3 copy of r2 on their exact
	// key, so it never reaches the requestless sidechain on the replay route
	// and both responses remain.
	root = t.TempDir()
	sidechain := requestlessRecord("2026-09-11T11:00:00.000Z", "s2", "g", 2, true)
	answer := nativeRecord("s3", "g", "r2", 100, true)
	answer["timestamp"] = "2026-09-12T00:00:00.100Z"
	replay := nativeRecord("s2", "g", "r2", 100, true)
	replay["timestamp"] = "2026-09-11T11:00:00.000Z"
	replay["message"].(map[string]interface{})["usage"].(map[string]interface{})["output_tokens"] = 30
	entries := mustNative(t,
		writeNative(t, root, "projects/p/b.jsonl", replay),
		writeNative(t, root, "projects/p/a.jsonl", sidechain, answer))
	if len(entries) != 2 || entries[0].TotalTokens+entries[1].TotalTokens != 102+130 {
		t.Errorf("mixed: %+v", entries)
	}
}

// A replay with a new request ID only matches its parent at the parent's
// timestamp. Deduping each file (or project) first would merge the later
// line of that replayed response into its first line, which then matches
// the parent; upstream sees every line in one pass and keeps the later one.
func TestClaudeDedupeIsOnePassOverEveryLine(t *testing.T) {
	root := t.TempDir()
	parent := nativeRecord("s", "m", "r0", 5, false)
	first := nativeRecord("s", "m", "r1", 30, true)
	later := nativeRecord("s", "m", "r1", 20, true)
	later["timestamp"] = "2026-09-12T04:38:47.296Z"
	writeNative(t, root, "projects/p1/a.jsonl", parent)
	writeNative(t, root, "projects/p2/b.jsonl", first, later)
	loaded := mustNative(t, filepath.Join(root, "projects/p1/a.jsonl"), filepath.Join(root, "projects/p2/b.jsonl"))
	cached, _, err := NewIncrementalCache().Update(New(), nil, root, 0)
	if err != nil {
		t.Fatal(err)
	}
	for name, entries := range map[string][]types.UsageEntry{"load": loaded, "cache": cached} {
		if len(entries) != 2 || entries[0].TotalTokens+entries[1].TotalTokens != 7+22 {
			t.Errorf("%s: %+v", name, entries)
		}
	}
}
func TestClaudeSpeedBreaksEqualUsageTie(t *testing.T) {
	root := t.TempDir()
	plain := nativeRecord("s", "m", "r", 10, false)
	fast := nativeRecord("s", "m", "r", 10, false)
	fast["message"].(map[string]interface{})["usage"].(map[string]interface{})["speed"] = "fast"
	p := writeNative(t, root, "projects/p/s.jsonl", plain, fast)
	entries := mustNative(t, p)
	if len(entries) != 1 || entries[0].Speed != "fast" {
		t.Fatal(entries)
	}
}

// Upstream takes Claude's speed from usage.speed alone, so a Priority Tier
// request (usage.service_tier "priority") is priced and named as standard.
func TestClaudeSpeedComesFromUsageSpeedOnly(t *testing.T) {
	record := func(request, speed, tier string) map[string]interface{} {
		r := nativeRecord("s", "msg-"+request, request, 10, false)
		usage := r["message"].(map[string]interface{})["usage"].(map[string]interface{})
		if speed != "" {
			usage["speed"] = speed
		}
		usage["service_tier"] = tier
		return r
	}
	entries := mustNative(t, writeNative(t, t.TempDir(), "projects/p/s.jsonl",
		record("a", "", "priority"), record("b", "fast", "standard"), record("c", "standard", "priority")))
	got := map[string]string{}
	for _, e := range entries {
		got[e.Raw["request_id"].(string)] = e.Speed
	}
	if want := map[string]string{"a": "", "b": "fast", "c": "standard"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("speeds %v, want %v", got, want)
	}
}

// Upstream's daily loader, which serves daily, weekly and monthly totals,
// matches a /btw sidechain replay with its parent on message and session
// alone, so a replay with a new request ID and a later timestamp merges into
// the parent. Its entry loader needs the parent's timestamp for that replay
// and keeps both.
func TestClaudeDailyDedupeMatchesSidechainReplaysAcrossTimestamps(t *testing.T) {
	record := func(timestamp, request string, cacheRead int, side bool) map[string]interface{} {
		r := nativeRecord("session-a", "msg-parent", request, 0, side)
		r["timestamp"] = timestamp
		usage := r["message"].(map[string]interface{})["usage"].(map[string]interface{})
		usage["output_tokens"] = 10
		usage["cache_read_input_tokens"] = cacheRead
		return r
	}
	p := writeNative(t, t.TempDir(), "projects/p/session-a.jsonl",
		record("2026-03-20T09:46:40.000Z", "req-parent", 20, false),
		record("2026-03-20T09:46:41.000Z", "req-sidechain-replay", 50000, true))
	daily := New()
	daily.SetDailyLoader(func(e *types.UsageEntry) float64 { return e.Cost })
	entries, err := daily.LoadParallel(context.Background(), []string{p})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Raw["request_id"] != "req-parent" || entries[0].CacheReadInputTokens != 20 {
		t.Fatalf("daily: %+v", entries)
	}
	if entries := mustNative(t, p); len(entries) != 2 {
		t.Fatalf("entries: %+v", entries)
	}
}

// Upstream's daily loader breaks a tie in usage by the higher cost before
// speed; its entry loader keeps the first of equal copies.
func TestClaudeDailyDedupeBreaksEqualUsageTieByCost(t *testing.T) {
	priced := nativeRecord("s", "msg_1", "req_1", 100, false)
	priced["costUSD"] = 0.5
	p := writeNative(t, t.TempDir(), "projects/p/s.jsonl", nativeRecord("s", "msg_1", "req_1", 100, false), priced)
	daily := New()
	daily.SetDailyLoader(func(e *types.UsageEntry) float64 { return e.Cost })
	entries, err := daily.LoadParallel(context.Background(), []string{p})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Cost != 0.5 {
		t.Fatalf("daily: %+v", entries)
	}
	if entries := mustNative(t, p); len(entries) != 1 || entries[0].HasCost {
		t.Fatalf("entries: %+v", entries)
	}
}

// Upstream's daily loader reads the assistant record an AgentProgress line
// wraps as usage of the line's session, dated and identified by the record;
// its entry loader skips the line.
func TestClaudeDailyLoaderReadsAgentProgressRecords(t *testing.T) {
	record := nativeRecord("record-session", "msg_1", "req_1", 100, true)
	record["timestamp"] = "2026-09-12T04:38:41.000Z"
	record["costUSD"] = 0.25
	p := writeNative(t, t.TempDir(), "projects/p/s.jsonl", map[string]interface{}{
		"type": "progress", "timestamp": "2026-09-12T04:38:42.296Z", "sessionId": "session-a", "isSidechain": false, "data": map[string]interface{}{"type": "agent_progress", "message": record},
	})
	daily := New()
	daily.SetDailyLoader(func(e *types.UsageEntry) float64 { return e.Cost })
	entries, err := daily.LoadParallel(context.Background(), []string{p})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("daily: %+v", entries)
	}
	e := entries[0]
	if e.RecordedSessionID != "session-a" || !e.Timestamp.Equal(time.Date(2026, 9, 12, 4, 38, 41, 0, time.UTC)) || e.ID != "msg_1" || e.Raw["request_id"] != "req_1" || !e.IsSidechain || e.Cost != 0.25 || e.TotalTokens != 102 {
		t.Fatalf("daily: %+v", e)
	}
	if entries := mustNative(t, p); len(entries) != 0 {
		t.Fatalf("entries: %+v", entries)
	}
}
func TestClaudeAdvisorCallsHaveIndependentModelCostAndIDs(t *testing.T) {
	root := t.TempDir()
	r := nativeRecord("s", "m", "r", 1, false)
	r["costUSD"] = 1.23
	u := r["message"].(map[string]interface{})["usage"].(map[string]interface{})
	u["iterations"] = []interface{}{map[string]interface{}{"type": "message", "model": nil, "input_tokens": 1, "output_tokens": 2}, map[string]interface{}{"type": "advisor_message", "model": "advisor-model", "input_tokens": 10, "output_tokens": 20, "cache_read_input_tokens": 30}}
	p := writeNative(t, root, "projects/p/s.jsonl", r, r)
	entries := mustNative(t, p)
	if len(entries) != 2 {
		t.Fatal(entries)
	}
	for _, e := range entries {
		if e.Model == "advisor-model" {
			if e.HasCost || e.Cost != 0 || e.InputTokens != 10 || e.OutputTokens != 20 || e.CacheReadInputTokens != 30 || e.ID != "m:advisor:0" {
				t.Fatal(e)
			}
		} else if !e.HasCost || e.Cost != 1.23 {
			t.Fatal(e)
		}
	}
}
func TestClaudeFileSessionAndWorkspaceFallback(t *testing.T) {
	root := t.TempDir()
	r := nativeRecord("", "m", "r", 1, false)
	delete(r, "sessionId")
	p := writeNative(t, root, "projects/p/session/subagents/worker.jsonl", map[string]interface{}{"type": "user", "cwd": "/actual/project"}, r)
	entries := mustNative(t, p)
	if len(entries) != 1 || entries[0].SessionID != "session" || entries[0].Workspace != "/actual/project" {
		t.Fatal(entries)
	}
}
func TestIncrementalCacheKeepsRequestlessGatewayResponses(t *testing.T) {
	root := t.TempDir()
	writeNative(t, root, "projects/p/s.jsonl",
		requestlessRecord("2026-09-11T12:00:00.000Z", "s", "ocgo", 2, false),
		requestlessRecord("2026-09-12T12:01:00.000Z", "s", "ocgo", 2, false),
		requestlessRecord("2026-09-13T12:02:00.000Z", "s", "ocgo", 2, false))
	entries, _, err := NewIncrementalCache().Update(New(), nil, root, 0)
	if err != nil || len(entries) != 3 {
		t.Fatal(entries, err)
	}
}
func TestIncrementalRereadReplacesChangedNativeUsage(t *testing.T) {
	root := t.TempDir()
	p := writeNative(t, root, "projects/p/session/subagents/worker.jsonl", nativeRecord("s", "m", "r", 1, false))
	cache := NewIncrementalCache()
	l := New()
	entries, changed, err := cache.Update(l, nil, root, 0)
	if err != nil || !changed || len(entries) != 1 {
		t.Fatal(entries, changed, err)
	}
	writeNative(t, root, "projects/p/session/subagents/worker.jsonl", nativeRecord("s", "m", "r", 100, false))
	now := time.Now().Add(time.Second)
	if err := os.Chtimes(p, now, now); err != nil {
		t.Fatal(err)
	}
	entries, changed, err = cache.Update(l, nil, root, 0)
	if err != nil || !changed || len(entries) != 1 || entries[0].InputTokens != 100 {
		t.Fatal(entries, changed, err)
	}
	if err := os.WriteFile(p, nil, 0644); err != nil {
		t.Fatal(err)
	}
	entries, changed, err = cache.Update(l, nil, root, 0)
	if err != nil || !changed || len(entries) != 0 {
		t.Fatal(entries, changed, err)
	}
}

// Claude Code records the session as camelCase sessionId. Resumed and forked
// transcripts also carry a snake_case session_id naming another session,
// which upstream never reads: entries keep their sessionId, or their file's
// session without one, and requestless copies dedupe within that session.
func TestClaudeSessionIgnoresSnakeCaseSessionID(t *testing.T) {
	root := t.TempDir()
	a := nativeRecord("sess-a", "msg_x", "", 1000, false)
	a["session_id"] = "same"
	b := nativeRecord("sess-b", "msg_x", "", 1000, false)
	b["session_id"] = "same"
	c := nativeRecord("", "msg_y", "req_y", 10, false)
	delete(c, "sessionId")
	c["session_id"] = "other"
	entries := mustNative(t,
		writeNative(t, root, "projects/p/sess-a.jsonl", a),
		writeNative(t, root, "projects/p/sess-b.jsonl", b),
		writeNative(t, root, "projects/p/sess-c.jsonl", c))
	sessions := map[string]int{}
	for _, e := range entries {
		sessions[e.SessionID] += e.InputTokens
	}
	if len(entries) != 3 || sessions["sess-a"] != 1000 || sessions["sess-b"] != 1000 || sessions["sess-c"] != 10 {
		t.Fatal(sessions, entries)
	}
}

// Sessions are named by the transcript's path, as upstream's
// extract_session_parts names them; workflow agents form their own session.
func TestFileSessionIDFollowsTheTranscriptPath(t *testing.T) {
	for path, want := range map[string]string{
		"/home/me/.claude/projects/project-a/session-a.jsonl":                                      "session-a",
		"/home/me/.claude/projects/project-a/session-a/chat.jsonl":                                 "session-a",
		"/home/me/.claude/projects/project-a/session-a/subagents/worker.jsonl":                     "session-a",
		"/home/me/.claude/projects/project-a/session-a/subagents/workflows/wf_1/agent-a.jsonl":     "wf_1",
		"/home/me/.claude/projects/project-a/session-a/subagents/workflows/wf_1/subagents/b.jsonl": "wf_1",
	} {
		if got := fileSessionID(path); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

// Workflow agents record their parent's sessionId but are filed under a
// directory of their own: they report as that session, and still dedupe
// within the session they record.
func TestClaudeWorkflowAgentsFormTheirOwnSession(t *testing.T) {
	root := t.TempDir()
	parent := writeNative(t, root, "projects/p/sess-x.jsonl", nativeRecord("sess-x", "msg_p", "", 1000, false))
	agent := writeNative(t, root, "projects/p/sess-x/subagents/workflows/wf_1/agent-a.jsonl",
		nativeRecord("sess-x", "msg_p", "", 1000, true), nativeRecord("sess-x", "msg_a", "req_a", 200, true))
	entries := mustNative(t, parent, agent)
	sessions := map[string]int{}
	for _, e := range entries {
		sessions[e.SessionID+"/"+e.RecordedSessionID] += e.InputTokens
	}
	if len(entries) != 2 || sessions["sess-x/sess-x"] != 1000 || sessions["wf_1/sess-x"] != 200 {
		t.Fatal(sessions)
	}
}

// Session files are named by UUID, so the file that sorts last by path can
// hold the oldest usage. Loads still return entries in time order, which the
// monitor relies on to show the newest calls under Recent Activity.
func TestLoadReturnsEntriesInTimeOrderAcrossFiles(t *testing.T) {
	root := t.TempDir()
	starts := map[string]time.Time{
		"00000000-new": time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC),
		"ffffffff-old": time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC),
	}
	for session, start := range starts {
		var records []map[string]interface{}
		for i := 0; i < 10; i++ {
			r := nativeRecord(session, fmt.Sprintf("msg_%s_%d", session, i), fmt.Sprintf("req_%s_%d", session, i), 1, false)
			r["timestamp"] = start.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
			records = append(records, r)
		}
		writeNative(t, root, "projects/p/"+session+".jsonl", records...)
	}
	entries, err := New().LoadFromPath(context.Background(), root)
	if err != nil || len(entries) != 20 {
		t.Fatal(len(entries), err)
	}
	for i, e := range entries[10:] {
		if want := starts["00000000-new"].Add(time.Duration(i) * time.Hour); !e.Timestamp.Equal(want) {
			t.Errorf("entry %d at %s, want %s", 10+i, e.Timestamp, want)
		}
	}
}

// Upstream skips a usage line that holds one of its non-nullable fields as
// null anywhere in the line (only a usage.iterations element's model may be
// null), a non-semver version, an empty session, request, message or model
// ID, or a token count that is not an unsigned integer; a line its typed
// decode rejects, for a field of another JSON type, a token count not
// written as an integer, a speed other than standard or fast, or a missing
// timestamp; a line whose timestamp it cannot parse; and a line without the
// bytes "usage":{. For an AgentProgress line, it checks the line's own
// session and its record's fields.
func TestClaudeLinesUpstreamRejectsAreSkipped(t *testing.T) {
	base := `{"type":"assistant","timestamp":"2026-09-12T04:38:42.296Z","version":"2.0.0","sessionId":"s","requestId":"r","cwd":"/w","isApiErrorMessage":false,"costUSD":0.5,"message":{"id":"m","model":"claude-sonnet-4-5","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":10,"cache_read_input_tokens":20,"speed":"standard","cache_creation":{"ephemeral_5m_input_tokens":10}}}}`
	progress := `{"type":"progress","timestamp":"2026-09-12T04:38:42.296Z","sessionId":"s","data":{"type":"agent_progress","message":{"timestamp":"2026-09-12T04:38:41.000Z","requestId":"r","message":{"id":"m","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":20}}}}}`
	iterations := `"usage":{"iterations":[{"type":"message","model":null,"input_tokens":1,"output_tokens":2}],`
	for _, tc := range []struct {
		line, old, new string
		kept           bool
	}{
		{base, "", "", true},
		{base, `"usage":{`, iterations, true},
		{base, `"stop_reason":null`, `"stop_reason":null,"content":[{"type":"text","text":"\"model\":null"}]`, true},
		{base, `"version":"2.0.0"`, `"version":"2.1.310-beta"`, true},
		{base, `"speed":"standard"`, `"speed":null`, false},
		{base, `"speed":"standard"`, `"speed" : null`, false},
		{base, `"costUSD":0.5`, `"costUSD":null`, false},
		{base, `"version":"2.0.0"`, `"version":null`, false},
		{base, `"version":"2.0.0"`, `"version":"not-semver"`, false},
		{base, `"version":"2.0.0"`, `"version":"2.0"`, false},
		{base, `"sessionId":"s"`, `"sessionId":null`, false},
		{base, `"sessionId":"s"`, `"sessionId":""`, false},
		{base, `"requestId":"r"`, `"requestId":null`, false},
		{base, `"requestId":"r"`, `"requestId":""`, false},
		{base, `"id":"m"`, `"id":null`, false},
		{base, `"id":"m"`, `"id":""`, false},
		{base, `"model":"claude-sonnet-4-5"`, `"model":null`, false},
		{base, `"model":"claude-sonnet-4-5"`, `"model":""`, false},
		{base, `"cwd":"/w"`, `"cwd":null`, false},
		{base, `"isApiErrorMessage":false`, `"isApiErrorMessage":null`, false},
		{base, `"cache_creation_input_tokens":10`, `"cache_creation_input_tokens":null`, false},
		{base, `"cache_read_input_tokens":20`, `"cache_read_input_tokens":null`, false},
		{base, `"stop_reason":null`, `"content":[{"type":"tool_use","input":{"model":null}}]`, false},
		{base, `"input_tokens":10`, `"input_tokens":-5`, false},
		{base, `"output_tokens":20`, `"output_tokens":2.5`, false},
		{base, `"cache_read_input_tokens":20`, `"cache_read_input_tokens":-1`, false},
		{base, `"ephemeral_5m_input_tokens":10`, `"ephemeral_5m_input_tokens":-10`, false},
		{base, `"speed":"standard"`, `"speed":"fast"`, true},
		{base, `"speed":"standard"`, `"speed":"priority"`, false},
		{base, `"speed":"standard"`, `"speed":"Fast"`, false},
		{base, `"speed":"standard"`, `"speed":1`, false},
		{base, `"input_tokens":10`, `"input_tokens":10.0`, false},
		{base, `"input_tokens":10`, `"input_tokens":1e1`, false},
		{base, `"input_tokens":10`, `"input_tokens":-0`, false},
		{base, `"input_tokens":10`, `"input_tokens":18446744073709551616`, false},
		{base, `"output_tokens":20`, `"output_tokens":"20"`, false},
		{base, `"cache_read_input_tokens":20`, `"cache_read_input_tokens":20.0`, false},
		{base, `"ephemeral_5m_input_tokens":10`, `"ephemeral_5m_input_tokens":1E1`, false},
		{base, `"cache_creation":{"ephemeral_5m_input_tokens":10}`, `"cache_creation":null`, true},
		{base, `"cache_creation":{"ephemeral_5m_input_tokens":10}`, `"cache_creation":5`, false},
		{base, `"version":"2.0.0"`, `"version":2`, false},
		{base, `"sessionId":"s"`, `"sessionId":5`, false},
		{base, `"requestId":"r"`, `"requestId":5`, false},
		{base, `"id":"m"`, `"id":5`, false},
		{base, `"model":"claude-sonnet-4-5"`, `"model":5`, false},
		{base, `"costUSD":0.5`, `"costUSD":1`, true},
		{base, `"costUSD":0.5`, `"costUSD":"0.5"`, false},
		{base, `"cwd":"/w"`, `"cwd":5,"isSidechain":true`, true},
		{base, `"cwd":"/w"`, `"cwd":"/w","isSidechain":"true"`, false},
		{base, `"timestamp":"2026-09-12T04:38:42.296Z"`, `"timestamp":"2026-09-12T04:38:42Z"`, true},
		{base, `"timestamp":"2026-09-12T04:38:42.296Z"`, `"timestamp":"2026-09-12T04:38:42+00:00"`, true},
		{base, `"timestamp":"2026-09-12T04:38:42.296Z"`, `"timestamp":"2026-09-12T04:38:42.296-07:00"`, true},
		{base, `"timestamp":"2026-09-12T04:38:42.296Z"`, `"timestamp":"2026-09-12T04:38:42.296+23:59"`, true},
		{base, `"timestamp":"2026-09-12T04:38:42.296Z"`, `"timestamp":"2026-09-12T04:38:42.296+24:00"`, false},
		{base, `"timestamp":"2026-09-12T04:38:42.296Z"`, `"timestamp":"2026-09-12T04:38:42.296-24:00"`, false},
		{base, `"timestamp":"2026-09-12T04:38:42.296Z"`, `"timestamp":"2026-09-12T04:38:42.296+05:60"`, false},
		{base, `"timestamp":"2026-09-12T04:38:42.296Z"`, `"timestamp":"2026-09-12T04:38:42+24:00"`, false},
		{base, `"timestamp":"2026-09-12T04:38:42.296Z"`, `"timestamp":"2026-09-12T04:38:42.12Z"`, false},
		{base, `"timestamp":"2026-09-12T04:38:42.296Z"`, `"timestamp":"2026-09-12T04:38:42.123456Z"`, false},
		{base, `"timestamp":"2026-09-12T04:38:42.296Z"`, `"timestamp":"2026-09-12T04:38:42.123456+00:00"`, false},
		{base, `"timestamp":"2026-09-12T04:38:42.296Z"`, `"timestamp":"2026-09-12T04:38:42.1234Z"`, false},
		{base, `"timestamp":"2026-09-12T04:38:42.296Z"`, `"timestamp":"2026-09-12 04:38:42.296Z"`, false},
		{base, `"timestamp":"2026-09-12T04:38:42.296Z"`, `"timestamp":1789000000`, false},
		{base, `"timestamp":"2026-09-12T04:38:42.296Z",`, ``, false},
		{base, `"usage":{`, `"usage": {`, false},
		{base, `"usage":{`, `"usage" :{`, false},
		{progress, "", "", true},
		{progress, `"usage":{`, iterations, true},
		{progress, `"sessionId":"s"`, `"sessionId":""`, false},
		{progress, `"requestId":"r"`, `"requestId":""`, false},
		{progress, `"model":"claude-sonnet-4-5"`, `"model":""`, false},
		{progress, `"input_tokens":10`, `"input_tokens":-5`, false},
		{progress, `"timestamp":"2026-09-12T04:38:42.296Z"`, `"timestamp":5,"version":5,"isSidechain":5`, true},
		{progress, `"timestamp":"2026-09-12T04:38:41.000Z"`, `"timestamp":5`, false},
		{progress, `"timestamp":"2026-09-12T04:38:41.000Z"`, `"timestamp":"2026-09-12T04:38:41.000000Z"`, false},
		{progress, `"sessionId":"s"`, `"sessionId":5`, false},
		{progress, `"requestId":"r"`, `"requestId":"r","costUSD":"0.5"`, false},
		{progress, `"requestId":"r"`, `"requestId":"r","isSidechain":"true"`, false},
		{progress, `"input_tokens":10`, `"input_tokens":10.0`, false},
		{progress, `"input_tokens":10`, `"speed":"priority","input_tokens":10`, false},
		{progress, `"usage":{`, `"usage": {`, false},
	} {
		line := strings.Replace(tc.line, tc.old, tc.new, 1)
		want := 0
		if tc.kept {
			want = 1
		}
		daily, entry := loadClaudeLine(t, line)
		if daily != want {
			t.Errorf("%s: daily loader read %d entries, want %d", line, daily, want)
		}
		// Only the daily loader reads AgentProgress lines.
		if tc.line == base && entry != want {
			t.Errorf("%s: entry loader read %d entries, want %d", line, entry, want)
		}
	}
}

// Upstream parses a usage line holding "null" into a value that rejects any
// repeated member, at any depth; on another line its typed decode rejects
// only a repeated field, which for its entry loader includes
// isApiErrorMessage and for an AgentProgress line the line's own session and
// its record's fields.
func TestClaudeLinesRepeatingAMemberUpstreamRejectsAreSkipped(t *testing.T) {
	base := `{"type":"assistant","timestamp":"2026-09-12T04:38:42.296Z","version":"2.0.0","sessionId":"s","requestId":"r","cwd":"/w","isApiErrorMessage":false,"costUSD":0.5,"message":{"id":"m","model":"claude-sonnet-4-5","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":10,"cache_read_input_tokens":20,"speed":"standard","cache_creation":{"ephemeral_5m_input_tokens":10}}}}`
	plain := strings.Replace(base, `"stop_reason":null,`, "", 1)
	progress := `{"type":"progress","timestamp":"2026-09-12T04:38:42.296Z","sessionId":"s","data":{"type":"agent_progress","message":{"timestamp":"2026-09-12T04:38:41.000Z","requestId":"r","message":{"id":"m","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":20}}}}}`
	for _, tc := range []struct {
		line, old, new string
		daily, entry   int
	}{
		{base, `"requestId":"r"`, `"requestId":"r","requestId":"r2"`, 0, 0},
		{base, `"cwd":"/w"`, `"cwd":"/w","cwd":"/x"`, 0, 0},
		{base, `"stop_reason":null`, `"stop_reason":null,"content":[{"type":"text","text":"a","text":"b"}]`, 0, 0},
		{plain, "", "", 1, 1},
		{plain, `"cwd":"/w"`, `"cwd":"/w","cwd":"null"`, 0, 0},
		{plain, `"cwd":"/w"`, `"cwd":"/w","cwd":"/x"`, 1, 1},
		{plain, `"id":"m"`, `"id":"m","content":[{"type":"text","text":"a","text":"b"}]`, 1, 1},
		{plain, `"speed":"standard"`, `"speed":"standard","server_tool_use":{"web_search_requests":1,"web_search_requests":2}`, 1, 1},
		{plain, `"requestId":"r"`, `"requestId":"r","requestId":"r2"`, 0, 0},
		{plain, `"id":"m"`, `"id":"m","id":"m"`, 0, 0},
		{plain, `"input_tokens":10`, `"input_tokens":5,"input_tokens":10`, 0, 0},
		{plain, `"ephemeral_5m_input_tokens":10`, `"ephemeral_5m_input_tokens":10,"ephemeral_5m_input_tokens":10`, 0, 0},
		{plain, `"isApiErrorMessage":false`, `"isApiErrorMessage":false,"isApiErrorMessage":false`, 1, 0},
		{progress, `"type":"agent_progress"`, `"type":"agent_progress","type":"agent_progress"`, 1, 0},
		{progress, `"sessionId":"s"`, `"sessionId":"s","sessionId":"s"`, 0, 0},
		{progress, `"requestId":"r"`, `"requestId":"r","requestId":"r"`, 0, 0},
		{progress, `"input_tokens":10`, `"input_tokens":10,"input_tokens":10`, 0, 0},
	} {
		line := strings.Replace(tc.line, tc.old, tc.new, 1)
		if daily, entry := loadClaudeLine(t, line); daily != tc.daily || entry != tc.entry {
			t.Errorf("%s: daily loader read %d entries, entry loader %d; want %d and %d", line, daily, entry, tc.daily, tc.entry)
		}
	}
}

// Upstream bills a line's advisor calls only when its typed decode takes
// every element of usage.iterations: a type string, an optional model and a
// usage whose token counts are unsigned integers, none repeated, nor the
// array itself. An element it rejects drops every advisor, not the line.
func TestClaudeAdvisorsNeedEveryIterationToDecode(t *testing.T) {
	advisor := `{"type":"advisor_message","model":"claude-opus-4-6","input_tokens":50000,"output_tokens":5000}`
	line := func(iterations ...string) string {
		return `{"timestamp":"2026-10-20T10:00:00.000Z","sessionId":"a1","requestId":"req_a1","version":"2.0.1","type":"assistant","message":{"id":"msg_a1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":1000,"output_tokens":10,"iterations":[` + strings.Join(iterations, ",") + `]}}}`
	}
	for _, tc := range []struct {
		line    string
		entries int
	}{
		{line(advisor), 2},
		{line(`{"type":"message","input_tokens":1000,"output_tokens":10}`, advisor), 2},
		{line(`{"type":"message","model":null,"input_tokens":1,"output_tokens":1,"cache_creation":null}`, advisor), 2},
		{line(`{"type":"message","model":"","input_tokens":1,"output_tokens":1,"speed":"fast","foo":1,"foo":2}`, advisor), 2},
		{line(strings.Replace(advisor, "50000", "50000.0", 1)), 1},
		{line(`{"type":"compaction"}`, advisor), 1},
		{line(`{"input_tokens":1000,"output_tokens":10}`, advisor), 1},
		{line(`{"type":5,"input_tokens":1,"output_tokens":1}`, advisor), 1},
		{line(`{"type":"message","model":5,"input_tokens":1,"output_tokens":1}`, advisor), 1},
		{line(`"message"`, advisor), 1},
		{line(`{"type":"message","input_tokens":"1","output_tokens":1}`, advisor), 1},
		{line(`{"type":"message","input_tokens":null,"output_tokens":1}`, advisor), 1},
		{line(`{"type":"message","input_tokens":1,"output_tokens":1,"cache_read_input_tokens":-1}`, advisor), 1},
		{line(`{"type":"message","input_tokens":1,"output_tokens":1,"cache_creation":{"ephemeral_5m_input_tokens":1.5}}`, advisor), 1},
		{line(`{"type":"message","input_tokens":1,"output_tokens":1,"speed":"turbo"}`, advisor), 1},
		{line(`{"type":"message","type":"message","input_tokens":1,"output_tokens":1}`, advisor), 1},
		{line(`{"type":"message","input_tokens":1,"output_tokens":1,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_5m_input_tokens":1}}`, advisor), 1},
		{line(strings.Replace(advisor, `"output_tokens":5000`, `"output_tokens":5000,"output_tokens":5000`, 1)), 1},
		{strings.Replace(line(advisor), `"iterations":[`, `"iterations":[],"iterations":[`, 1), 1},
	} {
		if daily, entry := loadClaudeLine(t, tc.line); daily != tc.entries || entry != tc.entries {
			t.Errorf("%s: daily loader read %d entries, entry loader %d; want %d", tc.line, daily, entry, tc.entries)
		}
	}
}

// Upstream's entry loader, which serves sessions and blocks, decodes
// isApiErrorMessage as a bool; its daily loader does not read it.
func TestClaudeEntryLoadsSkipANonBoolAPIErrorFlag(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-09-12T04:38:42.296Z","sessionId":"s","requestId":"r","isApiErrorMessage":"yes","message":{"id":"m","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":20}}}`
	if daily, entry := loadClaudeLine(t, line); daily != 1 || entry != 0 {
		t.Errorf("daily loader read %d entries, entry loader %d; want 1 and 0", daily, entry)
	}
}

// loadClaudeLine reads a transcript holding line with a daily and an entry
// loader and returns how many entries each read.
func loadClaudeLine(t *testing.T, line string) (daily, entry int) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "projects/p/s.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(line+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	l := New()
	l.SetDailyLoader(func(e *types.UsageEntry) float64 { return e.Cost })
	dailyEntries, _ := l.LoadParallel(context.Background(), []string{p})
	entryEntries, _ := New().LoadParallel(context.Background(), []string{p})
	return len(dailyEntries), len(entryEntries)
}
