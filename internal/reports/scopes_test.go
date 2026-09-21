package reports

import (
	"testing"
	"time"

	"github.com/RedwindA/ccusage_go/internal/types"
)

func reportEntry(agent, session, project, date string, input int) types.UsageEntry {
	ts, _ := time.Parse(time.RFC3339, date)
	return types.UsageEntry{Agent: agent, SessionID: session, ProjectPath: project, Timestamp: ts, InputTokens: input, TotalTokens: input, Model: "model"}
}
func TestSessionDateScopeMatchesEachSource(t *testing.T) {
	for _, agent := range []string{"claude", "qwen", "codex", "pi", "openclaw", "gemini", "grok", "amp", "droid", "opencode", "hermes", "copilot", "goose", "kilo", "antigravity", "zcode"} {
		for _, focused := range []bool{false, true} {
			t.Run(agent+map[bool]string{false: "/unified", true: "/focused"}[focused], func(t *testing.T) {
				entries := []types.UsageEntry{reportEntry(agent, "s", "p", "2026-01-01T12:00:00Z", 10), reportEntry(agent, "s", "p", "2026-01-03T12:00:00Z", 20)}
				o := Options{Kind: "session", Since: "2026-01-03", Until: "2026-01-03", Location: time.UTC}
				if focused {
					o.Agent = agent
				}
				rows := Aggregate(entries, o)
				want := 20
				if agent == "qwen" || agent == "claude" && !focused {
					want = 30
				}
				if len(rows) != 1 || rows[0].Total != want {
					t.Fatalf("want %d tokens: %+v", want, rows)
				}
				o.Since = "2026-01-01"
				o.Until = "2026-01-01"
				rows = Aggregate(entries, o)
				if want == 30 && len(rows) != 0 {
					t.Fatalf("whole session ended after bound: %+v", rows)
				}
				if want == 20 && (len(rows) != 1 || rows[0].Total != 10) {
					t.Fatalf("entry scoped bound: %+v", rows)
				}
			})
		}
	}
}
func TestSessionGroupingScopeAndSystemUsers(t *testing.T) {
	for _, tc := range []struct {
		agent, focused string
		want           int
	}{{"qwen", "", 1}, {"qwen", "qwen", 1}, {"pi", "", 2}, {"pi", "pi", 1}, {"claude", "", 2}, {"claude", "claude", 2}, {"codex", "codex", 1}, {"openclaw", "", 1}} {
		entries := []types.UsageEntry{reportEntry(tc.agent, "s", "p1", "2026-01-01T12:00:00Z", 10), reportEntry(tc.agent, "s", "p2", "2026-01-02T12:00:00Z", 20)}
		rows := Aggregate(entries, Options{Kind: "session", Agent: tc.focused, Location: time.UTC})
		if len(rows) != tc.want {
			t.Fatalf("%+v: %+v", tc, rows)
		}
		entries[0].Raw = map[string]any{"system_user": "alice"}
		entries[1].Raw = map[string]any{"system_user": "bob"}
		rows = Aggregate(entries, Options{Kind: "session", Agent: tc.focused, Location: time.UTC})
		if len(rows) != 2 {
			t.Fatalf("system users merged: %+v", rows)
		}
	}
}
func TestWholeSessionDateRespectsTimezoneAndOtherAgents(t *testing.T) {
	entries := []types.UsageEntry{reportEntry("qwen", "s", "p", "2026-01-01T12:00:00Z", 10), reportEntry("qwen", "s", "p", "2026-01-03T01:00:00Z", 20), reportEntry("codex", "s", "p", "2026-01-02T12:00:00Z", 100)}
	rows := Aggregate(entries, Options{Kind: "session", Agent: "qwen", Since: "2026-01-02", Until: "2026-01-02", Location: time.FixedZone("west", -7*3600)})
	if len(rows) != 1 || rows[0].Total != 30 {
		t.Fatal(rows)
	}
}
func TestModelsPreserveAgentLabelsFastAndAliases(t *testing.T) {
	t.Setenv("CCUSAGE_MODEL_ALIASES", `{"private":"public","[pi] model":"Pi Alias"}`)
	entries := []types.UsageEntry{reportEntry("pi", "s", "p", "2026-01-01T00:00:00Z", 10), reportEntry("openclaw", "s", "p", "2026-01-01T00:00:00Z", 20), reportEntry("claude", "s", "p", "2026-01-01T00:00:00Z", 30), reportEntry("archive", "s", "p", "2026-01-01T00:00:00Z", 40)}
	entries[2].Model = "private"
	entries[2].Speed = "fast"
	entries[3].Raw = map[string]any{"source_format": "pi"}
	rows := Aggregate(entries, Options{Kind: "model", Location: time.UTC})
	want := map[string]bool{"Pi Alias": true, "[openclaw] model": true, "public-fast": true, "[archive] model": true}
	if len(rows) != 4 {
		t.Fatal(rows)
	}
	for _, r := range rows {
		if !want[r.Period] {
			t.Fatal(r)
		}
	}
	if entries[2].Model != "private" {
		t.Fatal("report normalization mutated input")
	}
}
func TestInstancesDoNotCollapseSameBasename(t *testing.T) {
	entries := []types.UsageEntry{reportEntry("claude", "a", "/alice/project", "2026-01-01T00:00:00Z", 1), reportEntry("claude", "b", "/bob/project", "2026-01-01T00:00:00Z", 2)}
	rows := Aggregate(entries, Options{Kind: "daily", Instances: true})
	if len(rows) != 2 {
		t.Fatal(rows)
	}
}
func TestSourceActivityMetadataAndCostParts(t *testing.T) {
	e := reportEntry("example", "s", "p", "2026-01-02T00:00:00Z", 10)
	e.APICost = .1
	e.CacheCreateCost = .2
	e.CacheReadCost = .3
	e.Cost = .6
	e.Raw = map[string]any{"first_activity": "2026-01-01T00:00:00Z", "last_activity": time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)}
	rows := Aggregate([]types.UsageEntry{e}, Options{Kind: "session", Agent: "example"})
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	r := rows[0]
	if r.FirstActivity != "2026-01-01T00:00:00Z" || r.LastActivity != "2026-01-03T00:00:00Z" || r.APICost != .1 || r.CacheCreateCost != .2 || r.CacheReadCost != .3 {
		t.Fatal(r)
	}
}
func TestOpenCodeAggregateFallbackScope(t *testing.T) {
	e := reportEntry("opencode", "s", "p", "2026-01-01T00:00:00Z", 10)
	e.Raw = map[string]any{"session_aggregate": true}
	for _, o := range []Options{{Kind: "daily"}, {Kind: "session", Since: "2026-01-01"}, {Kind: "session", Until: "2026-01-02"}} {
		if rows := Aggregate([]types.UsageEntry{e}, o); len(rows) != 0 {
			t.Fatal(rows)
		}
	}
	if rows := Aggregate([]types.UsageEntry{e}, Options{Kind: "session"}); len(rows) != 1 {
		t.Fatal(rows)
	}
}
func TestAgentBreakdownInheritsPeriodAndUser(t *testing.T) {
	e := reportEntry("codex", "s", "p", "2026-01-01T00:00:00Z", 10)
	e.Raw = map[string]any{"system_user": "alice"}
	rows := Aggregate([]types.UsageEntry{e}, Options{Kind: "daily", ByAgent: true, Location: time.UTC})
	if len(rows) != 1 || len(rows[0].Agents) != 1 {
		t.Fatal(rows)
	}
	sub := rows[0].Agents[0]
	if sub.Period != "2026-01-01" || sub.User != "alice" {
		t.Fatal(sub)
	}
}
