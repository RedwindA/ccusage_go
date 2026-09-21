package reports

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/RedwindA/ccusage_go/internal/types"
)

func TestCalendarBounds(t *testing.T) {
	for _, s := range []string{"20260230", "2026-13-01", "2026-2-01", "2025-02-29", "foo"} {
		if _, err := NormalizeDate(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	for _, s := range []string{"20240229", "2024-02-29"} {
		if got, err := NormalizeDate(s); err != nil || got != "2024-02-29" {
			t.Errorf("%s: %s %v", s, got, err)
		}
	}
	loc, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 3, 9, 1, 0, 0, 0, loc)
	for _, tc := range []struct {
		kind  string
		n     int
		start time.Weekday
		want  string
	}{{"daily", 2, time.Monday, "2026-03-08"}, {"weekly", 1, time.Monday, "2026-03-09"}, {"weekly", 1, time.Sunday, "2026-03-08"}, {"monthly", 4, time.Monday, "2025-12-01"}} {
		got, err := LastSince(tc.kind, tc.n, now, tc.start)
		if err != nil || got != tc.want {
			t.Errorf("%+v got %s %v", tc, got, err)
		}
	}
}

func TestUnifiedTotalsAndBreakdowns(t *testing.T) {
	ts := time.Date(2026, 3, 9, 0, 30, 0, 0, time.UTC)
	entries := []types.UsageEntry{
		{Agent: "claude", Timestamp: ts, Model: "claude-sonnet-4-6", InputTokens: 10, OutputTokens: 2, CacheReadInputTokens: 30, TotalTokens: 42, Cost: 1, SessionID: "same"},
		{Agent: "codex", Timestamp: ts, Model: "gpt-5.4", InputTokens: 20, OutputTokens: 3, TotalTokens: 23, Cost: 2, SessionID: "same"},
	}
	loc, _ := time.LoadLocation("America/New_York")
	o := Options{Kind: "daily", Location: loc, Since: "2026-03-08", Until: "2026-03-08", ByAgent: true}
	rows := Aggregate(entries, o)
	if len(rows) != 1 || rows[0].Total != 65 || rows[0].Cost != 3 || rows[0].Period != "2026-03-08" || len(rows[0].Agents) != 2 {
		t.Fatalf("bad rows %+v", rows)
	}
	o.Kind = "session"
	rows = Aggregate(entries, o)
	if len(rows) != 2 {
		t.Fatal("sessions from different sources merged")
	}
	o.NoCost = true
	payload := map[string]any{"session": JSONRows(rows, o), "totals": Totals(rows)}
	StripCosts(payload)
	b, _ := json.Marshal(payload)
	if strings.Contains(strings.ToLower(string(b)), "cost") {
		t.Fatalf("cost leaked: %s", b)
	}
	var csv bytes.Buffer
	if err := WriteCSV(&csv, rows, o); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(csv.String(), "cost") {
		t.Fatal(csv.String())
	}
}

func TestWorkspaceAndInstanceIdentity(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entries := []types.UsageEntry{{Timestamp: ts, ProjectPath: "/logs/projects/project-a", Workspace: "/home/alice/a", InputTokens: 3}, {Timestamp: ts, ProjectPath: "/logs/projects/project-b", Workspace: "/home/bob/a", InputTokens: 5}}
	rows := Aggregate(entries, Options{Kind: "workspace", Agent: "claude", Location: time.UTC})
	if len(rows) != 2 {
		t.Fatal("workspace paths must not collapse to basenames")
	}
	rows = Aggregate(entries, Options{Kind: "daily", Instances: true, Project: "project-a", Location: time.UTC})
	if len(rows) != 1 || rows[0].Input != 3 || rows[0].Project != "project-a" {
		t.Fatalf("bad project filter: %+v", rows)
	}
}
