package reports

import (
	"fmt"
	"reflect"
	"sort"
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
				o := Options{Kind: "session", Since: "2026-01-03", Until: "2026-01-04", Location: time.UTC}
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

// Upstream dates a whole session by its UTC RFC3339 last activity, dashes
// stripped and compared as a string with the YYYYMMDD bounds, so the report
// timezone does not move it and --until drops a session last active that day.
func TestWholeSessionDateComparesUTCLastActivity(t *testing.T) {
	west := time.FixedZone("west", -8*3600)
	for _, tc := range []struct{ agent, focused string }{{"claude", ""}, {"qwen", ""}, {"qwen", "qwen"}} {
		entries := []types.UsageEntry{
			reportEntry(tc.agent, "s1", "p", "2026-01-01T12:00:00Z", 10),
			reportEntry(tc.agent, "s1", "p", "2026-01-03T12:00:00Z", 20),
			reportEntry(tc.agent, "s2", "p", "2026-01-04T01:00:00Z", 5),
			reportEntry("codex", "s2", "p", "2026-01-02T12:00:00Z", 100),
		}
		for _, bounds := range []struct {
			since, until string
			want         []string
		}{
			{"", "2026-01-03", nil},
			{"", "2026-01-04", []string{"s1"}},
			{"2026-01-03", "", []string{"s1", "s2"}},
			{"2026-01-04", "", []string{"s2"}},
			{"2026-01-01", "2026-01-02", nil},
		} {
			o := Options{Kind: "session", Agent: tc.focused, Since: bounds.since, Until: bounds.until, Location: west}
			var got []string
			for _, row := range Aggregate(entries, o) {
				if row.Agent == tc.agent {
					got = append(got, row.Period)
				}
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, bounds.want) {
				t.Errorf("%s focused=%q since=%q until=%q: got %v, want %v", tc.agent, tc.focused, bounds.since, bounds.until, got, bounds.want)
			}
		}
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

// Like upstream's Claude loader, Fast usage is <model>-fast even when the
// model's name already ends in -fast, so it stays apart from the model's
// standard usage.
func TestFastUsageOfAFastNamedModelIsItsOwnModel(t *testing.T) {
	entries := []types.UsageEntry{reportEntry("claude", "s", "p", "2026-01-01T00:00:00Z", 10), reportEntry("claude", "s", "p", "2026-01-01T00:00:00Z", 20)}
	for i := range entries {
		entries[i].Model = "x-ai/grok-4.1-fast"
	}
	entries[1].Speed = "fast"
	got := map[string]int{}
	for _, r := range Aggregate(entries, Options{Kind: "model", Location: time.UTC}) {
		got[r.Period] = r.Input
	}
	if want := map[string]int{"x-ai/grok-4.1-fast": 10, "x-ai/grok-4.1-fast-fast": 20}; !reflect.DeepEqual(got, want) {
		t.Fatalf("model rows %v, want %v", got, want)
	}
}

// Upstream groups --instances rows by the project's directory under
// projects/, whichever Claude root holds it; system users stay apart.
func TestInstancesMergeAClaudeProjectAcrossRoots(t *testing.T) {
	entries := []types.UsageEntry{reportEntry("claude", "a", "/alice/.claude/projects/project", "2026-01-01T00:00:00Z", 1), reportEntry("claude", "b", "/alice/.config/claude/projects/project", "2026-01-01T00:00:00Z", 2), reportEntry("claude", "c", "/bob/.claude/projects/project", "2026-01-01T00:00:00Z", 4)}
	entries[2].Raw = map[string]any{"system_user": "bob"}
	rows := Aggregate(entries, Options{Kind: "daily", Instances: true})
	if len(rows) != 2 || rows[0].Total+rows[1].Total != 7 || rows[0].Total != 3 && rows[1].Total != 3 {
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

// Unified daily-family reports merge each agent's daily summary and drop one
// with neither tokens nor cost first, like a Copilot credit row that --mode
// calculate prices at $0.
func TestUnifiedDropsAgentDaysWithoutTokensOrCost(t *testing.T) {
	credit := reportEntry("copilot", "c", "p", "2026-02-04T01:00:00Z", 0)
	credit.Model = ""
	credit.Raw = map[string]any{"request_count": 0}
	entries := []types.UsageEntry{reportEntry("copilot", "c", "p", "2026-02-03T02:00:00Z", 50), credit, reportEntry("claude", "s", "p", "2026-02-04T05:00:00Z", 150)}
	entries[0].Model = "gpt-5.4"
	for _, tc := range []struct {
		kind string
		want []string
	}{
		{"daily", []string{"2026-02-03 [copilot] [gpt-5.4] copilot=[gpt-5.4]", "2026-02-04 [claude] [model] claude=[model]"}},
		{"weekly", []string{"2026-02-01 [claude copilot] [gpt-5.4 model] claude=[model] copilot=[gpt-5.4]"}},
		{"monthly", []string{"2026-02 [claude copilot] [gpt-5.4 model] claude=[model] copilot=[gpt-5.4]"}},
	} {
		var got []string
		for _, r := range Aggregate(entries, Options{Kind: tc.kind, ByAgent: true, Location: time.UTC}) {
			line := fmt.Sprintf("%s %v %v", r.Period, r.Metadata["agents"], r.Models)
			for _, a := range r.Agents {
				line += fmt.Sprintf(" %s=%v", a.Agent, a.Models)
			}
			got = append(got, line)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.kind, got, tc.want)
		}
	}
	credit.Cost = .02
	rows := Aggregate([]types.UsageEntry{credit, entries[2]}, Options{Kind: "daily", Location: time.UTC})
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].Models, []string{"model", "unknown"}) || rows[0].Cost != .02 {
		t.Fatalf("cost-only credit row dropped: %+v", rows)
	}
}
