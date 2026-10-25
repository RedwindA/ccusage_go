package sourcesa

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// zstdFrame compresses body into one frame, as Codex compresses cold rollouts.
func zstdFrame(t *testing.T, body string) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	return enc.EncodeAll([]byte(body), nil)
}
func compressedFixture(t *testing.T, root, path, body string) string {
	t.Helper()
	return fixture(t, root, path, string(zstdFrame(t, body)))
}
func codexUsageLog(ts string, input int) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"turn_context","payload":{"model":"gpt-5"}}
{"timestamp":%[1]q,"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":%d,"output_tokens":20,"total_tokens":%d}}}}`, ts, input, input+20)
}
func codexCumulative(ts string, input, cached, output int) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d,"total_tokens":%d}}}}`, ts, input, cached, output, input+output)
}
func sumTokens(t *testing.T, root string) (int, map[string]int) {
	t.Helper()
	total, bySession := 0, map[string]int{}
	for _, e := range loadTest(t, "codex", root) {
		total += e.TotalTokens
		bySession[e.SessionID] += e.TotalTokens
	}
	return total, bySession
}

func TestCodexLoadsCompressedRolloutWithPlainSessionID(t *testing.T) {
	root := t.TempDir()
	compressedFixture(t, root, "2026/10/14/rollout.jsonl.zst", `{"timestamp":"2026-10-14T00:00:00Z","type":"turn_context","payload":{"model":"gpt-5"}}
{"timestamp":"2026-10-14T00:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":80,"output_tokens":20,"reasoning_output_tokens":5,"total_tokens":120}}}}`)
	entries := loadTest(t, "codex", root)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	requireTokens(t, entries[0], 20, 20, 80, 0, 120)
	if e := entries[0]; e.SessionID != "2026/10/14/rollout" || e.Model != "gpt-5" || e.ReasoningTokens != 5 {
		t.Fatalf("%+v", e)
	}
}

func TestCodexDiscoversCompressedRolloutsInSessionsAndArchives(t *testing.T) {
	root := t.TempDir()
	compressedFixture(t, root, "sessions/2026/10/14/active.jsonl.zst", codexUsageLog("2026-10-14T00:00:00Z", 100))
	compressedFixture(t, root, "archived_sessions/archived.jsonl.zst", codexUsageLog("2026-10-14T00:01:00Z", 200))
	fixture(t, root, "archived_sessions/plain.jsonl", codexUsageLog("2026-10-14T00:02:00Z", 300))
	fixture(t, root, "sessions/notes.zst", "not a rollout")
	total, bySession := sumTokens(t, root)
	var ids []string
	for id := range bySession {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if total != 660 || !reflect.DeepEqual(ids, []string{"2026/10/14/active", "archived", "plain"}) {
		t.Fatal(total, bySession)
	}
}

func TestCodexDedupesRolloutsAcrossEncodingsAndSessionDirectories(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{
		"sessions/both.jsonl", "sessions/both.jsonl.zst", "sessions/compressed.jsonl.zst", "sessions/other.zst", "sessions/other.json.zst",
		"archived_sessions/compressed.jsonl", "archived_sessions/both.jsonl.zst", "archived_sessions/archived.jsonl.zst",
	} {
		fixture(t, root, p, "")
	}
	files, dirs, err := discover(context.Background(), "codex", []string{root})
	want := []string{filepath.Join(root, "sessions/both.jsonl"), filepath.Join(root, "sessions/compressed.jsonl.zst"), filepath.Join(root, "archived_sessions/archived.jsonl.zst")}
	wantDirs := []string{filepath.Join(root, "sessions"), filepath.Join(root, "sessions"), filepath.Join(root, "archived_sessions")}
	if err != nil || !reflect.DeepEqual(files, want) || !reflect.DeepEqual(dirs, wantDirs) {
		t.Fatalf("files=%v dirs=%v err=%v", files, dirs, err)
	}
}

func TestCodexCountsPlainAndCompressedCopiesOnce(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "session.jsonl", codexUsageLog("2026-10-14T00:00:10Z", 100))
	compressedFixture(t, root, "session.jsonl.zst", codexUsageLog("2026-10-14T00:00:01Z", 400))
	compressedFixture(t, root, "single.jsonl.zst", codexUsageLog("2026-10-14T00:00:20Z", 200))
	if total, bySession := sumTokens(t, root); total != 340 || bySession["session"] != 120 {
		t.Fatal(total, bySession)
	}
}

