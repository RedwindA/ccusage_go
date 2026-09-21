package commands

import (
	"encoding/json"
	"strings"
	"testing"
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
