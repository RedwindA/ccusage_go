package commands

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/RedwindA/ccusage_go/internal/calculator"
	"github.com/RedwindA/ccusage_go/internal/config"
	"github.com/RedwindA/ccusage_go/internal/monitor"
	"github.com/RedwindA/ccusage_go/internal/output"
	"github.com/RedwindA/ccusage_go/internal/pricing"
	"github.com/RedwindA/ccusage_go/internal/reports"
	"github.com/RedwindA/ccusage_go/internal/types"
	"github.com/spf13/cobra"
)

func NewBlocksCommand() *cobra.Command {
	f := &reportFlags{}
	var active, recent, live, gradient bool
	var tokenLimit string
	var length float64
	var interval int
	cmd := &cobra.Command{Use: "blocks", Short: "Show Claude billing windows and live usage", Args: cobra.NoArgs}
	registerReportFlags(cmd, f, "blocks")
	cmd.Flags().BoolVarP(&active, "active", "a", false, "Show active block")
	cmd.Flags().BoolVarP(&recent, "recent", "r", false, "Show blocks from the last three days")
	cmd.Flags().BoolVar(&live, "live", false, "Run the live usage dashboard")
	cmd.Flags().BoolVar(&gradient, "gradient", true, "Use gradient progress bars in live mode")
	cmd.Flags().StringVarP(&tokenLimit, "token-limit", "t", "", "Token limit or max")
	cmd.Flags().Float64VarP(&length, "session-length", "n", 5, "Billing window length in hours")
	cmd.Flags().IntVar(&interval, "refresh-interval", 1, "Live refresh interval in seconds")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(f.configPath)
		if err != nil {
			return err
		}
		if err := cfg.Validate("blocks", "claude"); err != nil {
			return err
		}
		for name, value := range cfg.Options("blocks", "claude") {
			name = optionFlag(name)
			flag := cmd.Flags().Lookup(name)
			if flag != nil && !explicitReportOption(cmd, name) {
				if err := flag.Value.Set(fmt.Sprint(value)); err != nil {
					return fmt.Errorf("config %s: %w", name, err)
				}
			}
		}
		if length <= 0 || math.IsNaN(length) || math.IsInf(length, 0) || length > float64(math.MaxInt64)/float64(time.Hour) {
			return fmt.Errorf("--session-length must be a finite positive duration")
		}
		if f.json || f.jq != "" {
			f.format = "json"
		}
		if f.format != "table" && f.format != "json" && f.format != "csv" {
			return fmt.Errorf("invalid --format %q", f.format)
		}
		loc := time.Local
		if f.timezone != "" {
			loc, err = time.LoadLocation(f.timezone)
			if err != nil {
				return err
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
		if since != "" && until != "" && since > until {
			return fmt.Errorf("--since must not be after --until")
		}
		if f.mode != "auto" && f.mode != "display" && f.mode != "calculate" {
			return fmt.Errorf("invalid --mode %q", f.mode)
		}
		if f.debugSamples < 0 {
			return fmt.Errorf("--debug-samples must be nonnegative")
		}
		if f.order != "" && f.order != "asc" && f.order != "desc" {
			return fmt.Errorf("invalid --order %q", f.order)
		}
		home, _ := os.UserHomeDir()
		paths := sourcePaths("claude", home, true)
		if live {
			existing := []string{}
			for _, path := range paths {
				if _, err := os.Stat(path); err == nil {
					existing = append(existing, path)
				}
			}
			paths = existing
		}
		if f.dataPath != "" {
			paths = splitPaths(f.dataPath, home)
		}
		entries, err := loadSource(cmd.Context(), "claude", paths, loc, f.debug)
		if err != nil {
			return err
		}
		service := pricing.NewService()
		service.SetOffline(f.offline && !f.noOffline)
		service.SetOverrides(cfg.PricingOverrides("blocks", "claude"))
		debugPricing(cmd, service, entries, f)
		calc := calculator.New(service)
		calc.SetMode(f.mode)
		entries, err = calc.CalculateCosts(cmd.Context(), entries)
		if err != nil {
			return err
		}
		blocks := calc.IdentifySessionBlocksDuration(entries, time.Duration(length*float64(time.Hour)))
		limit := calculator.GetMaxTokensFromBlocks(blocks)
		if tokenLimit != "" && tokenLimit != "max" {
			limit, err = strconv.Atoi(tokenLimit)
			if err != nil || limit < 1 {
				return fmt.Errorf("invalid --token-limit %q", tokenLimit)
			}
		}
		if live && f.format != "json" {
			if len(paths) != 1 {
				return fmt.Errorf("live monitoring requires a single --data-path")
			}
			if interval < MinRefreshIntervalSeconds {
				interval = MinRefreshIntervalSeconds
			}
			if interval > MaxRefreshIntervalSeconds {
				interval = MaxRefreshIntervalSeconds
			}
			return monitor.StartBlocksLiveMonitoring(monitor.BlocksLiveConfig{DataPath: paths[0], TokenLimit: limit, RefreshInterval: time.Duration(interval) * time.Second, SessionLength: int(length), SessionDuration: time.Duration(length * float64(time.Hour)), Calculator: calc, NoCost: f.noCost, NoColor: f.noColor, Timezone: loc, UseGradient: gradient, OptimizeMemory: true})
		}

		selected := make([]types.SessionBlock, 0, len(blocks))
		for _, block := range blocks {
			date := block.StartTime.In(loc).Format("2006-01-02")
			if since != "" && date < since || until != "" && date > until || active && !block.IsActive {
				continue
			}
			selected = append(selected, block)
		}
		if recent {
			selected = calculator.FilterRecentBlocks(selected, DefaultRecentDays)
		}
		if f.order == "desc" {
			sort.Slice(selected, func(i, j int) bool { return selected[i].StartTime.After(selected[j].StartTime) })
		}
		if f.format == "json" {
			payload := blocksJSON(selected, limit)
			if f.noCost {
				reports.StripCosts(payload)
			}
			if f.jq != "" {
				data, _ := json.Marshal(payload)
				jq := exec.CommandContext(cmd.Context(), "jq", f.jq)
				jq.Stdin = strings.NewReader(string(data))
				jq.Stdout = cmd.OutOrStdout()
				jq.Stderr = cmd.ErrOrStderr()
				return jq.Run()
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(payload)
		}
		if f.noCost || f.compact {
			rows := make([]*reports.Row, 0, len(selected))
			for _, b := range selected {
				rows = append(rows, &reports.Row{Period: b.ID, Models: b.Models, Input: b.TokenCounts.InputTokens, Output: b.TokenCounts.OutputTokens, CacheCreate: b.TokenCounts.CacheCreationInputTokens, CacheRead: b.TokenCounts.CacheReadInputTokens, Total: b.TokenCounts.GetTotal(), Cost: b.CostUSD})
			}
			o := reports.Options{Kind: "blocks", Agent: "claude", NoCost: f.noCost, Compact: f.compact}
			configureTableOutput(cmd, f, &o)
			if f.format == "csv" {
				return reports.WriteCSV(cmd.OutOrStdout(), rows, o)
			}
			return writeUsageTable(cmd, rows, o)
		}
		if f.format == "csv" {
			formatter := output.NewFormatter(output.FormatterOptions{Format: "csv"})
			out, err := formatter.FormatCSV(formatBlocksAsCSV(selected))
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), out)
			return err
		}
		formatter := output.NewTableWriterFormatter(f.noColor)
		formatter.SetTimezone(loc)
		out := formatter.FormatBlocksReport(selected, limit)
		if active && len(selected) == 1 {
			out = formatActiveBlockDetail(selected[0], limit, f.noColor, loc)
		}
		_, err = fmt.Fprint(cmd.OutOrStdout(), out)
		return err
	}
	return cmd
}

