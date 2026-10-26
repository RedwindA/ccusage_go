package commands

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNativeSourcesACLI(t *testing.T) {
	tests := []struct {
		name         string
		tokens, cost float64
	}{
		{"codex", 120, 620}, {"droid", 65, 160}, {"amp", 120, 340}, {"pi", 25, 48}, {"openclaw", 25, 0}, {"gemini", 200, 380}, {"grok", 120, .0185192}, {"qwen", 200, 380},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateSourceHomes(t)
			dir := t.TempDir()
			cfg := filepath.Join(t.TempDir(), "ccusage.json")
			sourceCLIWrite(t, cfg, `{"defaults":{"pricingOverrides":{"test-model-1":{"inputCostPerToken":1,"outputCostPerToken":2,"cacheCreationInputTokenCost":3,"cacheReadInputTokenCost":4,"fastMultiplier":2}}}}`)
			switch tc.name {
			case "codex":
				sourceCLIWrite(t, filepath.Join(dir, "sessions/s.jsonl"), `{"type":"session_meta","timestamp":"2026-08-01T00:00:00Z","payload":{"id":"s","cwd":"/work"}}
{"type":"turn_context","payload":{"model":"test-model-1"}}
{"type":"event_msg","timestamp":"2026-08-01T00:00:01Z","payload":{"type":"thread_settings_applied","thread_settings":{"service_tier":"priority"}}}
{"type":"event_msg","timestamp":"2026-08-01T00:00:02Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"cache_write_input_tokens":25,"output_tokens":20,"reasoning_output_tokens":10}}}}`)
			case "droid":
				sourceCLIWrite(t, filepath.Join(dir, "sessions/work/s.settings.json"), `{"model":"test-model-1","providerLockTimestamp":"2026-08-01T00:00:00Z","tokenUsage":{"inputTokens":11,"outputTokens":12,"cacheCreationTokens":13,"cacheReadTokens":14,"thinkingTokens":15}}`)
				sourceCLIWrite(t, filepath.Join(dir, "sessions/work/s.jsonl"), `{"type":"session_start","payload":{"cwd":"/work/project"}}`)
			case "amp":
				sourceCLIWrite(t, filepath.Join(dir, "threads/s.json"), `{"id":"T-s","messages":[{"role":"assistant","messageId":42,"usage":{"inputTokens":999,"cacheCreationInputTokens":30,"cacheReadInputTokens":40}}],"usageLedger":{"events":[{"id":"event","timestamp":"2026-08-01T00:00:00Z","model":"test-model-1","toMessageId":42,"tokens":{"input":10,"output":20,"total":120},"credits":0.25}]}}`)
			case "pi":
				sourceCLIWrite(t, filepath.Join(dir, "sessions/work/s.jsonl"), `{"type":"session","timestamp":"2026-08-01T00:00:00Z"}
{"type":"message","id":"m","timestamp":"2026-08-01T00:00:01Z","message":{"role":"assistant","model":"test-model-1","usage":{"input":10,"output":5,"cacheRead":3,"cacheWrite":2,"totalTokens":25}}}`)
			case "openclaw":
				sourceCLIWrite(t, filepath.Join(dir, "agents/main/sessions/s.jsonl"), `{"type":"custom","customType":"model-snapshot","data":{"modelId":"test-model-1","provider":"openai"}}
{"id":"m","type":"message","message":{"role":"assistant","usage":{"input":10,"output":5,"cacheRead":3,"cacheWrite":2,"totalTokens":25,"cost":{"total":0}},"timestamp":1785542401000}}`)
			case "gemini":
				sourceCLIWrite(t, filepath.Join(dir, "chat.json"), `{"sessionId":"s","messages":[{"type":"gemini","id":"m","model":"test-model-1","timestamp":"2026-08-01T00:00:00Z","tokens":{"input":100,"output":20,"cached":40,"thoughts":10,"total":200}}]}`)
			case "grok":
				sourceCLIWrite(t, filepath.Join(dir, "sessions/%2Fwork/s/updates.jsonl"), `{"timestamp":1785542400,"params":{"sessionId":"s","update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"test-model-1":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":40,"cacheCreationTokens":25,"reasoningTokens":10,"costUsdTicks":185192000}}}},"_meta":{"eventId":"m","agentTimestampMs":1785542401000}}}`)
			case "qwen":
				sourceCLIWrite(t, filepath.Join(dir, "projects/work/chats/s.jsonl"), `{"type":"assistant","sessionId":"s","model":"test-model-1","timestamp":"2026-08-01T00:00:00Z","usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"cachedContentTokenCount":40,"thoughtsTokenCount":10,"totalTokenCount":200}}`)
			}
			args := []string{tc.name, "daily", "--json", "--offline", "--data-path", dir, "--config", cfg, "--timezone", "UTC"}
			payload := sourceCLIJSON(t, args...)
			daily, ok := payload["daily"].([]any)
			if !ok || len(daily) != 1 {
				t.Fatalf("daily rows: %#v", payload)
			}
			totals := payload["totals"].(map[string]any)
			costKey := "totalCost"
			if tc.name == "codex" {
				costKey = "costUSD"
			}
			if totals["totalTokens"] != tc.tokens {
				t.Fatalf("total: got %#v want %v", totals, tc.tokens)
			}
			got, ok := totals[costKey].(float64)
			if !ok || math.Abs(got-tc.cost) > 1e-9 {
				t.Fatalf("cost: got %#v want %v", totals, tc.cost)
			}
			if tc.name == "codex" {
				if totals["reasoningOutputTokens"] != 10.0 {
					t.Fatal(totals)
				}
				standard := sourceCLIJSON(t, append(args, "--speed", "standard")...)
				if standard["totals"].(map[string]any)[costKey] != 310.0 {
					t.Fatalf("forced standard: %#v", standard)
				}
			}
			if tc.name == "openclaw" || tc.name == "grok" {
				want := 38.0
				if tc.name == "grok" {
					want = 310
				}
				calculated := sourceCLIJSON(t, append(args, "--mode", "calculate")...)
				if cost := calculated["totals"].(map[string]any)[costKey]; cost != want {
					t.Fatalf("calculate got %v want %v", cost, want)
				}
			}
			if tc.name == "amp" && totals["credits"] != .25 {
				t.Fatal(totals)
			}
			hidden := sourceCLIJSON(t, append(args, "--no-cost")...)
			raw, _ := json.Marshal(hidden)
			if strings.Contains(strings.ToLower(string(raw)), "cost") {
				t.Fatalf("no-cost leaked: %s", raw)
			}
			args[1] = "session"
			sessions := sourceCLIJSON(t, args...)
			if rows, ok := sessions["sessions"].([]any); !ok || len(rows) != 1 {
				t.Fatalf("session: %#v", sessions)
			}
		})
	}
}