func TestCodexKeepsActiveRolloutWhenArchiveUsesOtherEncoding(t *testing.T) {
	root := t.TempDir()
	compressedFixture(t, root, "sessions/session.jsonl.zst", codexUsageLog("2026-10-14T00:00:10Z", 100))
	fixture(t, root, "archived_sessions/session.jsonl", codexUsageLog("2026-10-14T00:00:01Z", 400))
	if total, _ := sumTokens(t, root); total != 120 {
		t.Fatal(total)
	}
}

func TestCodexCumulativeDeltasAcrossConcatenatedZstdFrames(t *testing.T) {
	root := t.TempDir()
	first := `{"type":"turn_context","payload":{"model":"gpt-5"}}` + "\n" + codexCumulative("2026-10-14T00:00:01Z", 100, 80, 20) + "\n"
	second := codexCumulative("2026-10-14T00:00:02Z", 160, 110, 40) + "\n" + codexCumulative("2026-10-14T00:00:03Z", 160, 110, 40)
	fixture(t, root, "session.jsonl.zst", string(append(zstdFrame(t, first), zstdFrame(t, second)...)))
	entries := loadTest(t, "codex", root)
	if len(entries) != 2 {
		t.Fatal(entries)
	}
	requireTokens(t, entries[1], 30, 20, 30, 0, 80)
}

func TestCodexKeepsUsageDecodedBeforeDamagedZstd(t *testing.T) {
	root := t.TempDir()
	truncated := zstdFrame(t, codexUsageLog("2026-10-14T00:01:00Z", 200)+"\n")
	fixture(t, root, "session.jsonl.zst", string(append(zstdFrame(t, codexUsageLog("2026-10-14T00:00:00Z", 100)+"\n"), truncated[:len(truncated)/2]...)))
	fixture(t, root, "garbage.jsonl.zst", codexUsageLog("2026-10-14T00:02:00Z", 300))
	if total, bySession := sumTokens(t, root); total != 120 || bySession["session"] != 120 {
		t.Fatal(total, bySession)
	}
}

func TestCodexStreamsZstdFrameClaimingHugeContentSize(t *testing.T) {
	root := t.TempDir()
	// A multi-segment frame with a 1 MiB window claims 8 GiB but holds one
	// raw block of usage, then an empty last block.
	log := codexUsageLog("2026-10-14T00:00:00Z", 100) + "\n"
	frame := binary.LittleEndian.AppendUint64([]byte{0x28, 0xb5, 0x2f, 0xfd, 0xc0, 0x50}, 1<<33)
	n := len(log) << 3 // raw block header: size, type 0, not last
	frame = append(append(frame, byte(n), byte(n>>8), byte(n>>16)), log...)
	fixture(t, root, "claims-more.jsonl.zst", string(append(frame, 0x01, 0x00, 0x00)))
	fixture(t, root, "session.jsonl", codexUsageLog("2026-10-14T00:01:00Z", 200))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	total, _ := sumTokens(t, root)
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; total != 340 || allocated > 1<<30 {
		t.Fatalf("total=%d allocated=%d", total, allocated)
	}
}

func TestCodexReadsOtherEncodingWhenCodexMigratesAfterDiscovery(t *testing.T) {
	for _, tc := range []struct{ discovered, migrated string }{{"session.jsonl", "session.jsonl.zst"}, {"session.jsonl.zst", "session.jsonl"}} {
		t.Run(tc.discovered, func(t *testing.T) {
			root := t.TempDir()
			log := codexUsageLog("2026-10-14T00:00:00Z", 100)
			write := func(path string) string {
				if filepath.Ext(path) == ".zst" {
					return compressedFixture(t, root, path, log)
				}
				return fixture(t, root, path, log)
			}
			discovered := write(tc.discovered)
			files, dirs, err := discover(context.Background(), "codex", []string{root})
			if err != nil || !reflect.DeepEqual(files, []string{discovered}) {
				t.Fatalf("files=%v err=%v", files, err)
			}
			if err := os.Remove(discovered); err != nil {
				t.Fatal(err)
			}
			write(tc.migrated)
			entries, err := loadCodex(context.Background(), files, dirs, time.UTC)
			if err != nil || len(entries) != 1 || entries[0].SessionID != "session" || entries[0].TotalTokens != 120 {
				t.Fatalf("entries=%+v err=%v", entries, err)
			}
		})
	}
}

