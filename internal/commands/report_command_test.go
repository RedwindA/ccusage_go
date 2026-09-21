package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureReport(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	p := filepath.Join(root, "projects", "demo")
	if err := os.MkdirAll(p, 0700); err != nil {
		t.Fatal(err)
	}
	data := `{"timestamp":"2026-03-08T06:30:00Z","sessionId":"one","cwd":"/work/demo","costUSD":1.5,"message":{"id":"m1","model":"claude-sonnet-4-6","usage":{"input_tokens":10,"output_tokens":3,"cache_read_input_tokens":20}}}
{"timestamp":"2026-03-09T03:30:00Z","sessionId":"two","cwd":"/work/second","costUSD":2.5,"message":{"id":"m2","model":"claude-sonnet-4-6","usage":{"input_tokens":30,"output_tokens":7}}}
{"timestamp":"2026-03-09T04:30:00Z","sessionId":"three","costUSD":5,"message":{"id":"m3","model":"claude-sonnet-4-6","usage":{"input_tokens":100,"output_tokens":1}}}
`
	if err := os.WriteFile(filepath.Join(p, "session.jsonl"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func executeReport(args ...string) (string, error) {
	cmd := NewRootCommand("test")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestCommandTimezoneAndJSONParity(t *testing.T) {
	path := fixtureReport(t)
	args := []string{"claude", "daily", "--data-path", path, "--offline", "--since", "20260308", "--until", "2026-03-08", "--timezone", "America/New_York"}
	out, err := executeReport(append(args, "--json")...)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Daily []struct {
			Input int     `json:"inputTokens"`
			Cost  float64 `json:"totalCost"`
		}
		Totals struct {
			Input int `json:"inputTokens"`
		}
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("not JSON: %v %s", err, out)
	}
	if len(report.Daily) != 1 || report.Totals.Input != 40 || report.Daily[0].Cost != 4 {
		t.Fatalf("unexpected report %s", out)
	}
	out, err = executeReport(append(args, "--format", "csv", "--no-cost")...)
	if err != nil || !strings.Contains(out, ",40,10,0,20,70") || strings.Contains(out, "cost") {
		t.Fatalf("csv parity: %v %s", err, out)
	}
}

func TestDimensionsAndConfigPrecedence(t *testing.T) {
	path := fixtureReport(t)
	cfg := filepath.Join(t.TempDir(), "ccusage.json")
	if err := os.WriteFile(cfg, []byte(`{"defaults":{"json":true,"noCost":true,"since":"20260309","timezone":"UTC"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := executeReport("claude", "workspace", "--data-path", path, "--config", cfg, "--since", "20260308")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "totalCost") || !strings.Contains(out, "/work/demo") || !strings.Contains(out, "/work/second") {
		t.Fatal(out)
	}
}

func TestReportValidationBeforeLoading(t *testing.T) {
	for _, args := range [][]string{{"daily", "--since", "20260230"}, {"daily", "--last", "0"}, {"daily", "--last", "1", "--since", "20260101"}, {"daily", "--since", "20260902", "--until", "20260901"}, {"claude", "daily", "--mode", "wrong"}, {"daily", "--sections", ","}, {"session", "--last", "1"}} {
		if out, err := executeReport(args...); err == nil {
			t.Errorf("accepted %v: %s", args, out)
		}
	}
}

func TestEveryAgentCommandRegistered(t *testing.T) {
	root := NewRootCommand("test")
	for _, source := range SourceNames() {
		for _, kind := range []string{"daily", "weekly", "monthly", "session"} {
			cmd, _, err := root.Find([]string{source, kind})
			if err != nil || cmd.Name() != kind || cmd.Parent().Name() != source {
				t.Errorf("missing %s %s", source, kind)
			}
		}
	}
}

func TestClaudeInstancesNativeJSONAndNoCost(t *testing.T) {
	path := fixtureReport(t)
	out, err := executeReport("claude", "daily", "--data-path", path, "--offline", "--json", "--instances", "--no-cost", "--project-aliases", "demo=Display Name")
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	projects, ok := p["projects"].(map[string]any)
	if !ok || projects["demo"] == nil {
		t.Fatal(out)
	}
	if strings.Contains(strings.ToLower(out), "cost") || strings.Contains(out, "Display Name") {
		t.Fatal(out)
	}
}

func TestExplicitFormatOverridesConfiguredJSON(t *testing.T) {
	path := fixtureReport(t)
	cfg := filepath.Join(t.TempDir(), "ccusage.json")
	if err := os.WriteFile(cfg, []byte(`{"defaults":{"json":true,"offline":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := executeReport("claude", "daily", "--data-path", path, "--config", cfg, "--format", "csv")
	if err != nil || !strings.HasPrefix(out, "daily,agent,") {
		t.Fatalf("format precedence: %v %s", err, out)
	}
}

func TestOpenClawUpstreamPathFlag(t *testing.T) {
	isolateSourceHomes(t)
	out, err := executeReport("openclaw", "daily", "--open-claw-path", t.TempDir(), "--offline", "--json")
	if err != nil || !strings.Contains(out, `"daily": []`) {
		t.Fatalf("flag: %v %s", err, out)
	}
}

func TestFocusedVersionAndDebugSamples(t *testing.T) {
	out, err := executeReport("codex", "daily", "--version")
	if err != nil || !strings.Contains(out, "test") {
		t.Fatalf("version: %v %s", err, out)
	}
	stdout, stderr, err := sourceCLIRun("claude", "daily", "--data-path", fixtureReport(t), "--offline", "--json", "--debug", "--debug-samples", "1")
	if err != nil || !json.Valid([]byte(stdout)) || strings.Count(stderr, "Pricing mismatch") != 1 || !strings.Contains(stderr, "Pricing discrepancies: 3") {
		t.Fatalf("debug stdout=%s stderr=%s err=%v", stdout, stderr, err)
	}
}

func TestClaudeSessionIDDetailJSON(t *testing.T) {
	path := fixtureReport(t)
	payload := sourceCLIJSON(t, "claude", "session", "--id", "one", "--data-path", path, "--offline", "--json")
	if payload["sessionId"] != "one" || payload["totalTokens"] != 33.0 || len(payload["entries"].([]any)) != 1 {
		t.Fatalf("wrong detail: %#v", payload)
	}
	out, err := executeReport("claude", "session", "--id", "missing", "--data-path", path, "--offline", "--json")
	if err != nil || strings.TrimSpace(out) != "null" {
		t.Fatalf("missing session: %v %s", err, out)
	}
	out, err = executeReport("claude", "session", "--id", "one", "--data-path", path, "--offline", "--json", "--no-cost")
	if err != nil || strings.Contains(strings.ToLower(out), "cost") {
		t.Fatalf("no-cost detail: %v %s", err, out)
	}
}

func TestRootSessionIDUsesClaudeDetails(t *testing.T) {
	p := sourceCLIJSON(t, "session", "-i", "one", "--data-path", fixtureReport(t), "--offline", "--json")
	if p["sessionId"] != "one" {
		t.Fatalf("wrong detail route %#v", p)
	}
	if _, err := executeReport("session", "--id", "one", "--all-users", "--offline"); err == nil {
		t.Fatal("accepted incompatible --all-users")
	}
}
