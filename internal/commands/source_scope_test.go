package commands

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RedwindA/ccusage_go/internal/reports"
)

func sourceCodexRollout(at string, input, output int) string {
	return fmt.Sprintf(`{"type":"turn_context","payload":{"model":"gpt-source-config"}}
{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"model":"gpt-source-config","last_token_usage":{"input_tokens":%d,"output_tokens":%d,"total_tokens":%d},"total_token_usage":{"input_tokens":%d,"output_tokens":%d,"total_tokens":%d}}}}`, at, input, output, input+output, input, output, input+output)
}

func sourcePiMessage(at string, input, output int) string {
	return sourcePiModelMessage("gpt-source-config", at, input, output)
}

func sourcePiModelMessage(model, at string, input, output int) string {
	return fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":"assistant","model":%q,"usage":{"input":%d,"output":%d}}}`, at, model, input, output)
}

// sourceConfigFixture isolates the source homes and writes Claude usage in
// project-a (2026-01-04 and 05) and project-b (05), Codex usage on both days
// and the config, where $HOME names the fixture home. It returns the home and
// the config path.
func sourceConfigFixture(t *testing.T, config string) (string, string) {
	t.Helper()
	home := isolateSourceHomes(t)
	claude, codex := filepath.Join(home, "claude"), filepath.Join(home, "codex")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)
	t.Setenv("PI_AGENT_DIR", filepath.Join(home, "pi"))
	t.Setenv("OPENCLAW_DIR", filepath.Join(home, "openclaw"))
	claudeLine := `{"timestamp":"%sT12:00:00Z","sessionId":"session-%s","costUSD":%v,"message":{"id":"%s","model":"claude-sonnet-4-20250514","usage":{"input_tokens":%d,"output_tokens":5}}}`
	sourceCLIWrite(t, filepath.Join(claude, "projects/project-a/a.jsonl"), fmt.Sprintf(claudeLine, "2026-01-04", "a", 0.01, "a1", 10)+"\n"+fmt.Sprintf(claudeLine, "2026-01-05", "a", 0.01, "a2", 10))
	sourceCLIWrite(t, filepath.Join(claude, "projects/project-b/b.jsonl"), fmt.Sprintf(claudeLine, "2026-01-05", "b", 0.02, "b1", 1000))
	sourceCLIWrite(t, filepath.Join(codex, "sessions/2026/01/04/before.jsonl"), sourceCodexRollout("2026-01-04T12:00:00Z", 900, 100))
	sourceCLIWrite(t, filepath.Join(codex, "sessions/2026/01/05/in.jsonl"), sourceCodexRollout("2026-01-05T12:00:00Z", 1000, 500))
	cfg := filepath.Join(home, "ccusage.json")
	sourceCLIWrite(t, cfg, strings.ReplaceAll(config, "$HOME", filepath.ToSlash(home)))
	return home, cfg
}

// sourceRows maps "<period> <agent>" to the input tokens and cost of each
// agent row in section key of a --by-agent report.
func sourceRows(p map[string]any, key string) map[string][2]float64 {
	rows := map[string][2]float64{}
	for _, r := range p[key].([]any) {
		row := r.(map[string]any)
		agents, ok := row["agents"].([]any)
		if !ok {
			agents = []any{row}
		}
		for _, a := range agents {
			agent := a.(map[string]any)
			rows[row["period"].(string)+" "+agent["agent"].(string)] = [2]float64{agent["inputTokens"].(float64), agent["totalCost"].(float64)}
		}
	}
	return rows
}

func assertSourceRows(t *testing.T, name string, got, want map[string][2]float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: rows %v, want %v", name, got, want)
		return
	}
	for key, w := range want {
		if g, ok := got[key]; !ok || g[0] != w[0] || math.Abs(g[1]-w[1]) > 1e-9 {
			t.Errorf("%s: rows %v, want %v", name, got, want)
			return
		}
	}
}

const sourceOverride = `{"gpt-source-config":{"inputCostPerToken":1e-6,"outputCostPerToken":2e-6,"fastMultiplier":3}}`

