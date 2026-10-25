package loader

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RedwindA/ccusage_go/internal/types"
)

// captureStderr returns what fn writes to os.Stderr, where loads print their
// debug messages.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = saved
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// Loads that share ParsedFiles see what each would load on its own, whatever
// files and load kind earlier loads read: entries, the error when no file
// loads, and the debug messages, which differ between entry and daily loads
// for a file with AgentProgress lines.
func TestLoadsSharingParsedFilesMatchSeparateLoads(t *testing.T) {
	root := t.TempDir()
	write := func(name string, lines ...string) string {
		path := filepath.Join(root, "projects/p", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	session := write("session-a.jsonl",
		`{"timestamp":"2026-09-12T04:00:00.000Z","sessionId":"session-a","requestId":"req_1","message":{"id":"msg_1","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":2}}}`,
		`{"type":"progress","timestamp":"2026-09-12T04:00:01.500Z","sessionId":"session-a","data":{"message":{"timestamp":"2026-09-12T04:00:01.000Z","requestId":"req_2","costUSD":0.25,"message":{"id":"msg_2","model":"claude-sonnet-4-5","usage":{"input_tokens":1000,"output_tokens":0}}}}}`,
		`{"type":"assistant","timestamp":"2026-09-12T04:00:02.000Z","sessionId":"session-a","message":{"id":"msg_3"}}`,
		`{"timestamp":"2026-09-12T04:00:03.000Z","sessionId":"session-a","requestId":"req_1","message":{"id":"msg_1","model":"claude-sonnet-4-5","usage":{"input_tokens":100,"output_tokens":2}}}`,
		`{"type":`,
		`{"type":"custom-title","customTitle":"Title A","sessionId":"session-a"}`,
		`{"timestamp":"2026-09-12T04:00:04.000Z","sessionId":"session-a","requestId":"req_4","message":{"id":"msg_4","model":"claude-sonnet-4-5","usage":{"input_tokens":5,"output_tokens":1,"iterations":[{"type":"advisor_message","model":"claude-opus-4-1","input_tokens":7,"output_tokens":3}]}}}`,
	)
	subagent := write("session-a/subagents/agent-1.jsonl",
		`{"timestamp":"2026-09-12T04:00:05.000Z","sessionId":"session-a","isSidechain":true,"requestId":"req_side","message":{"id":"msg_1","model":"claude-sonnet-4-5","usage":{"input_tokens":100,"output_tokens":2}}}`,
		`{"type":"progress","timestamp":"2026-09-12T04:00:06.500Z","sessionId":"session-a","data":{"message":{"timestamp":"2026-09-12T04:00:01.000Z","requestId":"req_2","isSidechain":true,"message":{"id":"msg_2","model":"claude-sonnet-4-5","usage":{"input_tokens":1000,"output_tokens":0}}}}}`,
	)
	gateway := write("session-b.jsonl",
		`{"timestamp":"2026-09-12T05:00:00.000Z","sessionId":"session-b","message":{"id":"gw-1","model":"claude-sonnet-4-5","usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"timestamp":"2026-09-12T05:00:01.000Z","sessionId":"session-b","message":{"id":"gw-1","model":"claude-sonnet-4-5","usage":{"input_tokens":2,"output_tokens":1}}}`,
	)
	missing := filepath.Join(root, "projects/p/missing.jsonl")

	load := func(paths []string, daily bool, parsed *ParsedFiles) (entries []types.UsageEntry, err error, stderr string) {
		l := New()
		l.SetDebug(true)
		l.SetTimezone(time.UTC)
		l.SetMaxWorkers(4)
		if daily {
			l.SetDailyLoader(func(e *types.UsageEntry) float64 { return e.Cost })
		}
		l.SetParsedFiles(parsed)
		stderr = captureStderr(t, func() { entries, err = l.LoadParallel(context.Background(), paths) })
		return entries, err, stderr
	}
	var parsed ParsedFiles
	for _, step := range []struct {
		paths []string
		daily bool
	}{
		{[]string{session, subagent, gateway}, false},
		{[]string{session, subagent}, true},
		{[]string{session}, false},
		{[]string{gateway, missing}, true},
		{[]string{missing}, false},
		{[]string{session, subagent, gateway, missing}, true},
		{[]string{subagent, session}, false},
	} {
		want, wantErr, wantStderr := load(step.paths, step.daily, nil)
		got, gotErr, gotStderr := load(step.paths, step.daily, &parsed)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%d files, daily %v: entries\n%+v\nwant\n%+v", len(step.paths), step.daily, got, want)
		}
		if (gotErr == nil) != (wantErr == nil) || gotErr != nil && gotErr.Error() != wantErr.Error() {
			t.Errorf("%d files, daily %v: err %v, want %v", len(step.paths), step.daily, gotErr, wantErr)
		}
		if gotStderr != wantStderr {
			t.Errorf("%d files, daily %v: debug output\n%s\nwant\n%s", len(step.paths), step.daily, gotStderr, wantStderr)
		}
	}
}

// A later load sharing ParsedFiles reuses the file an earlier load parsed
// instead of reading it again.
func TestParsedFilesReadEachFileOnce(t *testing.T) {
	root := t.TempDir()
	path := writeNative(t, root, "projects/p/s.jsonl", nativeRecord("s", "msg_1", "req_1", 100, false))
	load := func(parsed *ParsedFiles) []types.UsageEntry {
		t.Helper()
		l := New()
		l.SetParsedFiles(parsed)
		entries, err := l.LoadParallel(context.Background(), []string{path})
		if err != nil {
			t.Fatal(err)
		}
		return entries
	}
	var parsed ParsedFiles
	first := load(&parsed)
	writeNative(t, root, "projects/p/s.jsonl", nativeRecord("s", "msg_2", "req_2", 5, false))
	if got := load(&parsed); !reflect.DeepEqual(got, first) {
		t.Fatalf("shared load = %+v, want the first parse %+v", got, first)
	}
	if got := load(nil); len(got) != 1 || got[0].ID != "msg_2" {
		t.Fatalf("separate load = %+v, want the rewritten file", got)
	}
}
