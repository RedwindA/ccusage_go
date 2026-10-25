package monitor

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/RedwindA/ccusage_go/internal/types"
)

// Recent Activity lists the five newest of the calls updateData keeps, at
// local times like the Last Update clock above it.
func TestRecentActivityShowsTheNewestCallsInLocalTime(t *testing.T) {
	local := time.Local
	time.Local = time.FixedZone("west", -7*3600)
	t.Cleanup(func() { time.Local = local })
	m := initialModel(Options{NoColor: true})
	start := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	for i := 2; i < 12; i++ {
		m.recentEntries = append(m.recentEntries, types.UsageEntry{Timestamp: start.Add(time.Duration(i) * time.Minute), Model: "claude-sonnet-4-5"})
	}
	out := m.View()
	for i := 2; i < 12; i++ {
		if strings.Contains(out, fmt.Sprintf("03:%02d:00 - claude-sonnet-4-5", i)) != (i >= 7) {
			t.Fatalf("want the calls from 03:07 to 03:11 only:\n%s", out)
		}
	}
}