// Upstream's OpenClaw counts the part of totalTokens above the counted
// tokens but prices only those, unless output is 0 and the part becomes it.
func TestOpenClawLeavesTheTotalTokenSurplusUnpriced(t *testing.T) {
	isolateSourceHomes(t)
	dir := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "ccusage.json")
	sourceCLIWrite(t, cfg, `{"defaults":{"pricingOverrides":{"test-model-1":{"inputCostPerToken":1,"outputCostPerToken":2}}}}`)
	sourceCLIWrite(t, filepath.Join(dir, "agents/main/sessions/s.jsonl"), `{"id":"a","type":"message","message":{"role":"assistant","model":"test-model-1","usage":{"input":10,"output":5,"totalTokens":100},"timestamp":1785542401000}}
{"id":"b","type":"message","message":{"role":"assistant","model":"test-model-1","usage":{"input":10,"output":0,"totalTokens":100},"timestamp":1785542402000}}`)
	totals := sourceCLIJSON(t, "openclaw", "daily", "--json", "--offline", "--data-path", dir, "--config", cfg, "--timezone", "UTC")["totals"].(map[string]any)
	if totals["totalTokens"] != 200.0 || totals["outputTokens"] != 95.0 || totals["totalCost"] != 210.0 {
		t.Fatalf("totals %v, want 200 tokens, 95 output and a cost of 10+5*2 + 10+90*2", totals)
	}
}

