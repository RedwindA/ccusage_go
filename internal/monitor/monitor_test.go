package monitor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Session files are named by UUID, so the file that sorts last by path can
// hold the oldest usage; Recent Activity must still show the newest calls.
func TestRecentActivityShowsNewestEntries(t *testing.T) {
	root := t.TempDir()
	starts := map[string]time.Time{
		"00000000-new": time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC),
		"ffffffff-old": time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC),
	}
	for session, start := range starts {
		var lines []string
		for i := 0; i < 10; i++ {
			lines = append(lines, fmt.Sprintf(`{"type":"assistant","timestamp":%q,"sessionId":%q,"requestId":"req_%s_%d","message":{"id":"msg_%s_%d","model":"claude-sonnet-4-5","usage":{"input_tokens":1,"output_tokens":1}}}`,
				start.Add(time.Duration(i)*time.Hour).Format(time.RFC3339), session, session, i, session, i))
		}
		p := filepath.Join(root, "projects", "p", session+".jsonl")
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0644); err != nil {
			t.Fatal(err)
		}
	}
	msg := initialModel(Options{DataPath: root}).updateData()().(updateDataMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if len(msg.recentEntries) != 10 {
		t.Fatalf("%d recent entries", len(msg.recentEntries))
	}
	for i, e := range msg.recentEntries {
		if want := starts["00000000-new"].Add(time.Duration(i) * time.Hour); !e.Timestamp.Equal(want) {
			t.Errorf("recent entry %d at %s, want %s", i, e.Timestamp, want)
		}
	}
}