// Each source of a unified report reads `<source>.commands.<report>` and
// `commands."<source>:<report>"` for the invoked report, as
// `ccusage <source> <report>` would; other sources keep the unified options.
func TestUnifiedReportsApplySourceConfigOfTheInvokedReport(t *testing.T) {
	_, cfg := sourceConfigFixture(t, `{
		"defaults": {"since": "20260101", "timezone": "UTC"},
		"codex": {
			"defaults": {"pricingOverrides": `+sourceOverride+`},
			"commands": {
				"daily": {"since": "20260105"},
				"weekly": {"since": "20260106"},
				"monthly": {"since": "20260105"},
				"session": {"since": "20260106"}
			}
		},
		"commands": {
			"codex:daily": {"mode": "calculate"},
			"codex:weekly": {"mode": "calculate"},
			"codex:monthly": {"mode": "display"},
			"codex:session": {"mode": "calculate"}
		}
	}`)
	for _, tc := range []struct {
		name string
		args []string
		key  string
		want map[string][2]float64
	}{
		{"bare", nil, "daily", map[string][2]float64{"2026-01-04 claude": {10, 0.01}, "2026-01-05 claude": {1010, 0.03}, "2026-01-05 codex": {1000, 0.002}}},
		{"daily", []string{"daily"}, "daily", map[string][2]float64{"2026-01-04 claude": {10, 0.01}, "2026-01-05 claude": {1010, 0.03}, "2026-01-05 codex": {1000, 0.002}}},
		{"weekly", []string{"weekly"}, "weekly", map[string][2]float64{"2025-12-29 claude": {10, 0.01}, "2026-01-05 claude": {1010, 0.03}}},
		{"monthly", []string{"monthly"}, "monthly", map[string][2]float64{"2026-01 claude": {1020, 0.04}, "2026-01 codex": {1000, 0}}},
		{"session", []string{"session"}, "session", map[string][2]float64{"session-a claude": {20, 0.02}, "session-b claude": {1000, 0.02}}},
	} {
		p := sourceCLIJSON(t, append(tc.args, "--json", "--offline", "--by-agent", "--config", cfg)...)
		assertSourceRows(t, tc.name, sourceRows(p, tc.key), tc.want)
	}
	// A section reads the invoked report's configuration.
	p := sourceCLIJSON(t, "daily", "--json", "--offline", "--by-agent", "--config", cfg, "--sections", "monthly")
	assertSourceRows(t, "sections", sourceRows(p, "monthly"), map[string][2]float64{"2026-01 claude": {1020, 0.04}, "2026-01 codex": {1000, 0.002}})
}

// Explicit flags still win over a source's sections, which still supply what
// the command line leaves out, such as the Codex tier and pricing.
func TestUnifiedSourceConfigKeepsCommandLineFlagsFirst(t *testing.T) {
	_, cfg := sourceConfigFixture(t, `{"codex": {"defaults": {"since": "20260105", "timezone": "Pacific/Auckland", "mode": "display", "speed": "fast", "pricingOverrides": `+sourceOverride+`}}}`)
	p := sourceCLIJSON(t, "daily", "--json", "--offline", "--by-agent", "--config", cfg, "--since", "20260104", "--timezone", "UTC", "--mode", "auto")
	assertSourceRows(t, "flags", sourceRows(p, "daily"), map[string][2]float64{"2026-01-04 claude": {10, 0.01}, "2026-01-05 claude": {1010, 0.03}, "2026-01-04 codex": {900, 0.0033}, "2026-01-05 codex": {1000, 0.006}})
}

// Colon and spaced command sections address a source without a source
// object, and a source groups its dates in its own timezone.
func TestUnifiedSourceConfigFromCommandSectionsAndTimezone(t *testing.T) {
	for _, config := range []string{
		`{"defaults": {"timezone": "UTC"}, "commands": {"codex:daily": {"since": "20260105"}}}`,
		`{"defaults": {"timezone": "UTC"}, "commands": {"codex daily": {"since": "20260105"}}}`,
	} {
		_, cfg := sourceConfigFixture(t, config)
		p := sourceCLIJSON(t, "daily", "--json", "--offline", "--by-agent", "--config", cfg)
		assertSourceRows(t, config, sourceRows(p, "daily"), map[string][2]float64{"2026-01-04 claude": {10, 0.01}, "2026-01-05 claude": {1010, 0.03}, "2026-01-05 codex": {1000, 0}})
	}
	_, cfg := sourceConfigFixture(t, `{"defaults": {"timezone": "UTC"}, "codex": {"defaults": {"timezone": "Pacific/Auckland"}}}`)
	p := sourceCLIJSON(t, "daily", "--json", "--offline", "--by-agent", "--config", cfg)
	assertSourceRows(t, "timezone", sourceRows(p, "daily"), map[string][2]float64{"2026-01-04 claude": {10, 0.01}, "2026-01-05 claude": {1010, 0.03}, "2026-01-05 codex": {900, 0}, "2026-01-06 codex": {1000, 0}})
}

