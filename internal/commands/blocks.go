package commands

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/RedwindA/ccusage_go/internal/calculator"
	"github.com/RedwindA/ccusage_go/internal/types"
)

const (
	DefaultRecentDays             = 3
	DefaultRefreshIntervalSeconds = 1
	MinRefreshIntervalSeconds     = 1
	MaxRefreshIntervalSeconds     = 60
)

func formatActiveBlockDetail(block types.SessionBlock, tokenLimit int, noColor bool, loc *time.Location) string {
	var output strings.Builder

	// Title box
	output.WriteString("\n")
	output.WriteString(" ╭───────────────────────────────────────────────╮\n")
	output.WriteString(" │                                               │\n")
	output.WriteString(" │  Current Session Block Status (WITH GO)  │\n")
	output.WriteString(" │                                               │\n")
	output.WriteString(" ╰───────────────────────────────────────────────╯\n\n")

	now := time.Now()
	elapsed := now.Sub(block.StartTime)
	remaining := block.EndTime.Sub(now)

	// Convert StartTime to local timezone for display
	localStartTime := block.StartTime
	if loc != nil {
		localStartTime = block.StartTime.In(loc)
	}

	// Block timing
	if !noColor {
		output.WriteString(fmt.Sprintf("Block Started: \033[36m%s\033[0m (\033[33m%dh %dm\033[0m ago)\n",
			localStartTime.Format("1/2/2006, 3:04:05 PM"),
			int(elapsed.Hours()), int(elapsed.Minutes())%60))
		output.WriteString(fmt.Sprintf("Time Remaining: \033[32m%dh %dm\033[0m\n\n",
			int(remaining.Hours()), int(remaining.Minutes())%60))
	} else {
		output.WriteString(fmt.Sprintf("Block Started: %s (%dh %dm ago)\n",
			localStartTime.Format("1/2/2006, 3:04:05 PM"),
			int(elapsed.Hours()), int(elapsed.Minutes())%60))
		output.WriteString(fmt.Sprintf("Time Remaining: %dh %dm\n\n",
			int(remaining.Hours()), int(remaining.Minutes())%60))
	}

	// Current usage
	output.WriteString("Current Usage:\n")
	output.WriteString(fmt.Sprintf("  Input Tokens:     %s\n", formatNumber(block.TokenCounts.InputTokens)))
	output.WriteString(fmt.Sprintf("  Output Tokens:    %s\n", formatNumber(block.TokenCounts.OutputTokens)))
	output.WriteString(fmt.Sprintf("  Total Cost:       $%.2f\n\n", block.CostUSD))

	// Burn rate
	if burnRate := calculator.CalculateBurnRate(block); burnRate != nil {
		output.WriteString("Burn Rate:\n")
		output.WriteString(fmt.Sprintf("  Tokens/minute:    %s\n", formatNumber(int(burnRate.TokensPerMinute))))
		output.WriteString(fmt.Sprintf("  Cost/hour:        $%.2f\n\n", burnRate.CostPerHour))
	}

	// Projections
	if projection := calculator.ProjectBlockUsage(block); projection != nil {
		output.WriteString("Projected Usage (if current rate continues):\n")
		output.WriteString(fmt.Sprintf("  Total Tokens:     %s\n", formatNumber(projection.TotalTokens)))
		output.WriteString(fmt.Sprintf("  Total Cost:       $%.2f\n\n", projection.TotalCost))

		// Token limit status
		if tokenLimit > 0 {
			currentTokens := block.TokenCounts.GetTotal()
			remainingTokens := tokenLimit - currentTokens
			if remainingTokens < 0 {
				remainingTokens = 0
			}
			percentUsed := float64(projection.TotalTokens) / float64(tokenLimit) * 100

			var status string
			if !noColor {
				if percentUsed > 100 {
					status = "\033[31mEXCEEDS LIMIT\033[0m"
				} else if percentUsed > calculator.BlocksWarningThreshold*100 {
					status = "\033[33mWARNING\033[0m"
				} else {
					status = "\033[32mOK\033[0m"
				}
			} else {
				if percentUsed > 100 {
					status = "EXCEEDS LIMIT"
				} else if percentUsed > calculator.BlocksWarningThreshold*100 {
					status = "WARNING"
				} else {
					status = "OK"
				}
			}

			output.WriteString("Token Limit Status:\n")
			output.WriteString(fmt.Sprintf("  Limit:            %s tokens\n", formatNumber(tokenLimit)))
			output.WriteString(fmt.Sprintf("  Current Usage:    %s (%.1f%%)\n", formatNumber(currentTokens), float64(currentTokens)/float64(tokenLimit)*100))
			output.WriteString(fmt.Sprintf("  Remaining:        %s tokens\n", formatNumber(remainingTokens)))
			output.WriteString(fmt.Sprintf("  Projected Usage:  %.1f%% %s\n", percentUsed, status))
		}
	}

	return output.String()
}

