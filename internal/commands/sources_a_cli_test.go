package commands

import (
	"encoding/json"
	"math"
	"path/filepath"
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
				want := 48.0
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
