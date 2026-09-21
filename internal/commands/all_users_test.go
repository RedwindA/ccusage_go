package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAllUsersIgnoresForeignSourcePathsAndCodexTier(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("--all-users requires Linux root")
	}
	isolateSourceHomes(t)
	root := t.TempDir()
	home := filepath.Join(root, "user")
	foreign := filepath.Join(root, "foreign")
	bin := filepath.Join(root, "bin")
	sourceCLIWrite(t, filepath.Join(bin, "getent"), "#!/bin/sh\nprintf '%s\\n' 'audit:x:12345:12345::"+home+":/bin/sh'\n")
	if err := os.Chmod(filepath.Join(bin, "getent"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	fixture := `{"type":"session_meta","payload":{"id":"s"}}
{"type":"turn_context","payload":{"model":"audit-model"}}
{"type":"event_msg","timestamp":"2026-08-01T00:00:00Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15},"total_token_usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}}`
	sourceCLIWrite(t, filepath.Join(home, ".codex/sessions/s.jsonl"), fixture)
	sourceCLIWrite(t, filepath.Join(home, ".codex/config.toml"), "service_tier = 'standard'\n")
	sourceCLIWrite(t, filepath.Join(foreign, "config.toml"), "service_tier = 'fast'\n")
	// A foreign source has extra usage, so both token and cost assertions protect
	// isolation: neither foreign logs nor its service tier may affect user scans.
	sourceCLIWrite(t, filepath.Join(foreign, "sessions/foreign.jsonl"), fixture)
	t.Setenv("CODEX_HOME", foreign)
	cfg := filepath.Join(root, "config.json")
	sourceCLIWrite(t, cfg, `{"defaults":{"pricingOverrides":{"audit-model":{"inputCostPerToken":0,"outputCostPerToken":1,"fastMultiplier":2}}}}`)
	args := []string{"daily", "--all-users", "--json", "--offline", "--config", cfg, "--data-path", foreign}
	stdout, diagnostics, err := sourceCLIRun(args...)
	if err != nil {
		t.Fatalf("%v: %s", err, diagnostics)
	}
	var payload map[string]any
	if err = json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("diagnostics polluted JSON: %v %s", err, stdout)
	}
	totals := payload["totals"].(map[string]any)
	if totals["totalTokens"] != 15.0 || totals["totalCost"] != 5.0 {
		t.Fatalf("foreign env affected user scan: %#v", totals)
	}
	daily := payload["daily"].([]any)
	if len(daily) != 1 || daily[0].(map[string]any)["user"] != "audit" {
		t.Fatalf("wrong user attribution: %#v", payload)
	}
	if !strings.Contains(diagnostics, "--all-users") {
		t.Fatalf("missing source override warning: %s", diagnostics)
	}
	stdout, _, err = sourceCLIRun(append(args, "--no-cost")...)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(stdout), "cost") {
		t.Fatalf("cost leaked: %s", stdout)
	}
}