func TestCodexDatesHeadlessUsageFromEncodingOpenedAfterMigration(t *testing.T) {
	root := t.TempDir()
	headless := `{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}`
	plain := fixture(t, root, "run.jsonl", headless)
	files, dirs, err := discover(context.Background(), "codex", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(plain); err != nil {
		t.Fatal(err)
	}
	modified := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(compressedFixture(t, root, "run.jsonl.zst", headless), modified, modified); err != nil {
		t.Fatal(err)
	}
	entries, err := loadCodex(context.Background(), files, dirs, time.UTC)
	if err != nil || len(entries) != 1 || !entries[0].Timestamp.Equal(modified) {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
}

func TestCodexRemovesInheritedUsageWhenParentAndChildAreCompressed(t *testing.T) {
	root := t.TempDir()
	compressedFixture(t, root, "a-parent.jsonl.zst", `{"type":"session_meta","payload":{"id":"parent"}}`+"\n"+codexUsageLog("2026-10-14T00:00:00Z", 100))
	compressedFixture(t, root, "b-child.jsonl.zst", `{"timestamp":"2026-10-14T00:01:00Z","type":"session_meta","payload":{"id":"child","forked_from_id":"parent"}}`+"\n"+codexUsageLog("2026-10-14T00:01:00Z", 100)+"\n"+codexUsageLog("2026-10-14T00:02:00Z", 50))
	if total, bySession := sumTokens(t, root); total != 190 || bySession["b-child"] != 70 {
		t.Fatal(total, bySession)
	}
}

func TestCodexUsesCompressedArchivedParent(t *testing.T) {
	root := t.TempDir()
	compressedFixture(t, root, "archived_sessions/2026/09/01/parent.jsonl.zst", `{"type":"session_meta","payload":{"id":"parent"}}`+"\n"+codexUsageLog("2026-09-01T00:00:00Z", 100))
	fixture(t, root, "sessions/2026/10/14/child.jsonl", `{"timestamp":"2026-10-14T00:01:00Z","type":"session_meta","payload":{"id":"child","forked_from_id":"parent"}}`+"\n"+codexUsageLog("2026-10-14T00:01:00Z", 100)+"\n"+codexUsageLog("2026-10-14T00:02:00Z", 50))
	if _, bySession := sumTokens(t, root); bySession["2026/10/14/child"] != 70 {
		t.Fatal(bySession)
	}
}

func TestCodexDetectsRewrittenBurstInCompressedChildWithoutParent(t *testing.T) {
	root := t.TempDir()
	compressedFixture(t, root, "child.jsonl.zst", `{"timestamp":"2026-10-14T00:00:10Z","type":"session_meta","payload":{"id":"child","forked_from_id":"missing-parent"}}`+"\n"+codexUsageLog("2026-10-14T00:00:10.000Z", 100)+"\n"+codexUsageLog("2026-10-14T00:00:10.010Z", 200)+"\n"+codexUsageLog("2026-10-14T00:00:12Z", 50))
	if total, _ := sumTokens(t, root); total != 70 {
		t.Fatal(total)
	}
}

func TestCodexCompressedRolloutsMatchPlain(t *testing.T) {
	root := t.TempDir()
	log := strings.Replace(codexUsageLog("2026-02-01T00:00:00Z", 100), "gpt-5", "gpt-5.3-codex", 1) + "\n" + codexUsageLog("2026-10-14T00:00:00Z", 50)
	fixture(t, root, "plain/session.jsonl", log)
	compressedFixture(t, root, "compressed/session.jsonl.zst", log)
	plain, compressed := loadTest(t, "codex", filepath.Join(root, "plain")), loadTest(t, "codex", filepath.Join(root, "compressed"))
	if len(plain) != 2 || len(compressed) != 2 {
		t.Fatalf("plain=%+v\ncompressed=%+v", plain, compressed)
	}
	for i := range compressed {
		compressed[i].SourceFile = plain[i].SourceFile
	}
	if !reflect.DeepEqual(compressed, plain) {
		t.Fatalf("plain=%+v\ncompressed=%+v", plain, compressed)
	}
}

func TestCodexCountsCompactionRecordsInCompressedArchiveOnce(t *testing.T) {
	root := t.TempDir()
	record := `{"timestamp":"2026-10-14T00:00:02Z","type":"token_usage_record","payload":{"response_id":"compaction-1","usage":{"input_tokens":500,"output_tokens":20}}}`
	marker := `{"type":"compacted","payload":{"compaction_response_id":"compaction-1"}}`
	compressedFixture(t, root, "archived_sessions/session.jsonl.zst", strings.Join([]string{codexUsageLog("2026-10-14T00:00:01Z", 100), record, marker, record, marker}, "\n"))
	if entries := loadTest(t, "codex", root); len(entries) != 2 || entries[0].TotalTokens+entries[1].TotalTokens != 640 {
		t.Fatal(entries)
	}
}
