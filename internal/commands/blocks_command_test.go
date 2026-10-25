package commands

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBlocksJSONCleanAndNoCost(t *testing.T) {
	path := fixtureReport(t)
	for _, extra := range [][]string{{}, {"--no-cost"}, {"--active"}, {"--since", "20260308", "--until", "20260308"}, {"--session-length", "2.5"}} {
		args := append([]string{"blocks", "--data-path", path, "--offline", "--json", "--token-limit", "max"}, extra...)
		out, err := executeReport(args...)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(out), &payload); err != nil {
			t.Fatalf("polluted JSON: %v %s", err, out)
		}
		if payload["blocks"] == nil {
			t.Fatalf("missing blocks array %s", out)
		}
		if len(extra) > 0 && extra[0] == "--no-cost" && strings.Contains(strings.ToLower(out), "cost") {
			t.Fatal(out)
		}
	}
}

func TestBlocksValidateBeforeLoading(t *testing.T) {
	for _, args := range [][]string{{"--session-length", "NaN"}, {"--session-length", "0"}, {"--since", "20260230"}, {"--token-limit", "-1"}} {
		_, err := executeReport(append([]string{"blocks", "--data-path", t.TempDir(), "--offline", "--json"}, args...)...)
		if err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func blocksFixture(t *testing.T, usage map[time.Time]int) string {
	t.Helper()
	root := t.TempDir()
	var lines []string
	for at, input := range usage {
		lines = append(lines, fmt.Sprintf(`{"timestamp":%q,"sessionId":"s1","requestId":"r%d","message":{"id":"m%d","model":"claude-sonnet-4-20250514","usage":{"input_tokens":%d,"output_tokens":0}}}`, at.UTC().Format(time.RFC3339), len(lines), len(lines), input))
	}
	sourceCLIWrite(t, filepath.Join(root, "projects/proj/s1.jsonl"), strings.Join(lines, "\n"))
	return root
}

// The default token limit is the largest finished block shown, as upstream
// takes it after --since, --until, --recent and --active narrow the blocks.
func TestBlocksTokenLimitComesFromTheShownBlocks(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 1, d, 10, 0, 0, 0, time.UTC) }
	root := blocksFixture(t, map[time.Time]int{day(1): 1000000, day(32): 1000, day(33): 500})
	payload := sourceCLIJSON(t, "blocks", "--data-path", root, "--offline", "--json", "--timezone", "UTC", "--token-limit", "max", "--since", "20260201")
	for _, b := range payload["blocks"].([]any) {
		if block := b.(map[string]any); block["tokenLimit"] != 1000.0 {
			t.Errorf("--since: block %v has token limit %v, want 1000", block["id"], block["tokenLimit"])
		}
	}
	now := time.Now()
	root = blocksFixture(t, map[time.Time]int{now.Add(-48 * time.Hour): 1000000, now.Add(-30 * time.Minute): 10, now.Add(-10 * time.Minute): 10})
	payload = sourceCLIJSON(t, "blocks", "--data-path", root, "--offline", "--json", "--token-limit", "max", "--active")
	blocks := payload["blocks"].([]any)
	if len(blocks) != 1 || blocks[0].(map[string]any)["tokenLimit"] != nil {
		t.Errorf("--active: %v, want one block without a token limit", blocks)
	}
}
