package commands

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RedwindA/ccusage_go/internal/reports"
	"github.com/RedwindA/ccusage_go/internal/types"
	"github.com/klauspost/compress/zstd"
)

const (
	codexSessionUUID = "01a0bb8c-c7c0-7630-9d82-860b875930f0"
	codexSessionID   = "2026/09/19/rollout-2026-09-19T23-22-40-01a0bb8c-c7c0-7630-9d82-860b875930f0"
	codexSessionFile = "rollout-2026-09-19T23-22-40-01a0bb8c-c7c0-7630-9d82-860b875930f0"
	codexOtherUUID   = "01a0cc9d-d8d1-8741-ae93-971c986041a1"
)

func codexSessionEntries(ids ...string) []types.UsageEntry {
	entries := make([]types.UsageEntry, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, types.UsageEntry{Agent: "codex", SessionID: id, Timestamp: time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)})
	}
	return entries
}

func TestResolveCodexSessionFromSupportedIDForms(t *testing.T) {
	o := reports.Options{Kind: "session", Agent: "codex", Location: time.UTC}
	for _, requested := range []string{codexSessionID, codexSessionFile, codexSessionFile + ".jsonl", codexSessionFile + ".jsonl.zst", codexSessionUUID, "codex://threads/" + codexSessionUUID} {
		row, err := resolveCodexSession(codexSessionEntries(codexSessionID), o, requested)
		if err != nil || row.Period != codexSessionID {
			t.Fatalf("%s: %v %+v", requested, err, row)
		}
	}
}

func TestResolveCodexSessionRejectsAmbiguousUUID(t *testing.T) {
	entries := codexSessionEntries(codexSessionID, "2026/09/20/rollout-2026-09-20T10-00-00-"+codexSessionUUID)
	_, err := resolveCodexSession(entries, reports.Options{Kind: "session", Agent: "codex"}, codexSessionUUID)
	if want := fmt.Sprintf("Codex session ID '%s' is ambiguous and matches 2 sessions.", codexSessionUUID); err == nil || err.Error() != want {
		t.Fatalf("got %v, want %s", err, want)
	}
}

func TestResolveCodexSessionRejectsUnknownID(t *testing.T) {
	_, err := resolveCodexSession(codexSessionEntries("2026/09/19/rollout-known"), reports.Options{Kind: "session", Agent: "codex"}, "missing")
	if err == nil || err.Error() != "No Codex session found with ID: missing" {
		t.Fatalf("got %v", err)
	}
}

func codexSessionUsage(timestamp string, input, output int) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"model":"gpt-5","last_token_usage":{"input_tokens":%d,"cached_input_tokens":10,"output_tokens":%d,"reasoning_output_tokens":0,"total_tokens":%d}}}}`, timestamp, input, output, input+output)
}

func codexSessionFixture(t *testing.T, otherUUID string) string {
	t.Helper()
	isolateSourceHomes(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	sourceCLIWrite(t, filepath.Join(home, "sessions", codexSessionID+".jsonl"), codexSessionUsage("2026-09-19T23:22:40.000Z", 100, 50))
	sourceCLIWrite(t, filepath.Join(home, "sessions/2026/09/20/rollout-2026-09-20T10-00-00-"+otherUUID+".jsonl"), codexSessionUsage("2026-09-20T10:00:00.000Z", 900, 100))
	return home
}

func TestCodexSessionIDSelectsDetailJSON(t *testing.T) {
	codexSessionFixture(t, codexOtherUUID)
	for _, requested := range []string{codexSessionID, codexSessionFile, codexSessionFile + ".jsonl", codexSessionUUID, "codex://threads/" + codexSessionUUID} {
		payload := sourceCLIJSON(t, "codex", "session", "--id", requested, "--json", "--offline", "--timezone", "UTC")
		if payload["sessionId"] != codexSessionID || payload["totalTokens"] != 150.0 {
			t.Fatalf("%s: %#v", requested, payload)
		}
		for key, present := range map[string]bool{"totalCost": true, "costUSD": false, "sessions": false, "totals": false} {
			if _, ok := payload[key]; ok != present {
				t.Fatalf("%s: %s present=%v: %#v", requested, key, ok, payload)
			}
		}
	}
}

func TestCodexSessionIDFiltersTable(t *testing.T) {
	codexSessionFixture(t, codexOtherUUID)
	t.Setenv("COLUMNS", "240")
	out, stderr, err := sourceCLIRun("codex", "session", "--id", codexSessionUUID, "--no-cost", "--offline", "--no-color", "--timezone", "UTC")
	if err != nil || strings.Contains(out, codexOtherUUID) {
		t.Fatalf("%v stderr=%s\n%s", err, stderr, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, codexSessionUUID) {
			if !strings.Contains(line, "150") {
				t.Fatalf("selected row lacks its tokens: %s", line)
			}
			return
		}
	}
	t.Fatalf("selected session row missing:\n%s", out)
}

func TestCodexSessionIDLookupErrors(t *testing.T) {
	codexSessionFixture(t, codexSessionUUID)
	for requested, want := range map[string]string{
		"missing":        "No Codex session found with ID: missing",
		"":               "No Codex session found with ID: ",
		codexSessionUUID: fmt.Sprintf("Codex session ID '%s' is ambiguous and matches 2 sessions.", codexSessionUUID),
	} {
		out, _, err := sourceCLIRun("codex", "session", "--id", requested, "--offline")
		if err == nil || err.Error() != want || out != "" {
			t.Fatalf("%s: err=%v stdout=%s", requested, err, out)
		}
	}
}

func TestCodexSessionIDAcceptsCompressedRolloutName(t *testing.T) {
	home := codexSessionFixture(t, codexOtherUUID)
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	compressed := filepath.Join(home, "archived_sessions/2026/09/21", codexSessionFile+"-zst.jsonl.zst")
	sourceCLIWrite(t, compressed, string(enc.EncodeAll([]byte(codexSessionUsage("2026-09-21T08:00:00.000Z", 300, 25)), nil)))
	daily := sourceCLIJSON(t, "codex", "daily", "--json", "--offline", "--timezone", "UTC")
	if len(daily["daily"].([]any)) != 3 || daily["totals"].(map[string]any)["totalTokens"] != float64(150+1000+325) {
		t.Fatalf("daily: %#v", daily)
	}
	session := sourceCLIJSON(t, "codex", "session", "--id", codexSessionFile+"-zst.jsonl.zst", "--json", "--offline", "--timezone", "UTC")
	if session["sessionId"] != "2026/09/21/"+codexSessionFile+"-zst" || session["totalTokens"] != 325.0 {
		t.Fatalf("session: %#v", session)
	}
}
