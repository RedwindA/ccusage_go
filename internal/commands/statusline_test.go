package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RedwindA/ccusage_go/internal/pricing"
)

func runStatuslineTest(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	cmd := NewStatuslineCommand()
	var out bytes.Buffer
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}
func TestStatuslineHookCostContextAndEffort(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("NO_COLOR", "1")
	hook := `{"session_id":"status-test","transcript_path":"/missing","model":{"id":"claude-sonnet-4-6","display_name":"Long Model"},"cost":{"total_cost_usd":0},"effort":{"level":"high"},"context_window":{"total_input_tokens":120000,"context_window_size":200000}}`
	out, err := runStatuslineTest(t, hook, "--no-cache", "--model-label-aliases", `{"Long Model":"Short"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"🤖 Short (high)", "$0.00 session", "No active block", "120,000 (60%)"} {
		if !strings.Contains(out, expected) {
			t.Fatalf("missing %q in %s", expected, out)
		}
	}
}
func TestStatuslineRecalculatesSessionAndRespectsConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("NO_COLOR", "1")
	entry := map[string]any{"type": "assistant", "timestamp": time.Now().UTC().Format(time.RFC3339), "sessionId": "fixture-session", "message": map[string]any{"id": "unique-status-id", "model": "status-test-model", "usage": map[string]any{"input_tokens": 2, "output_tokens": 3}}}
	raw, _ := json.Marshal(entry)
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfg, []byte(`{"commands":{"statusline":{"costSource":"both","pricingOverrides":{"status-test-model":{"inputCostPerToken":1,"outputCostPerToken":2}},"modelLabelAliases":{"Model":"Friendly"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	hook := `{"session_id":"fixture-session","model":{"display_name":"Model"},"cost":{"total_cost_usd":4}}`
	out, err := runStatuslineTest(t, hook, "--no-cache", "--config", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Friendly") || !strings.Contains(out, "($4.00 cc / $8.00 ccusage)") {
		t.Fatal(out)
	}
	out, err = runStatuslineTest(t, hook, "--no-cache", "--config", cfg, "--cost-source", "cc")
	if err != nil || !strings.Contains(out, "$4.00 session") {
		t.Fatalf("CLI precedence: %s %v", out, err)
	}
}
func TestStatuslineInvalidInputAndFlags(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	for _, input := range []string{"", "{", `{}`, `{"session_id":4}`} {
		if _, err := runStatuslineTest(t, input, "--no-cache"); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	if _, err := runStatuslineTest(t, `{}`, "--context-low-threshold", "80", "--context-medium-threshold", "80"); err == nil {
		t.Fatal("accepted equal thresholds")
	}
}
func TestStatuslineCacheInvalidation(t *testing.T) {
	now := time.Now()
	cache := statuslineCache{Output: "fresh", Updated: now, Mtime: 10, Size: 20}
	if !statuslineCacheFresh(cache, 10, 20, now.Add(500*time.Millisecond), 1) {
		t.Fatal("fresh cache missed")
	}
	for _, tc := range []struct {
		mtime, size int64
		at          time.Time
	}{{11, 20, now}, {10, 21, now}, {10, 20, now.Add(time.Second)}, {10, 20, now.Add(-time.Second)}} {
		if statuslineCacheFresh(cache, tc.mtime, tc.size, tc.at, 1) {
			t.Fatal("stale cache reused")
		}
	}
}
func TestStatuslineTranscriptUsesLatestValidAssistantUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := `{"type":"assistant","message":{"usage":{"input_tokens":1}}}
{"type":"assistant","message":{"usage":{"input_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}}
invalid trailing record
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	service := pricing.NewService()
	service.SetOffline(true)
	input, limit, ok := statuslineTranscriptContext(path, "unpriced", service)
	if !ok || input != 90 || limit != 200000 {
		t.Fatalf("%d %d %v", input, limit, ok)
	}
}
func TestStatuslineStaleOutputWhileLivePIDUpdates(t *testing.T) {
	now := time.Now()
	cache := statuslineCache{Output: "previous", Updated: now.Add(-time.Hour), Mtime: 1, Updating: true, PID: os.Getpid()}
	if output, ok := cachedStatuslineOutput(cache, 2, 0, now, 1); !ok || output != "previous" {
		t.Fatal("live updater lost stale output")
	}
	cache.PID = 0
	if _, ok := cachedStatuslineOutput(cache, 2, 0, now, 1); ok {
		t.Fatal("dead updater kept stale output")
	}
}
func TestClaudeScopedStatuslineConfigAndPricing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("NO_COLOR", "1")
	sourceCLIWrite(t, filepath.Join(dir, "session.jsonl"), `{"type":"assistant","timestamp":"2026-08-01T00:00:00Z","sessionId":"scoped","message":{"id":"scoped-message","model":"scoped-model","usage":{"input_tokens":2,"output_tokens":3}}}`)
	cfg := filepath.Join(t.TempDir(), "config.json")
	sourceCLIWrite(t, cfg, `{"claude":{"commands":{"statusline":{"costSource":"ccusage","modelLabelAliases":{"Model":"Scoped"},"pricingOverrides":{"scoped-model":{"inputCostPerToken":2,"outputCostPerToken":3}}}}}}`)
	for _, args := range [][]string{{"statusline", "--no-cache", "--config", cfg}, {"claude", "statusline", "--no-cache", "--config", cfg}} {
		cmd := NewRootCommand("test")
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetIn(strings.NewReader(`{"session_id":"scoped","model":{"display_name":"Model"}}`))
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Scoped") || !strings.Contains(out.String(), "$13.00 session") {
			t.Fatal(out.String())
		}
	}
}