// formatNumber formats a number with thousand separators
func formatNumber(n int) string {
	if n < 0 {
		return "-" + formatNumber(-n)
	}
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return formatNumber(n/1000) + "," + fmt.Sprintf("%03d", n%1000)
}

// formatBlocksAsJSON converts blocks to JSON structure
func formatBlocksAsJSON(blocks []types.SessionBlock, tokenLimit int) map[string]interface{} {
	blockData := []map[string]interface{}{}

	for _, block := range blocks {
		burnRate := calculator.CalculateBurnRate(block)
		projection := calculator.ProjectBlockUsage(block)

		blockMap := map[string]interface{}{
			"id":              block.ID,
			"start_time":      block.StartTime,
			"end_time":        block.EndTime,
			"actual_end_time": block.ActualEndTime,
			"is_active":       block.IsActive,
			"is_gap":          block.IsGap,
			"entries":         len(block.Entries),
			"token_counts":    block.TokenCounts,
			"total_tokens":    block.TokenCounts.GetTotal(),
			"cost_usd":        block.CostUSD,
			"models":          block.Models,
		}

		if burnRate != nil {
			blockMap["burn_rate"] = burnRate
		}

		if projection != nil {
			blockMap["projection"] = projection

			if tokenLimit > 0 {
				percentUsed := float64(projection.TotalTokens) / float64(tokenLimit) * 100
				status := "ok"
				if percentUsed > 100 {
					status = "exceeds"
				} else if percentUsed > calculator.BlocksWarningThreshold*100 {
					status = "warning"
				}

				blockMap["token_limit_status"] = map[string]interface{}{
					"limit":           tokenLimit,
					"projected_usage": projection.TotalTokens,
					"percent_used":    percentUsed,
					"status":          status,
				}
			}
		}

		if block.UsageLimitResetTime != nil {
			blockMap["usage_limit_reset_time"] = block.UsageLimitResetTime
		}

		blockData = append(blockData, blockMap)
	}

	return map[string]interface{}{
		"blocks": blockData,
	}
}

// formatBlocksAsCSV converts blocks to CSV structure
func formatBlocksAsCSV(blocks []types.SessionBlock) [][]string {
	headers := []string{
		"Block ID",
		"Start Time",
		"End Time",
		"Is Active",
		"Is Gap",
		"Input Tokens",
		"Output Tokens",
		"Cache Creation Tokens",
		"Cache Read Tokens",
		"Total Tokens",
		"Cost (USD)",
		"Models",
		"Entry Count",
	}

	rows := [][]string{headers}

	for _, block := range blocks {
		row := []string{
			block.ID,
			block.StartTime.Format(time.RFC3339),
			block.EndTime.Format(time.RFC3339),
			strconv.FormatBool(block.IsActive),
			strconv.FormatBool(block.IsGap),
			strconv.Itoa(block.TokenCounts.InputTokens),
			strconv.Itoa(block.TokenCounts.OutputTokens),
			strconv.Itoa(block.TokenCounts.CacheCreationInputTokens),
			strconv.Itoa(block.TokenCounts.CacheReadInputTokens),
			strconv.Itoa(block.TokenCounts.GetTotal()),
			fmt.Sprintf("%.2f", block.CostUSD),
			strings.Join(block.Models, ";"),
			strconv.Itoa(len(block.Entries)),
		}
		rows = append(rows, row)
	}

	return rows
}

// filterEntriesByDateRange filters entries by date range
func filterEntriesByDateRange(entries []types.UsageEntry, since, until string) []types.UsageEntry {
	filtered := []types.UsageEntry{}

	var sinceTime, untilTime time.Time
	var err error

	if since != "" {
		sinceTime, err = time.Parse("2006-01-02", since)
		if err != nil {
			sinceTime = time.Time{}
		}
	}

	if until != "" {
		untilTime, err = time.Parse("2006-01-02", until)
		if err != nil {
			untilTime = time.Now()
		} else {
			// Include the entire "until" day
			untilTime = untilTime.Add(24 * time.Hour)
		}
	} else {
		untilTime = time.Now()
	}

	for _, entry := range entries {
		if (sinceTime.IsZero() || entry.Timestamp.After(sinceTime) || entry.Timestamp.Equal(sinceTime)) &&
			(entry.Timestamp.Before(untilTime)) {
			filtered = append(filtered, entry)
		}
	}

	return filtered
}