func TestUnifiedSourceConfigErrorsNameTheSource(t *testing.T) {
	for _, tc := range []struct {
		config, want string
		args         []string
	}{
		{`{"codex": {"defaults": {"since": "20260301"}}}`, "--last cannot be combined with --since or --until", []string{"daily", "--last", "1"}},
		{`{"codex": {"defaults": {"timezone": "Not/AZone"}}}`, "invalid timezone", []string{"daily"}},
		{`{"codex": {"defaults": {"since": "2026-02-30"}}}`, `since "2026-02-30"`, []string{"weekly"}},
	} {
		_, cfg := sourceConfigFixture(t, tc.config)
		_, _, err := sourceCLIRun(append(tc.args, "--json", "--offline", "--config", cfg)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.HasSuffix(err.Error(), "(in codex config)") {
			t.Errorf("%s: got %v", tc.config, err)
		}
	}
}

// A source's --last window starts weeks on the unified start of week, not on
// its own, while the rest of its section still applies.
func TestUnifiedSourceLastUsesTheUnifiedWeekStart(t *testing.T) {
	now := time.Now().UTC()
	wednesday, sunday := reports.WeekStart(now, time.Wednesday), reports.WeekStart(now, time.Sunday)
	earlier := wednesday
	if sunday.Before(wednesday) {
		earlier = sunday
	}
	home, cfg := sourceConfigFixture(t, `{"defaults": {"timezone": "UTC"}, "commands": {"weekly": {"startOfWeek": "wednesday"}}, "codex": {"commands": {"weekly": {"startOfWeek": "sunday", "pricingOverrides": `+sourceOverride+`}}}}`)
	sourceCLIWrite(t, filepath.Join(home, "codex/sessions/earlier.jsonl"), sourceCodexRollout(earlier.Format("2006-01-02")+"T12:00:00Z", 900, 100))
	sourceCLIWrite(t, filepath.Join(home, "codex/sessions/today.jsonl"), sourceCodexRollout(now.Format(time.RFC3339), 1000, 500))
	want := [2]float64{1000, 0.002}
	if earlier.Equal(wednesday) {
		want = [2]float64{1900, 0.0031}
	}
	p := sourceCLIJSON(t, "weekly", "--last", "1", "--json", "--offline", "--by-agent", "--config", cfg)
	assertSourceRows(t, "weekly", sourceRows(p, "weekly"), map[string][2]float64{wednesday.Format("2006-01-02") + " codex": want})
}

// Overrides, tier and date window configured for one source price only that
// source, with or without date bounds.
func TestUnifiedSourcesKeepTheirOwnPricing(t *testing.T) {
	for _, tc := range []struct {
		window string
		codex  map[string][2]float64
	}{
		{`"since": "20260105", "until": "20260105", `, map[string][2]float64{"2026-01-05 codex": {1000, 0.006}}},
		{"", map[string][2]float64{"2026-01-04 codex": {900, 0.0033}, "2026-01-05 codex": {1000, 0.006}}},
	} {
		home, cfg := sourceConfigFixture(t, `{
			"defaults": {"timezone": "UTC"},
			"codex": {"defaults": {`+tc.window+`"speed": "fast", "pricingOverrides": `+sourceOverride+`}},
			"pi": {"defaults": {"pricingOverrides": {"gpt-source-config": {"inputCostPerToken": 5e-6, "outputCostPerToken": 10e-6}}}}
		}`)
		sourceCLIWrite(t, filepath.Join(home, "pi/project-a/agent_pi-session.jsonl"), sourcePiMessage("2026-01-05T12:00:00Z", 1000, 500))
		want := map[string][2]float64{"2026-01-04 claude": {10, 0.01}, "2026-01-05 claude": {1010, 0.03}, "2026-01-05 pi": {1000, 0.01}}
		for key, row := range tc.codex {
			want[key] = row
		}
		p := sourceCLIJSON(t, "daily", "--json", "--offline", "--by-agent", "--config", cfg)
		assertSourceRows(t, tc.window, sourceRows(p, "daily"), want)
	}
}

func TestUnifiedSourcesReadTheirConfiguredPaths(t *testing.T) {
	home, cfg := sourceConfigFixture(t, `{"defaults": {"timezone": "UTC"}, "pi": {"defaults": {"piPath": "$HOME/custom-pi"}}, "openclaw": {"commands": {"daily": {"openClawPath": "$HOME/custom-openclaw"}}}}`)
	sourceCLIWrite(t, filepath.Join(home, "pi/project-a/agent_default.jsonl"), sourcePiMessage("2026-01-05T12:00:00Z", 7, 3))
	sourceCLIWrite(t, filepath.Join(home, "custom-pi/project-a/agent_custom.jsonl"), sourcePiMessage("2026-01-05T12:00:00Z", 30, 40))
	sourceCLIWrite(t, filepath.Join(home, "custom-openclaw/agents/main/sessions/custom.jsonl"), `{"type":"message","message":{"role":"assistant","model":"gpt-5.2","usage":{"input":1660,"output":55,"totalTokens":1715,"cost":{"total":0.02}},"timestamp":1767614400000}}`)
	p := sourceCLIJSON(t, "daily", "--json", "--offline", "--by-agent", "--config", cfg)
	assertSourceRows(t, "paths", sourceRows(p, "daily"), map[string][2]float64{"2026-01-04 claude": {10, 0.01}, "2026-01-05 claude": {1010, 0.03}, "2026-01-04 codex": {900, 0}, "2026-01-05 codex": {1000, 0}, "2026-01-05 pi": {30, 0}, "2026-01-05 openclaw": {1660, 0.02}})
}

func TestNamedPiStoreOverlappingTheSourcePiPathRejected(t *testing.T) {
	_, cfg := sourceConfigFixture(t, `{"pi": {"defaults": {"piPath": "$HOME/custom-pi"}, "stores": [{"name": "fork", "path": "$HOME/custom-pi/nested"}]}}`)
	if _, _, err := sourceCLIRun("daily", "--json", "--offline", "--config", cfg); err == nil || !strings.Contains(err.Error(), `"fork"`) {
		t.Fatalf("got %v", err)
	}
}

// Named pi stores load and price with the unified options; pi's sections
// reach only the default pi store.
func TestNamedPiStoresKeepTheUnifiedOptions(t *testing.T) {
	home, cfg := sourceConfigFixture(t, `{
		"defaults": {"timezone": "UTC", "pricingOverrides": {"[fork] gpt-named-source-config": {"inputCostPerToken": 1e-6, "outputCostPerToken": 2e-6}}},
		"pi": {
			"stores": [{"name": "fork", "path": "$HOME/fork"}],
			"defaults": {"since": "20260106", "pricingOverrides": {"[fork] gpt-named-source-config": {"inputCostPerToken": 10e-6, "outputCostPerToken": 20e-6}}}
		}
	}`)
	sourceCLIWrite(t, filepath.Join(home, "pi/project-a/agent_default.jsonl"), sourcePiMessage("2026-01-05T12:00:00Z", 7, 3))
	sourceCLIWrite(t, filepath.Join(home, "fork/project-a/agent_fork.jsonl"), sourcePiModelMessage("gpt-named-source-config", "2026-01-05T12:00:00Z", 1000, 500))
	p := sourceCLIJSON(t, "daily", "--json", "--offline", "--by-agent", "--config", cfg)
	assertSourceRows(t, "stores", sourceRows(p, "daily"), map[string][2]float64{"2026-01-04 claude": {10, 0.01}, "2026-01-05 claude": {1010, 0.03}, "2026-01-04 codex": {900, 0}, "2026-01-05 codex": {1000, 0}, "2026-01-05 fork": {1000, 0.002}})
}

// Every section of a unified report uses the invoked report's source
// options: claude.commands.daily.project filters only Claude, and pi's path
// and window reach the session section too.
func TestUnifiedSourceConfigReachesSections(t *testing.T) {
	home, cfg := sourceConfigFixture(t, `{
		"defaults": {"timezone": "UTC"},
		"claude": {"commands": {"daily": {"project": "project-a"}}},
		"pi": {"commands": {"daily": {"piPath": "$HOME/custom-pi", "since": "20260105", "until": "20260105"}}}
	}`)
	sourceCLIWrite(t, filepath.Join(home, "custom-pi/project-a/agent_in-range.jsonl"), sourcePiMessage("2026-01-05T12:00:00Z", 1000, 500))
	sourceCLIWrite(t, filepath.Join(home, "custom-pi/project-b/agent_out-of-range.jsonl"), sourcePiMessage("2026-01-06T12:00:00Z", 10000, 5000))
	p := sourceCLIJSON(t, "daily", "--json", "--offline", "--by-agent", "--config", cfg, "--sections", "session")
	assertSourceRows(t, "daily", sourceRows(p, "daily"), map[string][2]float64{"2026-01-04 claude": {10, 0.01}, "2026-01-05 claude": {10, 0.01}, "2026-01-04 codex": {900, 0}, "2026-01-05 codex": {1000, 0}, "2026-01-05 pi": {1000, 0}})
	assertSourceRows(t, "session", sourceRows(p, "session"), map[string][2]float64{"session-a claude": {20, 0.02}, "2026/01/04/before codex": {900, 0}, "2026/01/05/in codex": {1000, 0}, "in-range pi": {1000, 0}})
}
