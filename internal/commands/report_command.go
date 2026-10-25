package commands

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/RedwindA/ccusage_go/internal/calculator"
	"github.com/RedwindA/ccusage_go/internal/config"
	"github.com/RedwindA/ccusage_go/internal/reports"
	"github.com/RedwindA/ccusage_go/internal/types"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type reportFlags struct {
	debugSamples                                                                                                                             int
	format, dataPath, timezone, since, until, date, month, week, project, sessionID, sessionName                                             string
	order, mode, speed, startOfWeek, configPath, sections, piPath, openclawPath, jq, projectAliases                                          string
	last                                                                                                                                     int
	json, noCost, breakdown, compact, instances, byAgent, all, offline, noOffline, noColor, color, debug, singleThread, allUsers, responsive bool
}

// NewReportCommand is shared by root and source-focused reports.
func NewReportCommand(kind, agent string) *cobra.Command {
	f := &reportFlags{}
	cmd := &cobra.Command{Use: kind, Short: "Show " + kind + " usage across detected coding agents", Args: cobra.NoArgs}
	if agent != "" {
		cmd.Short = "Show " + agent + " " + kind + " usage"
	}
	registerReportFlags(cmd, f, kind, agent)
	if agent != "" && agent != "codex" && agent != "opencode" {
		f.startOfWeek = "sunday"
		cmd.Flags().Lookup("start-of-week").DefValue = "sunday"
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error { return runReport(cmd, kind, agent, f) }
	return cmd
}

func registerReportFlags(cmd *cobra.Command, f *reportFlags, kind, agent string) {
	bindReportFlags(cmd.Flags(), f, kind, agent)
	completeValues(cmd, "format", "table", "json", "csv")
	completeValues(cmd, "order", "asc", "desc")
	completeValues(cmd, "mode", "auto", "calculate", "display")
	completeValues(cmd, "speed", "auto", "standard", "flex", "fast")
	completeValues(cmd, "start-of-week", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday")
	completeList(cmd, "sections", "daily", "weekly", "monthly", "session")
	completeDir(cmd, "data-path", "pi-path", "openclaw-path", "open-claw-path")
	completeJSONFile(cmd, "config")
}

func bindReportFlags(p *pflag.FlagSet, f *reportFlags, kind, agent string) {
	p.StringVarP(&f.format, "format", "f", "table", "Output format: table, json, csv")
	p.BoolVarP(&f.json, "json", "j", false, "Output structured JSON")
	p.StringVar(&f.dataPath, "data-path", "", "Explicit source directory (root reports: Claude data)")
	p.StringVarP(&f.timezone, "timezone", "z", "", "Report timezone (default: local)")
	p.StringVarP(&f.since, "since", "s", "", "Include dates since YYYY-MM-DD or YYYYMMDD")
	p.StringVarP(&f.until, "until", "u", "", "Include dates through YYYY-MM-DD or YYYYMMDD")
	p.IntVar(&f.last, "last", 0, "Include the most recent N calendar periods")
	p.StringVarP(&f.order, "order", "o", "", "Sort order: asc or desc")
	p.StringVarP(&f.mode, "mode", "m", "auto", "Cost mode: auto, calculate, display")
	p.StringVar(&f.speed, "speed", "auto", "Codex pricing tier: auto, standard, flex, fast")
	// Upstream reads the start of week only for weekly reports; the weekly
	// section of another unified report starts its weeks on Monday.
	if agent != "" || kind == "weekly" {
		p.StringVarP(&f.startOfWeek, "start-of-week", "w", "monday", "First weekday for weekly reports")
	} else {
		f.startOfWeek = "monday"
	}
	p.BoolVarP(&f.breakdown, "breakdown", "b", false, "Include model breakdown rows")
	p.BoolVar(&f.noCost, "no-cost", false, "Remove cost fields and columns")
	p.BoolVar(&f.compact, "compact", false, "Use compact tables")
	if kind == "session" {
		p.BoolVar(&f.instances, "instances", false, "Group by Claude project")
	} else {
		p.BoolVarP(&f.instances, "instances", "i", false, "Group by Claude project")
	}
	p.StringVarP(&f.project, "project", "p", "", "Filter by project directory name")
	p.StringVar(&f.projectAliases, "project-aliases", "", "Project display aliases (project=label,other=label)")
	p.StringVar(&f.sessionID, "session-id", "", "Filter by session ID")
	p.StringVar(&f.sessionName, "session-name", "", "Filter by session name")
	p.StringVar(&f.configPath, "config", "", "Path to JSON config")
	p.StringVar(&f.sections, "sections", "", "Comma-separated unified report sections")
	p.BoolVar(&f.byAgent, "by-agent", false, "Show agent breakdowns in unified reports")
	p.BoolVar(&f.all, "all", false, "Include all detected agents")
	p.BoolVar(&f.allUsers, "all-users", false, "Scan all system users (Linux root only)")
	p.StringVar(&f.piPath, "pi-path", "", "Comma-separated pi session directories")
	p.StringVar(&f.openclawPath, "openclaw-path", "", "OpenClaw state directory")
	p.StringVar(&f.openclawPath, "open-claw-path", "", "OpenClaw state directory (upstream spelling)")
	p.StringVar(&f.jq, "jq", "", "Filter JSON with jq")
	p.BoolVarP(&f.offline, "offline", "O", false, "Use embedded pricing without network")
	p.BoolVar(&f.noOffline, "no-offline", false, "Refresh pricing from the network")
	p.BoolVar(&f.noColor, "no-color", false, "Disable colors")
	p.BoolVar(&f.color, "color", false, "Enable colors")
	p.BoolVar(&f.debug, "debug", false, "Show load diagnostics")
	p.IntVar(&f.debugSamples, "debug-samples", 5, "Maximum pricing discrepancy samples in debug output")
	p.BoolVar(&f.singleThread, "single-thread", false, "Load sources sequentially")
	p.BoolVar(&f.responsive, "responsive", true, "Use compact layout on narrow terminals")
	if kind == "daily" {
		p.StringVarP(&f.date, "date", "d", "", "Report only this date")
	}
	if kind == "monthly" {
		p.StringVar(&f.month, "month", "", "Report only this month (YYYY-MM)")
	}
	if kind == "weekly" {
		p.StringVar(&f.week, "week", "", "Report week containing this date")
	}
	if kind == "session" {
		p.StringVarP(&f.sessionID, "id", "i", "", "Show session details by ID")
	}
}

func runReport(cmd *cobra.Command, kind, agent string, f *reportFlags) error {
	cfg, err := config.Load(f.configPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(kind, agent); err != nil {
		return err
	}
	// Sources with config sections of their own read the command line again.
	cli := *f
	if err := applyConfig(cmd, cmd.Flags(), kind, agent, cfg.Options(kind, agent)); err != nil {
		return err
	}
	weekdays := map[string]time.Weekday{"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday, "thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday}
	start, ok := weekdays[strings.ToLower(f.startOfWeek)]
	if !ok {
		return fmt.Errorf("invalid --start-of-week %q", f.startOfWeek)
	}
	unified, err := newSourceScope(cmd, kind, f, start)
	if err != nil {
		return err
	}
	ordered := f.order != ""
	if !ordered {
		f.order = defaultOrder(kind, agent)
	}
	if f.order != "asc" && f.order != "desc" {
		return fmt.Errorf("invalid --order %q", f.order)
	}
	if err := checkCostFlags(f); err != nil {
		return err
	}
	if f.debugSamples < 0 {
		return fmt.Errorf("--debug-samples must be nonnegative")
	}
	if f.json || f.jq != "" {
		f.format = "json"
	}
	if f.format != "json" && f.format != "csv" && f.format != "table" {
		return fmt.Errorf("invalid --format %q", f.format)
	}
	kinds := []string{kind}
	if f.sections != "" {
		if agent != "" {
			return fmt.Errorf("--sections is only supported by unified reports")
		}
		kinds = []string{kind}
		seen := map[string]bool{kind: true}
		validSections := 0
		for _, k := range strings.Split(f.sections, ",") {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			validSections++
			if k != "daily" && k != "weekly" && k != "monthly" && k != "session" {
				return fmt.Errorf("invalid section %q", k)
			}
			if !seen[k] {
				kinds = append(kinds, k)
				seen[k] = true
			}
		}
		if validSections == 0 {
			return fmt.Errorf("--sections requires at least one report")
		}
	}
	if f.all {
		agent = ""
	}
	if kind == "session" && agent == "" && cmd.Flags().Changed("id") {
		if f.allUsers || f.byAgent || f.sections != "" {
			return fmt.Errorf("session --id cannot be combined with --all-users, --by-agent or --sections")
		}
		agent = "claude"
	}
	unified.overrides = cfg.PricingOverrides(kind, agent)
	scopes := sourceScopes{"": unified}
	if agent == "" {
		if err := scopes.addSources(cmd, kind, cli, cfg, start); err != nil {
			return err
		}
	}
	prefetch := startPricing(cmd, scopes.offline())
	// Unified sessions are dated by their latest activity and totaled whole,
	// so they read Claude usage from before --since; the sections beside
	// them still skip the Claude sessions last written before it.
	sessions := agent == "" && slices.Contains(kinds, "session")
	bounded := !sessions || len(kinds) > 1
	// Upstream totals unified daily, weekly and monthly sections and claude
	// daily from its Claude daily loader, which dedupes differently.
	daily := agent == "" || agent == "claude" && kind == "daily"
	scopes.price(prefetch)
	entries, sessionEntries, detected, err := loadReportEntries(cmd, agent, scopes, cfg, bounded, daily, sessions)
	if err != nil {
		return err
	}
	if sessions {
		debugPricing(cmd, sessionEntries, f, scopes)
	} else {
		debugPricing(cmd, entries, f, scopes)
	}
	if agent == "claude" && kind == "session" && f.sessionID != "" {
		for i := range entries {
			if entries[i].Raw == nil {
				entries[i].Raw = map[string]any{}
			}
			recorded := 0.0
			if entries[i].HasCost {
				recorded = entries[i].Cost
			}
			entries[i].Raw["recorded_cost"] = recorded
		}
	}
	for _, view := range [][]types.UsageEntry{entries, sessionEntries} {
		for i := range view {
			if _, err := scopes.of(view[i].Agent).calc.CalculateCosts(cmd.Context(), view[i:i+1]); err != nil {
				return err
			}
		}
	}
	o := reports.Options{Kind: kind, Agent: agent, Since: unified.since, Until: unified.until, Project: unified.project, SessionID: f.sessionID, SessionName: f.sessionName, Order: f.order, Instances: f.instances, ByAgent: f.byAgent, NoCost: f.noCost, Breakdown: f.breakdown, Compact: f.compact, Location: unified.loc, WeekStart: start, Sources: scopes.windows(), DetectedAgents: detected}
	configureTableOutput(cmd, f, &o)
	if agent == "codex" && kind == "session" && (f.sessionID != "" || explicitReportOption(cmd, "id")) {
		row, err := resolveCodexSession(entries, o, f.sessionID)
		if err != nil {
			return err
		}
		if f.format == "json" {
			return writeCodexSessionDetail(cmd, row, o, f)
		}
		o.SessionID = row.Period
	}
	if agent == "claude" && kind == "session" && (f.sessionID != "" || f.sessionName != "" && f.format == "table") {
		return writeClaudeSessionDetail(cmd, entries, o, f)
	}
	payload := map[string]any{}
	for _, k := range kinds {
		o.Kind, o.Order = k, f.order
		// Without --order or a configured order, a section sorts as its
		// own report does.
		if !ordered && k != kind {
			o.Order = defaultOrder(k, "")
		}
		view := entries
		if k == "session" && sessions {
			view = sessionEntries
		}
		rows := reports.Aggregate(view, o)
		if f.projectAliases != "" && f.format != "json" {
			var aliases map[string]string
			if strings.HasPrefix(strings.TrimSpace(f.projectAliases), "{") {
				if err := json.Unmarshal([]byte(f.projectAliases), &aliases); err != nil {
					return fmt.Errorf("invalid --project-aliases JSON: %w", err)
				}
			} else {
				aliases = map[string]string{}
				for _, pair := range strings.Split(f.projectAliases, ",") {
					key, value, ok := strings.Cut(pair, "=")
					if ok {
						aliases[strings.TrimSpace(key)] = strings.TrimSpace(value)
					}
				}
			}
			for _, r := range rows {
				if label, ok := aliases[r.Project]; ok {
					r.Project = label
				}
			}
		}
		switch f.format {
		case "json":
			key := reports.Key(k)
			if k == "session" && agent != "" {
				key = "sessions"
			}
			if agent != "" {
				focused := reports.FocusedJSONRows(rows, o)
				if agent == "claude" && k == "daily" && f.instances && len(rows) > 0 {
					projects := map[string][]map[string]any{}
					for i, row := range rows {
						projects[row.Project] = append(projects[row.Project], focused[i])
					}
					payload["projects"] = projects
				} else {
					payload[key] = focused
				}
			} else {
				payload[key] = reports.JSONRows(rows, o)
			}
			if k == kind {
				if agent != "" {
					payload["totals"] = reports.FocusedTotals(rows, o)
				} else {
					payload["totals"] = reports.Totals(rows)
				}
			}
		case "csv":
			if err := reports.WriteCSV(cmd.OutOrStdout(), rows, o); err != nil {
				return err
			}
		default:
			if err := writeUsageTable(cmd, rows, o); err != nil {
				return err
			}
		}
	}
	if f.format == "json" {
		return writeReportJSON(cmd, payload, f)
	}
	return nil
}

// defaultOrder is the order a kind report of agent lists its rows in
// without --order: unified sessions and model and workspace reports by cost,
// highest first, and the others by period, oldest first.
func defaultOrder(kind, agent string) string {
	if kind == "model" || kind == "workspace" || kind == "session" && agent == "" {
		return "desc"
	}
	return "asc"
}

func writeUsageTable(cmd *cobra.Command, rows []*reports.Row, o reports.Options) error {
	if err := reports.WriteTable(cmd.OutOrStdout(), rows, o); err != nil {
		return err
	}
	if o.Compact && len(rows) > 0 {
		_, err := fmt.Fprintln(cmd.ErrOrStderr(), "\nRunning in Compact Mode\nExpand terminal width to see cache metrics and total tokens")
		return err
	}
	return nil
}

// Use the command's output stream, so redirected reports do not inherit the
// terminal's colors. COLUMNS also makes a chosen layout reproducible in scripts.
func configureTableOutput(cmd *cobra.Command, f *reportFlags, o *reports.Options) {
	_, noColor := os.LookupEnv("NO_COLOR")
	_, forceColor := os.LookupEnv("FORCE_COLOR")
	o.Color = (f.color || forceColor) && !f.noColor && !noColor
	o.TerminalWidth = 120
	if file, ok := cmd.OutOrStdout().(*os.File); ok && term.IsTerminal(file.Fd()) {
		if width, _, err := term.GetSize(file.Fd()); err == nil && width > 0 {
			o.TerminalWidth = width
		}
		if !f.noColor && !noColor {
			o.Color = true
		}
	}
	if width, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && width > 0 {
		o.TerminalWidth = width
	}
	if f.responsive {
		o.Compact = o.Compact || o.TerminalWidth < 100
	} else {
		o.TerminalWidth = int(^uint(0) >> 1)
	}
}

func writeReportJSON(cmd *cobra.Command, payload any, f *reportFlags) error {
	if f.noCost {
		reports.StripCosts(payload)
	}
	if f.jq != "" {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		jq := exec.CommandContext(cmd.Context(), "jq", f.jq)
		jq.Stdin = strings.NewReader(string(data))
		jq.Stdout = cmd.OutOrStdout()
		jq.Stderr = cmd.ErrOrStderr()
		if err := jq.Run(); err != nil {
			return fmt.Errorf("jq: %w", err)
		}
		return nil
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// applyConfig sets each config option of the kind report of agent on flags
// unless cmd's command line sets it. Upstream's config has no period or
// session selectors, so --last, --date, --month, --week, --id and
// --session-id come only from the command line. It reads project, instances
// and projectAliases only for claude daily, and a unified daily report passes
// its project on to Claude.
func applyConfig(cmd *cobra.Command, flags *pflag.FlagSet, kind, agent string, options map[string]any) error {
	for name, value := range options {
		switch name = optionFlag(name); name {
		case "last", "date", "month", "week", "id", "session-id":
			continue
		case "project":
			if kind != "daily" || agent != "claude" && agent != "" {
				continue
			}
		case "instances", "project-aliases":
			if kind != "daily" || agent != "claude" {
				continue
			}
		}
		flag := flags.Lookup(name)
		if flag == nil || explicitReportOption(cmd, name) {
			continue
		}
		if err := flag.Value.Set(fmt.Sprint(value)); err != nil {
			return fmt.Errorf("config %s: %w", name, err)
		}
	}
	return nil
}

func checkCostFlags(f *reportFlags) error {
	if f.mode != "auto" && f.mode != "calculate" && f.mode != "display" {
		return fmt.Errorf("invalid --mode %q", f.mode)
	}
	if f.speed != "auto" && f.speed != "standard" && f.speed != "flex" && f.speed != "fast" {
		return fmt.Errorf("invalid --speed %q", f.speed)
	}
	return nil
}

func optionFlag(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('-')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func explicitReportOption(cmd *cobra.Command, name string) bool {
	if cmd.Flags().Changed(name) {
		return true
	}
	for _, aliases := range [][]string{{"open-claw-path", "openclaw-path"}, {"id", "session-id"}, {"json", "format"}, {"offline", "no-offline"}, {"color", "no-color"}} {
		for _, alias := range aliases {
			if alias == name {
				for _, other := range aliases {
					if cmd.Flags().Changed(other) {
						return true
					}
				}
			}
		}
	}
	return false
}

func debugPricing(cmd *cobra.Command, entries []types.UsageEntry, f *reportFlags, scopes sourceScopes) {
	if !f.debug || f.noCost {
		return
	}
	count := 0
	for _, entry := range entries {
		if !entry.HasCost && entry.Cost == 0 {
			continue
		}
		scope := scopes.of(entry.Agent)
		calc := calculator.New(scope.service)
		calc.SetMode("calculate")
		calc.SetSpeed(scope.f.speed)
		candidate := entry
		candidate.Raw = make(map[string]any, len(entry.Raw))
		for key, value := range entry.Raw {
			candidate.Raw[key] = value
		}
		calculated, err := calc.CalculateCosts(cmd.Context(), []types.UsageEntry{candidate})
		if err != nil || math.Abs(calculated[0].Cost-entry.Cost) < 1e-9 {
			continue
		}
		if count < f.debugSamples {
			fmt.Fprintf(cmd.ErrOrStderr(), "Pricing mismatch %q: recorded $%.6f, calculated $%.6f\n", entry.Model, entry.Cost, calculated[0].Cost)
		}
		count++
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Pricing discrepancies: %d\n", count)
}
