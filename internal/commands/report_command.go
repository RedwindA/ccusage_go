package commands

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/RedwindA/ccusage_go/internal/calculator"
	"github.com/RedwindA/ccusage_go/internal/config"
	"github.com/RedwindA/ccusage_go/internal/pricing"
	"github.com/RedwindA/ccusage_go/internal/reports"
	"github.com/RedwindA/ccusage_go/internal/types"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
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
	registerReportFlags(cmd, f, kind)
	if agent != "" && agent != "codex" && agent != "opencode" {
		f.startOfWeek = "sunday"
		cmd.Flags().Lookup("start-of-week").DefValue = "sunday"
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error { return runReport(cmd, kind, agent, f) }
	return cmd
}

func registerReportFlags(cmd *cobra.Command, f *reportFlags, kind string) {
	p := cmd.Flags()
	p.StringVarP(&f.format, "format", "f", "table", "Output format: table, json, csv")
	p.BoolVarP(&f.json, "json", "j", false, "Output structured JSON")
	p.StringVar(&f.dataPath, "data-path", "", "Explicit source directory (root reports: Claude data)")
	p.StringVarP(&f.timezone, "timezone", "z", "", "Report timezone (default: local)")
	p.StringVarP(&f.since, "since", "s", "", "Include dates since YYYY-MM-DD or YYYYMMDD")
	p.StringVarP(&f.until, "until", "u", "", "Include dates through YYYY-MM-DD or YYYYMMDD")
	p.IntVar(&f.last, "last", 0, "Include the most recent N calendar periods")
	p.StringVarP(&f.order, "order", "o", "", "Sort order: asc or desc")
	p.StringVarP(&f.mode, "mode", "m", "auto", "Cost mode: auto, calculate, display")
	p.StringVar(&f.speed, "speed", "auto", "Codex pricing tier: auto, standard, fast")
	p.StringVarP(&f.startOfWeek, "start-of-week", "w", "monday", "First weekday for weekly reports")
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
		p.StringVarP(&f.sessionID, "id", "i", "", "Show Claude session details by ID")
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
	for name, value := range cfg.Options(kind, agent) {
		name = optionFlag(name)
		flag := cmd.Flags().Lookup(name)
		if flag == nil || explicitReportOption(cmd, name) {
			continue
		}
		if err := flag.Value.Set(fmt.Sprint(value)); err != nil {
			return fmt.Errorf("config %s: %w", name, err)
		}
	}
	loc := time.Local
	if f.timezone != "" {
		loc, err = time.LoadLocation(f.timezone)
		if err != nil {
			return fmt.Errorf("invalid timezone: %w", err)
		}
	}
	since, err := reports.NormalizeDate(f.since)
	if err != nil {
		return err
	}
	until, err := reports.NormalizeDate(f.until)
	if err != nil {
		return err
	}
	weekdays := map[string]time.Weekday{"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday, "thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday}
	start, ok := weekdays[strings.ToLower(f.startOfWeek)]
	if !ok {
		return fmt.Errorf("invalid --start-of-week %q", f.startOfWeek)
	}
	if f.last != 0 || cmd.Flags().Changed("last") {
		if since != "" || until != "" {
			return fmt.Errorf("--last cannot be combined with --since or --until")
		}
		since, err = reports.LastSince(kind, f.last, time.Now().In(loc), start)
		if err != nil {
			return err
		}
	}
	if f.date != "" {
		since, err = reports.NormalizeDate(f.date)
		if err != nil {
			return err
		}
		until = since
	}
	if f.month != "" {
		t, e := time.ParseInLocation("2006-01", f.month, loc)
		if e != nil {
			return fmt.Errorf("invalid month: %w", e)
		}
		since = t.Format("2006-01-02")
		until = t.AddDate(0, 1, -1).Format("2006-01-02")
	}
	if f.week != "" {
		date, e := reports.NormalizeDate(f.week)
		if e != nil {
			return e
		}
		t, _ := time.ParseInLocation("2006-01-02", date, loc)
		t = reports.WeekStart(t, start)
		since = t.Format("2006-01-02")
		until = t.AddDate(0, 0, 6).Format("2006-01-02")
	}
	if since != "" && until != "" && since > until {
		return fmt.Errorf("--since must not be after --until")
	}
	if f.order == "" {
		f.order = "asc"
		if kind == "model" || kind == "workspace" || kind == "session" && agent == "" {
			f.order = "desc"
		}
	}
	if f.order != "asc" && f.order != "desc" {
		return fmt.Errorf("invalid --order %q", f.order)
	}
	if f.mode != "auto" && f.mode != "calculate" && f.mode != "display" {
		return fmt.Errorf("invalid --mode %q", f.mode)
	}
	if f.debugSamples < 0 {
		return fmt.Errorf("--debug-samples must be nonnegative")
	}
	if f.speed != "auto" && f.speed != "standard" && f.speed != "fast" {
		return fmt.Errorf("invalid --speed %q", f.speed)
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
	f.until = until
	entries, err := loadReportEntries(cmd, agent, f, loc, cfg)
	if err != nil {
		return err
	}
	service := pricing.NewService()
	service.SetOffline(f.offline && !f.noOffline)
	service.SetOverrides(cfg.PricingOverrides(kind, agent))
	debugPricing(cmd, service, entries, f)
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
	calc := calculator.New(service)
	calc.SetMode(f.mode)
	calc.SetSpeed(f.speed)
	entries, err = calc.CalculateCosts(cmd.Context(), entries)
	if err != nil {
		return err
	}
	o := reports.Options{Kind: kind, Agent: agent, Since: since, Until: until, Project: f.project, SessionID: f.sessionID, SessionName: f.sessionName, Order: f.order, Instances: f.instances, ByAgent: f.byAgent, NoCost: f.noCost, Breakdown: f.breakdown, Compact: f.compact, Location: loc, WeekStart: start}
	configureTableOutput(cmd, f, &o)
	detected := map[string]bool{}
	for _, entry := range entries {
		if entry.Agent != "" && !detected[entry.Agent] {
			detected[entry.Agent] = true
			o.DetectedAgents = append(o.DetectedAgents, entry.Agent)
		}
	}
	if agent == "claude" && kind == "session" && (f.sessionID != "" || f.sessionName != "" && f.format == "table") {
		return writeClaudeSessionDetail(cmd, entries, o, f)
	}
	payload := map[string]any{}
	for _, k := range kinds {
		o.Kind = k
		rows := reports.Aggregate(entries, o)
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

func debugPricing(cmd *cobra.Command, service *pricing.Service, entries []types.UsageEntry, f *reportFlags) {
	if !f.debug || f.noCost {
		return
	}
	calc := calculator.New(service)
	calc.SetMode("calculate")
	calc.SetSpeed(f.speed)
	count := 0
	for _, entry := range entries {
		if !entry.HasCost && entry.Cost == 0 {
			continue
		}
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
