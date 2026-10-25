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
