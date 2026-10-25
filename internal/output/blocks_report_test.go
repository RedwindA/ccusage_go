package output

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/RedwindA/ccusage_go/internal/types"
	"github.com/stretchr/testify/assert"
)

func TestBlocksReportContainsCostBreakdown(t *testing.T) {
	now := time.Now()
	blocks := []types.SessionBlock{
		{
			ID:        now.Add(-time.Hour).Format(time.RFC3339),
			StartTime: now.Add(-time.Hour),
			EndTime:   now.Add(4 * time.Hour),
			TokenCounts: types.TokenCounts{
				InputTokens:              100,
				OutputTokens:             50,
				CacheCreationInputTokens: 500,
				CacheReadInputTokens:     2000,
			},
			CostUSD:            4.0,
			APICostUSD:         2.5,
			CacheCreateCostUSD: 1.0,
			CacheReadCostUSD:   0.5,
			Models:             []string{"claude-sonnet-4-5-20250514"},
		},
	}

	formatter := NewTableWriterFormatter(true)
	output := formatter.FormatBlocksReport(blocks, 0)

	assert.Contains(t, output, "CC Cost", "Blocks report should have CC Cost column")
	assert.Contains(t, output, "CR Cost", "Blocks report should have CR Cost column")
	assert.Contains(t, output, "API Cost", "Blocks report should have API Cost column")
	assert.Contains(t, output, "Input", "Blocks report should have Input column")
	assert.Contains(t, output, "Output", "Blocks report should have Output column")
}

// Every row of the blocks report's title box, with or without blocks, is
// as wide as its borders.
func TestBlocksReportTitleBoxIsAligned(t *testing.T) {
	now := time.Now()
	formatter := NewTableWriterFormatter(true)
	for name, blocks := range map[string][]types.SessionBlock{
		"empty":  nil,
		"blocks": {{ID: now.Format(time.RFC3339), StartTime: now, EndTime: now.Add(5 * time.Hour)}},
	} {
		box := strings.Split(strings.TrimPrefix(formatter.FormatBlocksReport(blocks, 0), "\n"), "\n")[:5]
		for _, line := range box {
			if utf8.RuneCountInString(line) != utf8.RuneCountInString(box[0]) {
				t.Fatalf("%s: title box rows differ in width:\n%s", name, strings.Join(box, "\n"))
			}
		}
	}
}
