package loader

import (
	"context"
	"encoding/json"
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
