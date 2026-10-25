package commands

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
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
	if err := writeUsageTable(cmd, rows, o); err != nil {
		return err
	}
	// Preserve the Go session detail view's attribution to main/subagent files.
	for i := range entries {
		entries[i].SessionID = entries[i].SourceFile
	}
	o.SessionID = ""
	o.SessionName = ""
	fmt.Fprintln(cmd.OutOrStdout(), "\nSource file breakdown")
	return writeUsageTable(cmd, reports.Aggregate(entries, o), o)
}

var codexUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// resolveCodexSession accepts a Codex session's full ID, its rollout file name
// (with or without .jsonl or .jsonl.zst), its trailing UUID or a
// codex://threads/<uuid> URI. A UUID shared by several rollouts is an error
// rather than a guess.
func resolveCodexSession(entries []types.UsageEntry, o reports.Options, requested string) (*reports.Row, error) {
	id := strings.TrimPrefix(requested, "codex://threads/")
	id = strings.TrimSuffix(strings.TrimSuffix(id, ".zst"), ".jsonl")
	o.SessionID = ""
	var matches []*reports.Row
	for _, row := range reports.Aggregate(entries, o) {
		session := row.Period
		if session == id || session[strings.LastIndex(session, "/")+1:] == id || codexUUID.MatchString(id) && strings.HasSuffix(session, "-"+id) {
			matches = append(matches, row)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("No Codex session found with ID: %s", requested)
	case 1:
		return matches[0], nil
	}
	return nil, fmt.Errorf("Codex session ID '%s' is ambiguous and matches %d sessions.", requested, len(matches))
}

// writeCodexSessionDetail prints one session's report row on its own, with the
// cost named totalCost as in the Claude session detail.
func writeCodexSessionDetail(cmd *cobra.Command, row *reports.Row, o reports.Options, f *reportFlags) error {
	detail := reports.FocusedJSONRows([]*reports.Row{row}, o)[0]
	if cost, ok := detail["costUSD"]; ok {
		detail["totalCost"] = cost
		delete(detail, "costUSD")
	}
	return writeReportJSON(cmd, detail, f)
}