func blocksJSON(blocks []types.SessionBlock, limit int) map[string]any {
	rows := make([]map[string]any, 0, len(blocks))
	var totalTokens int
	var totalCost float64
	for _, b := range blocks {
		r := map[string]any{"id": b.ID, "startTime": b.StartTime, "endTime": b.EndTime, "actualEndTime": b.ActualEndTime, "isActive": b.IsActive, "isGap": b.IsGap, "entries": len(b.Entries), "tokenCounts": map[string]any{"inputTokens": b.TokenCounts.InputTokens, "outputTokens": b.TokenCounts.OutputTokens, "cacheCreationInputTokens": b.TokenCounts.CacheCreationInputTokens, "cacheReadInputTokens": b.TokenCounts.CacheReadInputTokens}, "totalTokens": b.TokenCounts.GetTotal(), "costUSD": b.CostUSD, "models": b.Models}
		if rate := calculator.CalculateBurnRate(b); rate != nil {
			r["burnRate"] = map[string]any{"tokensPerMinute": rate.TokensPerMinute, "tokensPerMinuteForIndicator": rate.TokensPerMinuteForIndicator, "costPerHour": rate.CostPerHour}
		}
		if p := calculator.ProjectBlockUsage(b); p != nil {
			r["projection"] = map[string]any{"totalTokens": p.TotalTokens, "totalCost": p.TotalCost, "remainingMinutes": p.RemainingMinutes}
		}
		if limit > 0 {
			r["tokenLimit"] = limit
		}
		rows = append(rows, r)
		totalTokens += b.TokenCounts.GetTotal()
		totalCost += b.CostUSD
	}
	return map[string]any{"blocks": rows, "totals": map[string]any{"totalTokens": totalTokens, "totalCost": totalCost}}
}
