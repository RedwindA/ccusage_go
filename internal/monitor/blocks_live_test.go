package monitor

import (
	"strings"
	"testing"
	"time"

	"github.com/RedwindA/ccusage_go/internal/types"
)

func TestLiveNoCostHidesUsageAndProjection(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	last := time.Now().Add(-time.Minute)
	block := types.SessionBlock{ID: "active", StartTime: start, EndTime: start.Add(5 * time.Hour), ActualEndTime: &last, IsActive: true, CostUSD: 3.25, TokenCounts: types.TokenCounts{InputTokens: 100, OutputTokens: 50}, Models: []string{"claude-sonnet-4-6"}, Entries: []types.UsageEntry{{Timestamp: start, InputTokens: 50}, {Timestamp: last, InputTokens: 50, OutputTokens: 50}}}
	m := BlocksLiveModel{config: BlocksLiveConfig{Timezone: time.UTC, TokenLimit: 1000, NoColor: true, NoCost: true}, activeBlock: &block, width: 100, gradientCache: map[string][]string{}}
	out := m.View()
	if strings.Contains(out, "Cost:") || strings.Contains(out, "$3.25") {
		t.Fatalf("cost leaked: %s", out)
	}
	m.config.NoCost = false
	if !strings.Contains(m.View(), "$3.25") {
		t.Fatal("normal live report lost cost")
	}
}
