package commands

import (
	"fmt"
	"sort"
	"time"

	"github.com/RedwindA/ccusage_go/internal/reports"
	"github.com/RedwindA/ccusage_go/internal/types"
	"github.com/spf13/cobra"
)

func writeClaudeSessionDetail(cmd *cobra.Command, entries []types.UsageEntry, o reports.Options, f *reportFlags) error {
	entries = reports.Filter(entries, o)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Timestamp.Before(entries[j].Timestamp) })
	if len(entries) == 0 {
		if f.format == "json" {
			return writeReportJSON(cmd, nil, f)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "No session found with ID: %s\n", f.sessionID)
		return nil
	}
	rows := reports.Aggregate(entries, o)
	totals := reports.Totals(rows)
	if f.format == "json" {
		items := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			cost, _ := e.Raw["recorded_cost"].(float64)
			items = append(items, map[string]any{"timestamp": e.Timestamp.UTC().Format(time.RFC3339Nano), "inputTokens": e.InputTokens, "outputTokens": e.OutputTokens, "cacheCreationTokens": e.CacheCreationInputTokens, "cacheReadTokens": e.CacheReadInputTokens, "model": e.Model, "costUSD": cost})
		}
		return writeReportJSON(cmd, map[string]any{"sessionId": f.sessionID, "totalCost": totals["totalCost"], "totalTokens": totals["totalTokens"], "entries": items}, f)
	}
	if f.format == "csv" {
		return reports.WriteCSV(cmd.OutOrStdout(), rows, o)
	}
	if err := reports.WriteTable(cmd.OutOrStdout(), rows, o); err != nil {
		return err
	}
	// Preserve the Go session detail view's attribution to main/subagent files.
	for i := range entries {
		entries[i].SessionID = entries[i].SourceFile
	}
	o.SessionID = ""
	o.SessionName = ""
	fmt.Fprintln(cmd.OutOrStdout(), "\nSource file breakdown")
	return reports.WriteTable(cmd.OutOrStdout(), reports.Aggregate(entries, o), o)
}
