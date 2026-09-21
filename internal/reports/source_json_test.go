package reports

import (
	"encoding/json"
	"github.com/RedwindA/ccusage_go/internal/types"
	"strings"
	"testing"
	"time"
)

func TestFocusedCodexJSON(t *testing.T) {
	entries := []types.UsageEntry{{Agent: "codex", SessionID: "2026/05/05/rollout", Timestamp: time.Date(2026, 5, 5, 1, 2, 3, 0, time.UTC), Model: "gpt-5", InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5, CacheReadInputTokens: 30, TotalTokens: 150, Cost: 0.2, Raw: map[string]any{"is_fallback": true}}}
	o := Options{Agent: "codex", Kind: "session", Location: time.UTC}
	rows := Aggregate(entries, o)
	v := FocusedJSONRows(rows, o)[0]
	if v["costUSD"] != 0.2 || v["totalCost"] != nil || v["sessionFile"] != "rollout" || v["directory"] != "2026/05/05" || v["reasoningOutputTokens"] != 5 || v["lastActivity"] != "2026-05-05T01:02:03.000Z" {
		t.Fatalf("%+v", v)
	}
	models := v["models"].(map[string]any)
	model := models["gpt-5"].(map[string]any)
	if model["isFallback"] != true || model["inputTokens"] != 100 || model["reasoningOutputTokens"] != 5 {
		t.Fatal(model)
	}
	totals := FocusedTotals(rows, o)
	if totals["costUSD"] != 0.2 || totals["reasoningOutputTokens"] != 5 || totals["totalTokens"] != 150 {
		t.Fatal(totals)
	}
}
func TestFocusedClaudeAndAgentSessionMetadata(t *testing.T) {
	rows := []*Row{{Period: "s", ProjectPath: "/work", FirstActivity: "2026-05-05T01:02:03Z", LastActivity: "2026-05-05T02:02:03Z", Requests: 2, Models: []string{"model"}, Breakdowns: []Model{{Name: "model", Total: 10, MissingPricing: true}}}}
	for _, agent := range []string{"claude", "opencode", "hermes", "copilot", "zcode"} {
		o := Options{Agent: agent, Kind: "session"}
		v := FocusedJSONRows(rows, o)[0]
		metadata := agent == "claude" || agent == "copilot" || agent == "zcode"
		if _, ok := v["projectPath"]; ok != metadata {
			t.Fatalf("%s %+v", agent, v)
		}
		if _, ok := v["messageCount"]; ok != (agent == "hermes" || agent == "copilot") {
			t.Fatalf("%s %+v", agent, v)
		}
		m := v["modelBreakdowns"].([]map[string]any)[0]
		if m["totalTokens"] != nil || m["missingPricing"] != true {
			t.Fatal(m)
		}
		o.NoCost = true
		b, _ := json.Marshal(map[string]any{"rows": FocusedJSONRows(rows, o), "totals": FocusedTotals(rows, o)})
		if strings.Contains(strings.ToLower(string(b)), "cost") || strings.Contains(string(b), "missingPricing") || strings.Contains(string(b), "unpricedModels") {
			t.Fatal(string(b))
		}
	}
}