func TestCodexServiceTiersAcrossReports(t *testing.T) {
	isolateSourceHomes(t)
	cfg := filepath.Join(t.TempDir(), "ccusage.json")
	sourceCLIWrite(t, cfg, `{"defaults":{"pricingOverrides":{"test-model-1":{"inputCostPerToken":0.000001,"outputCostPerToken":0.000002,"fastMultiplier":2,"flexMultiplier":0.5},"deepseek-v4-flash":{"fastMultiplier":2,"flexMultiplier":0.5}}}}`)
	// Costs for Auto with Standard, Flex and Fast fallbacks, then forced Flex.
	for model, want := range map[string][4]float64{"test-model-1": {45e-6, 40e-6, 55e-6, 20e-6}, "deepseek-v4-flash": {9.9e-6, 8.8e-6, 12.1e-6, 4.4e-6}} {
		dir := t.TempDir()
		for i, tier := range []string{"default", "flex", "priority", "unknown"} {
			at := fmt.Sprintf("2026-08-16T17:00:%02d.000Z", i+1)
			sourceCLIWrite(t, filepath.Join(dir, "sessions", tier+".jsonl"), `{"timestamp":"`+at+`","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"service_tier":"`+tier+`"}}}
{"timestamp":"`+at+`","type":"event_msg","payload":{"type":"token_count","info":{"model":"`+model+`","last_token_usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}}}`)
		}
		// Only the unclassified rollout follows config.toml; --speed overrides every tier.
		for _, tc := range []struct {
			config, speed string
			want          float64
		}{
			{"", "auto", want[0]},
			{"profile = \"work\"\n[profiles.work]\nmodel = \"gpt-5.4\"\n[profiles.other]\nservice_tier = \"priority\"", "auto", want[0]},
			{`service_tier = "flex"`, "auto", want[1]},
			{`service_tier = "priority"`, "auto", want[2]},
			{`service_tier = "priority"`, "flex", want[3]},
		} {
			sourceCLIWrite(t, filepath.Join(dir, "config.toml"), tc.config)
			for _, kind := range []string{"daily", "weekly", "monthly", "session"} {
				for _, threads := range [][]string{nil, {"--single-thread"}} {
					args := append([]string{"codex", kind, "--json", "--offline", "--data-path", dir, "--config", cfg, "--timezone", "UTC", "--speed", tc.speed}, threads...)
					totals := sourceCLIJSON(t, args...)["totals"].(map[string]any)
					if got, _ := totals["costUSD"].(float64); totals["totalTokens"] != 40.0 || math.Abs(got-tc.want) > 1e-12 {
						t.Errorf("%s %q %v: %#v, want cost %v", model, tc.config, args[1:], totals, tc.want)
					}
				}
			}
		}
	}
}

