package calculator

import (
	"testing"
	"time"

	"github.com/RedwindA/ccusage_go/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentifySessionBlocksCostBreakdown(t *testing.T) {
	now := time.Now()
	ts := now.Add(-time.Hour) // 1 hour ago

	entries := []types.UsageEntry{
		{
			Timestamp:       ts,
			Model:           "claude-sonnet-4-5-20250514",
			InputTokens:     100,
			OutputTokens:    50,
			TotalTokens:     150,
			Cost:            4.0,
			APICost:         2.5,
			CacheCreateCost: 1.0,
			CacheReadCost:   0.5,
			Raw: map[string]interface{}{
				"cache_creation_input_tokens": 200,
				"cache_read_input_tokens":     500,
			},
		},
		{
			Timestamp:       ts.Add(10 * time.Minute),
			Model:           "claude-sonnet-4-5-20250514",
			InputTokens:     200,
			OutputTokens:    100,
			TotalTokens:     300,
			Cost:            8.0,
			APICost:         5.0,
			CacheCreateCost: 2.0,
			CacheReadCost:   1.0,
			Raw: map[string]interface{}{
				"cache_creation_input_tokens": 400,
				"cache_read_input_tokens":     1000,
			},
		},
	}

	calc := New(nil)
	blocks := calc.IdentifySessionBlocks(entries, 5)

	require.GreaterOrEqual(t, len(blocks), 1)

	// Find the non-gap block
	var block types.SessionBlock
	for _, b := range blocks {
		if !b.IsGap {
			block = b
			break
		}
	}

	assert.InDelta(t, 12.0, block.CostUSD, 0.001, "Total cost should be sum of all entries")
	assert.InDelta(t, 7.5, block.APICostUSD, 0.001, "API cost should be sum of APICost")
	assert.InDelta(t, 3.0, block.CacheCreateCostUSD, 0.001, "Cache create cost should be sum")
	assert.InDelta(t, 1.5, block.CacheReadCostUSD, 0.001, "Cache read cost should be sum")
}

// Upstream rounds the remaining time to whole minutes before projecting, and
// the projection to a whole token and to cents.
func TestProjectBlockUsageRoundsLikeUpstream(t *testing.T) {
	now := time.Now()
	block := types.SessionBlock{
		IsActive:    true,
		EndTime:     now.Add(240*time.Minute + 17*time.Second),
		Entries:     []types.UsageEntry{{Timestamp: now.Add(-37*time.Minute - 18*time.Second)}, {Timestamp: now.Add(-time.Minute - 42*time.Second)}},
		TokenCounts: types.TokenCounts{InputTokens: 233332},
		CostUSD:     2.22221,
	}
	projection := ProjectBlockUsage(block)
	require.NotNil(t, projection)
	assert.Equal(t, 240, projection.RemainingMinutes)
	assert.Equal(t, 1806357, projection.TotalTokens) // 233332 + 233332/35.6*240
	assert.Equal(t, 17.2, projection.TotalCost)
}
