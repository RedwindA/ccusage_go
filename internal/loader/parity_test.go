package loader

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