// Upstream prices Codex usage in every cost mode, including display set by
// flag or by a codex config section in unified reports; display mode only
// leaves out the models it cannot price.
func TestCodexIsPricedInDisplayMode(t *testing.T) {
	home := isolateSourceHomes(t)
	codex := filepath.Join(home, "codex")
	t.Setenv("CODEX_HOME", codex)
	usage := func(model string) string {
		return `{"timestamp":"2026-10-18T12:00:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"model":"` + model + `","last_token_usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":50,"total_tokens":150}}}}`
	}
	sourceCLIWrite(t, filepath.Join(codex, "sessions/2026/10/18/rollout-a.jsonl"), usage("gpt-5"))
	cfg := filepath.Join(home, "ccusage.json")
	sourceCLIWrite(t, cfg, `{"codex":{"defaults":{"mode":"display"}}}`)
	for _, args := range [][]string{{"codex", "daily", "--mode", "display"}, {"codex", "session", "--mode", "display"}, {"daily", "--mode", "display"}, {"daily", "--config", cfg}, {"session", "--config", cfg}} {
		totals := sourceCLIJSON(t, append(args, "--json", "--offline", "--timezone", "UTC")...)["totals"].(map[string]any)
		cost, ok := totals["costUSD"].(float64)
		if !ok {
			cost, _ = totals["totalCost"].(float64)
		}
		if math.Abs(cost-0.00061375) > 1e-12 {
			t.Errorf("%v: totals %v, want cost 0.00061375", args, totals)
		}
	}
	sourceCLIWrite(t, filepath.Join(codex, "sessions/2026/10/18/rollout-b.jsonl"), usage("gpt-unknown-preview"))
	for _, mode := range []string{"auto", "display"} {
		for _, report := range [][]string{{"codex", "daily"}, {"daily"}} {
			out, _, err := sourceCLIRun(append(report, "--mode", mode, "--json", "--offline", "--timezone", "UTC")...)
			if err != nil {
				t.Fatal(err)
			}
			if flagged := strings.Contains(out, "missingPricing") && strings.Contains(out, "unpricedModels"); flagged != (mode == "auto") {
				t.Errorf("%v --mode %s: missing pricing flagged = %v\n%s", report, mode, flagged, out)
			}
		}
	}
}

// Upstream's Droid adapter prices usage in every cost mode, display
// included, and flags the models it cannot price.
func TestDroidIsPricedInDisplayMode(t *testing.T) {
	home := isolateSourceHomes(t)
	droid := filepath.Join(home, "droid")
	t.Setenv("DROID_SESSIONS_DIR", droid)
	usage := `"providerLock":"anthropic","providerLockTimestamp":"2026-05-01T01:02:03.000Z","tokenUsage":{"inputTokens":100000,"outputTokens":50000,"cacheCreationTokens":20000,"cacheReadTokens":10000,"thinkingTokens":5000}}`
	sourceCLIWrite(t, filepath.Join(droid, "work/session-a.settings.json"), `{"model":"Claude-Sonnet-4-[Anthropic]",`+usage)
	sourceCLIWrite(t, filepath.Join(droid, "work/session-b.settings.json"), `{"model":"unknown-droid-model",`+usage)
	for _, args := range [][]string{{"droid", "daily"}, {"droid", "session"}, {"daily"}, {"session"}} {
		payload := sourceCLIJSON(t, append(args, "--mode", "display", "--json", "--offline", "--timezone", "UTC")...)
		totals := payload["totals"].(map[string]any)
		if cost, _ := totals["totalCost"].(float64); math.Abs(cost-1.203) > 1e-9 {
			t.Errorf("%v: totals %v, want cost 1.203", args, totals)
		}
		if unpriced := fmt.Sprint(totals["unpricedModels"]); unpriced != "[unknown-droid-model]" {
			t.Errorf("%v: unpricedModels %s", args, unpriced)
		}
	}
}

