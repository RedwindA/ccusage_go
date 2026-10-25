package commands

import (
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sort"
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
	sourceCLIWrite(t, filepath.Join(claude, "projects/project-a/session-a.jsonl"), fmt.Sprintf(claudeLine, "2026-01-04", "a", 0.01, "a1", 10)+"\n"+fmt.Sprintf(claudeLine, "2026-01-05", "a", 0.01, "a2", 10))
	sourceCLIWrite(t, filepath.Join(claude, "projects/project-b/session-b.jsonl"), fmt.Sprintf(claudeLine, "2026-01-05", "b", 0.02, "b1", 1000))
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
			"codex:monthly": {"speed": "fast"},
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
		{"monthly", []string{"monthly"}, "monthly", map[string][2]float64{"2026-01 claude": {1020, 0.04}, "2026-01 codex": {1000, 0.006}}},
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

// Upstream's config has no period selectors: `last`, `date`, `month` and
// `week` in any section leave the window to the command line, so no section
// drops a source's rows or fails a report that --last does not apply to.
func TestConfigPeriodSelectorsAreIgnored(t *testing.T) {
	days := map[string][2]float64{"2026-01-04 claude": {10, 0.01}, "2026-01-05 claude": {1010, 0.03}, "2026-01-04 codex": {900, 0}, "2026-01-05 codex": {1000, 0}}
	sessions := map[string][2]float64{"session-a claude": {20, 0.02}, "session-b claude": {1000, 0.02}, "2026/01/04/before codex": {900, 0}, "2026/01/05/in codex": {1000, 0}}
	for _, tc := range []struct {
		config string
		args   []string
		key    string
		want   map[string][2]float64
	}{
		{`{"defaults": {"timezone": "UTC"}, "codex": {"defaults": {"last": 2}}}`, []string{"daily"}, "daily", days},
		{`{"defaults": {"timezone": "UTC"}, "codex": {"commands": {"daily": {"last": 1}}}}`, []string{"daily", "--since", "20260101"}, "daily", days},
		{`{"defaults": {"timezone": "UTC"}, "codex": {"defaults": {"last": 7}}}`, []string{"daily", "--sections", "session"}, "session", sessions},
		{`{"defaults": {"timezone": "UTC"}, "codex": {"defaults": {"last": 7}}}`, []string{"session"}, "session", sessions},
		{`{"defaults": {"timezone": "UTC", "last": 7}}`, []string{"session"}, "session", sessions},
		{`{"defaults": {"timezone": "UTC", "last": 2}}`, []string{"daily"}, "daily", days},
		{`{"defaults": {"timezone": "UTC"}, "commands": {"codex:daily": {"date": "2026-01-04"}}}`, []string{"daily"}, "daily", days},
		{`{"defaults": {"timezone": "UTC"}, "codex": {"defaults": {"week": "2025-12-01"}}}`, []string{"weekly"}, "weekly", map[string][2]float64{"2025-12-29 claude": {10, 0.01}, "2025-12-29 codex": {900, 0}, "2026-01-05 claude": {1010, 0.03}, "2026-01-05 codex": {1000, 0}}},
		{`{"defaults": {"timezone": "UTC"}, "codex": {"defaults": {"month": "2025-12"}}}`, []string{"monthly"}, "monthly", map[string][2]float64{"2026-01 claude": {1020, 0.04}, "2026-01 codex": {1900, 0}}},
	} {
		_, cfg := sourceConfigFixture(t, tc.config)
		p := sourceCLIJSON(t, append(tc.args, "--json", "--offline", "--by-agent", "--config", cfg)...)
		assertSourceRows(t, tc.config+" "+strings.Join(tc.args, " "), sourceRows(p, tc.key), tc.want)
	}
	_, cfg := sourceConfigFixture(t, `{"defaults": {"timezone": "UTC"}, "codex": {"defaults": {"last": 1}}}`)
	for report, key := range map[string]string{"daily": "daily", "session": "sessions"} {
		p := sourceCLIJSON(t, "codex", report, "--json", "--offline", "--config", cfg)
		if rows := p[key].([]any); len(rows) != 2 {
			t.Errorf("codex %s: %d rows, want 2", report, len(rows))
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

// Upstream reads -w and startOfWeek only for weekly reports; the weekly
// section of another unified report starts its weeks on Monday.
func TestOnlyUnifiedWeeklyReportsTakeAStartOfWeek(t *testing.T) {
	home := isolateSourceHomes(t)
	claude := filepath.Join(home, "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	var lines []string
	for i, day := range []string{"2026-10-08", "2026-10-18", "2026-10-19"} {
		lines = append(lines, fmt.Sprintf(`{"timestamp":"%sT12:00:00Z","sessionId":"s%d","costUSD":0.01,"message":{"id":"m%d","model":"claude-sonnet-4-20250514","usage":{"input_tokens":10,"output_tokens":5}}}`, day, i, i))
	}
	sourceCLIWrite(t, filepath.Join(claude, "projects/p/s.jsonl"), strings.Join(lines, "\n"))
	defaults, commands := filepath.Join(home, "defaults.json"), filepath.Join(home, "commands.json")
	sourceCLIWrite(t, defaults, `{"defaults":{"startOfWeek":"sunday"}}`)
	sourceCLIWrite(t, commands, `{"commands":{"daily":{"startOfWeek":"sunday"},"weekly":{"startOfWeek":"sunday"},"monthly":{"startOfWeek":"sunday"},"session":{"startOfWeek":"sunday"}}}`)
	weeks := func(args ...string) []string {
		var weeks []string
		for _, row := range sourceCLIJSON(t, append(args, "--sections", "weekly", "--json", "--offline", "--timezone", "UTC")...)["weekly"].([]any) {
			weeks = append(weeks, row.(map[string]any)["period"].(string))
		}
		sort.Strings(weeks)
		return weeks
	}
	monday, sunday := []string{"2026-10-05", "2026-10-12", "2026-10-19"}, []string{"2026-10-04", "2026-10-18"}
	for _, cfg := range []string{defaults, commands} {
		for _, kind := range []string{"daily", "monthly", "session"} {
			if got := weeks(kind, "--config", cfg); !reflect.DeepEqual(got, monday) {
				t.Errorf("%s --config %s: weeks %v, want %v", kind, filepath.Base(cfg), got, monday)
			}
		}
		if got := weeks("weekly", "--config", cfg); !reflect.DeepEqual(got, sunday) {
			t.Errorf("weekly --config %s: weeks %v, want %v", filepath.Base(cfg), got, sunday)
		}
	}
	for _, kind := range []string{"daily", "monthly", "session"} {
		if _, _, err := sourceCLIRun(kind, "-w", "sunday", "--json", "--offline"); err == nil {
			t.Errorf("%s accepted -w", kind)
		}
	}
	if got := weeks("weekly", "-w", "sunday"); !reflect.DeepEqual(got, sunday) {
		t.Errorf("weekly -w sunday: weeks %v, want %v", got, sunday)
	}
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

// A configured project filters only Claude, and only in unified daily
// reports and their sections, whether it comes from the global sections or
// Claude's own, which win; other sources' projects and other reports keep
// every row. --project still filters every source.
func TestUnifiedConfigProjectFiltersOnlyClaudeDaily(t *testing.T) {
	filtered := map[string][2]float64{"2026-01-04 claude": {10, 0.01}, "2026-01-05 claude": {10, 0.01}, "2026-01-04 codex": {900, 0}, "2026-01-05 codex": {1000, 0}}
	for _, tc := range []struct {
		config string
		args   []string
		key    string
		want   map[string][2]float64
	}{
		{`{"defaults": {"timezone": "UTC"}, "commands": {"daily": {"project": "project-a"}}}`, []string{"daily"}, "daily", filtered},
		{`{"defaults": {"timezone": "UTC"}, "commands": {"daily": {"project": "project-a"}}, "claude": {"defaults": {"timezone": "UTC"}}}`, []string{"daily"}, "daily", filtered},
		{`{"defaults": {"timezone": "UTC"}, "commands": {"daily": {"project": "project-b"}}, "claude": {"commands": {"daily": {"project": "project-a"}}}}`, []string{"daily"}, "daily", filtered},
		{`{"defaults": {"timezone": "UTC", "project": "project-a"}}`, []string{"daily", "--sections", "monthly"}, "monthly", map[string][2]float64{"2026-01 claude": {20, 0.02}, "2026-01 codex": {1900, 0}}},
		{`{"defaults": {"timezone": "UTC"}, "codex": {"defaults": {"project": "project-a"}}}`, []string{"daily"}, "daily", map[string][2]float64{"2026-01-04 claude": {10, 0.01}, "2026-01-05 claude": {1010, 0.03}, "2026-01-04 codex": {900, 0}, "2026-01-05 codex": {1000, 0}}},
		{`{"defaults": {"timezone": "UTC", "project": "project-a"}}`, []string{"weekly"}, "weekly", map[string][2]float64{"2025-12-29 claude": {10, 0.01}, "2025-12-29 codex": {900, 0}, "2026-01-05 claude": {1010, 0.03}, "2026-01-05 codex": {1000, 0}}},
		{`{"defaults": {"timezone": "UTC"}, "claude": {"defaults": {"project": "project-a"}}}`, []string{"monthly"}, "monthly", map[string][2]float64{"2026-01 claude": {1020, 0.04}, "2026-01 codex": {1900, 0}}},
		{`{"defaults": {"timezone": "UTC"}, "claude": {"defaults": {"project": "project-a"}}}`, []string{"session"}, "session", map[string][2]float64{"session-a claude": {20, 0.02}, "session-b claude": {1000, 0.02}, "2026/01/04/before codex": {900, 0}, "2026/01/05/in codex": {1000, 0}}},
		{`{"defaults": {"timezone": "UTC"}, "claude": {"defaults": {"project": "project-b"}}}`, []string{"weekly", "--project", "project-a"}, "weekly", map[string][2]float64{"2025-12-29 claude": {10, 0.01}, "2026-01-05 claude": {10, 0.01}}},
	} {
		_, cfg := sourceConfigFixture(t, tc.config)
		p := sourceCLIJSON(t, append(tc.args, "--json", "--offline", "--by-agent", "--config", cfg)...)
		assertSourceRows(t, tc.config+" "+strings.Join(tc.args, " "), sourceRows(p, tc.key), tc.want)
	}
}

// Upstream's config has no id or sessionId either: a configured one neither
// looks up a session nor filters a report.
func TestConfigSessionIDsAreIgnored(t *testing.T) {
	sessions := map[string][2]float64{"session-a claude": {20, 0.02}, "session-b claude": {1000, 0.02}, "2026/01/04/before codex": {900, 0}, "2026/01/05/in codex": {1000, 0}}
	for _, config := range []string{
		`{"defaults": {"timezone": "UTC", "id": "session-a"}}`,
		`{"defaults": {"timezone": "UTC"}, "commands": {"session": {"sessionId": "session-a"}}}`,
		`{"defaults": {"timezone": "UTC"}, "codex": {"defaults": {"id": "zzz"}}}`,
		`{"defaults": {"timezone": "UTC"}, "commands": {"codex:session": {"id": "zzz"}}}`,
	} {
		_, cfg := sourceConfigFixture(t, config)
		p := sourceCLIJSON(t, "session", "--json", "--offline", "--by-agent", "--config", cfg)
		assertSourceRows(t, config+" session", sourceRows(p, "session"), sessions)
		for _, args := range [][]string{{"claude", "session"}, {"codex", "session"}, {"claude", "daily"}} {
			p := sourceCLIJSON(t, append(args, "--json", "--offline", "--config", cfg)...)
			key := map[string]string{"session": "sessions", "daily": "daily"}[args[1]]
			if rows, _ := p[key].([]any); len(rows) != 2 {
				t.Errorf("%s %v: %d rows, want 2: %v", config, args, len(rows), p)
			}
		}
	}
}

// Upstream reads a configured project, instances and projectAliases only for
// claude daily, and a unified daily report gives its project to Claude; every
// other report keeps all rows, not grouped or labeled by project.
func TestConfigProjectOptionsApplyOnlyToClaudeDaily(t *testing.T) {
	_, cfg := sourceConfigFixture(t, `{"defaults": {"timezone": "UTC", "project": "project-a", "instances": true, "projectAliases": "project-a=Alpha"}}`)
	for _, tc := range []struct {
		args  []string
		key   string
		rows  int
		total float64
	}{
		{[]string{"claude", "daily"}, "projects", 1, 30},
		{[]string{"claude", "weekly"}, "weekly", 1, 1035},
		{[]string{"claude", "monthly"}, "monthly", 1, 1035},
		{[]string{"claude", "session"}, "sessions", 2, 1035},
		{[]string{"codex", "daily"}, "daily", 2, 2500},
		{[]string{"daily"}, "daily", 2, 2530},
		{[]string{"weekly"}, "weekly", 2, 3535},
	} {
		p := sourceCLIJSON(t, append(tc.args, "--json", "--offline", "--config", cfg)...)
		rows, _ := p[tc.key].([]any)
		if projects, ok := p[tc.key].(map[string]any); ok {
			rows = projects["project-a"].([]any)[:1]
		}
		if len(rows) != tc.rows || p["totals"].(map[string]any)["totalTokens"] != tc.total {
			t.Errorf("%v: %d rows, totals %v; want %d rows, %v tokens", tc.args, len(rows), p["totals"], tc.rows, tc.total)
		}
	}
	for args, alias := range map[string]bool{"claude daily": true, "claude weekly --instances": false} {
		out, _, err := sourceCLIRun(append(strings.Fields(args), "--offline", "--config", cfg)...)
		if err != nil || strings.Contains(out, "Alpha") != alias {
			t.Errorf("%s: alias shown %v, want %v: %v\n%s", args, !alias, alias, err, out)
		}
	}
}

// A project filter reads only that project's transcripts, as upstream scans
// only its directory, so a request copied into another project that would
// win deduplication there still counts for the filtered one.
func TestClaudeProjectFilterDedupesWithinTheProject(t *testing.T) {
	home := isolateSourceHomes(t)
	claude := filepath.Join(home, "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	line := `{"timestamp":"2026-01-05T12:00:00Z","sessionId":"s-%s","requestId":"req_%d","message":{"id":"msg_%d","model":"claude-sonnet-4-20250514","usage":{"input_tokens":%d,"output_tokens":%d}}}`
	sourceCLIWrite(t, filepath.Join(claude, "projects/wa/s-a.jsonl"), fmt.Sprintf(line, "a", 1, 1, 1000, 100))
	sourceCLIWrite(t, filepath.Join(claude, "projects/wb/s-b.jsonl"), fmt.Sprintf(line, "b", 1, 1, 1000, 100)+"\n"+fmt.Sprintf(line, "b", 2, 2, 50, 5))
	cfg := filepath.Join(home, "ccusage.json")
	sourceCLIWrite(t, cfg, `{"defaults": {"timezone": "UTC"}, "claude": {"defaults": {"project": "wb"}}}`)
	for _, args := range [][]string{{"claude", "daily", "--project", "wb"}, {"daily", "--config", cfg}, {"claude", "daily", "--config", cfg}} {
		p := sourceCLIJSON(t, append(args, "--json", "--offline", "--timezone", "UTC")...)
		if totals := p["totals"].(map[string]any); totals["inputTokens"] != 1050.0 || totals["outputTokens"] != 105.0 {
			t.Errorf("%v: totals %v, want 1050 input and 105 output tokens", args, totals)
		}
	}
}