// A branch rollout that copied its parent's history without naming the
// parent repeats the parent's usage. Upstream counts it once in daily,
// weekly and monthly reports, unified ones included, and in a unified
// session report bounded by date, which loads Codex as those do; the
// session reports otherwise keep each session's own copy.
func TestCodexCountsUsageCopiedAcrossSessionsOnce(t *testing.T) {
	home := isolateSourceHomes(t)
	codex := filepath.Join(home, "codex")
	t.Setenv("CODEX_HOME", codex)
	tokenCount := func(ts string, input, cached, output, reasoning, total int) string {
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":%d,"total_tokens":%d}}}}`, ts, input, cached, output, reasoning, total)
	}
	parent := `{"timestamp":"2026-05-12T08:00:00.000Z","type":"turn_context","payload":{"model":"gpt-5.2"}}` + "\n" + tokenCount("2026-05-12T08:01:00.000Z", 1000, 100, 200, 20, 1200)
	sourceCLIWrite(t, filepath.Join(codex, "sessions/2026/05/12/rollout-2026-05-12T08-00-00-parent.jsonl"), parent)
	sourceCLIWrite(t, filepath.Join(codex, "sessions/2026/05/12/rollout-2026-05-12T08-02-00-branch.jsonl"), parent+"\n"+tokenCount("2026-05-12T08:02:00.000Z", 1600, 300, 450, 40, 2050))
	args := []string{"--json", "--offline", "--timezone", "UTC"}
	for _, report := range [][]string{{"codex", "daily"}, {"codex", "weekly"}, {"codex", "monthly"}, {"daily"}, {"weekly"}, {"monthly"}, {"daily", "--since", "20260501"}} {
		if totals := sourceCLIJSON(t, append(report, args...)...)["totals"].(map[string]any); totals["totalTokens"] != 2050.0 {
			t.Errorf("%v: totals %v, want 2050 tokens", report, totals)
		}
	}
	for _, tc := range []struct {
		report []string
		branch float64
	}{{[]string{"codex", "session"}, 2050}, {[]string{"codex", "session", "--since", "20260501"}, 2050}, {[]string{"session"}, 2050}, {[]string{"session", "--since", "20260501"}, 850}} {
		payload := sourceCLIJSON(t, append(tc.report, args...)...)
		rows, _ := payload["sessions"].([]any)
		if tc.report[0] == "session" {
			rows, _ = payload["session"].([]any)
		}
		got := map[string]any{}
		for _, row := range rows {
			r := row.(map[string]any)
			id, _ := r["sessionId"].(string)
			if id == "" {
				id, _ = r["period"].(string)
			}
			got[id] = r["totalTokens"]
		}
		if want := map[string]any{"2026/05/12/rollout-2026-05-12T08-00-00-parent": 1200.0, "2026/05/12/rollout-2026-05-12T08-02-00-branch": tc.branch}; !reflect.DeepEqual(got, want) {
			t.Errorf("%v: sessions %v, want %v", tc.report, got, want)
		}
	}
}

// Like upstream, Codex usage is grouped and priced under its
// CCUSAGE_MODEL_ALIASES name: an alias of another model bills that model's
// rates, and an alias that only labels the model bills nothing. The name is
// resolved once, so an alias of the alias only prices it.
func TestCodexPricesTheAliasedModel(t *testing.T) {
	home := isolateSourceHomes(t)
	codex := filepath.Join(home, "codex")
	t.Setenv("CODEX_HOME", codex)
	sourceCLIWrite(t, filepath.Join(codex, "sessions/2026/10/18/rollout-a.jsonl"), `{"timestamp":"2026-10-18T12:00:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"model":"gpt-5","last_token_usage":{"input_tokens":1000000,"output_tokens":100000,"total_tokens":1100000}}}}`)
	for aliases, want := range map[string]string{
		"":                                 "[gpt-5] $2.25 []",
		"gpt-5=gpt-5-mini":                 "[gpt-5-mini] $0.45 []",
		"gpt-5=GPT5":                       "[GPT5] $0 [GPT5]",
		"gpt-5=gpt-5-mini,gpt-5-mini=GPT5": "[gpt-5-mini] $0.45 []",
	} {
		t.Setenv("CCUSAGE_MODEL_ALIASES", aliases)
		for _, report := range [][]string{{"codex", "daily"}, {"daily"}} {
			payload := sourceCLIJSON(t, append(report, "--json", "--offline", "--timezone", "UTC")...)
			row := payload["daily"].([]any)[0].(map[string]any)
			models, _ := row["modelsUsed"].([]any)
			codexModels, _ := row["models"].(map[string]any)
			for model := range codexModels {
				models = append(models, model)
			}
			totals := payload["totals"].(map[string]any)
			cost, ok := totals["costUSD"].(float64)
			if !ok {
				cost, _ = totals["totalCost"].(float64)
			}
			unpriced, _ := totals["unpricedModels"].([]any)
			if got := fmt.Sprintf("%v $%.4g %v", models, cost, unpriced); got != want {
				t.Errorf("%v with aliases %q: %s, want %s", report, aliases, got, want)
			}
		}
	}
}
