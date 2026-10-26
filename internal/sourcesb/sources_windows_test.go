package sourcesb

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// A source root that is a junction (mklink /J), the usual way to move a
// directory on Windows without admin rights, is read like a plain directory,
// as upstream's read_dir opens through it.
func TestJunctionedRootsAreRead(t *testing.T) {
	for _, c := range []struct {
		name string
		fill func(t *testing.T, store string)
		// link is the junction's path under the root passed to Load; ""
		// makes the root itself the junction.
		link string
	}{
		{"copilot", func(t *testing.T, store string) {
			write(t, filepath.Join(store, "session-state/s/events.jsonl"), `{"type":"session.shutdown","id":"a","timestamp":"2026-05-05T10:00:00Z","data":{"modelMetrics":{"gpt-5":{"usage":{"inputTokens":100,"outputTokens":20},"requests":{"count":2}}}}}`)
		}, ""},
		{"opencode", func(t *testing.T, store string) {
			database(t, filepath.Join(store, "opencode.db"), `CREATE TABLE session_message(id TEXT,session_id TEXT,type TEXT,time_created INTEGER,data TEXT); CREATE TABLE session_v2(id TEXT,time_created INTEGER,cost REAL,tokens_input INTEGER,tokens_output INTEGER,tokens_cache_read INTEGER,tokens_cache_write INTEGER,tokens_reasoning INTEGER,model TEXT); INSERT INTO session_message VALUES('m','s','assistant',1778000000000,'{"model":{"id":"gpt-5","providerID":"openai"},"tokens":{"input":10,"output":5}}'); INSERT INTO session_v2 VALUES('s',1778000000000,9,999,0,0,0,0,'gpt-5');`)
		}, ""},
		{"codebuff", func(t *testing.T, store string) {
			write(t, filepath.Join(store, "project/chats/2026-05-05T12-30-00.000Z/chat-messages.json"), `[{"id":"one","variant":"ai","credits":2,"metadata":{"model":"gpt-5","usage":{"inputTokens":100,"outputTokens":20,"totalTokens":120}}}]`)
		}, "projects"},
		{"kimi", func(t *testing.T, store string) {
			write(t, filepath.Join(store, "ws/new/agents/main/wire.jsonl"), `{"type":"usage.record","usageScope":"turn","model":"kimi-code/kimi-k2.6","time":1778000000000,"usage":{"inputOther":10,"output":3,"inputCacheRead":7}}`)
		}, "sessions"},
		{"antigravity", func(t *testing.T, store string) {
			antigravityDB(t, filepath.Join(store, "session.db"), "gemini-3-pro", 0, "valid-response")
		}, "conversations"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			plain := filepath.Join(dir, "plain")
			c.fill(t, filepath.Join(plain, c.link))
			want := len(mustLoad(t, c.name, plain))
			if want == 0 {
				t.Fatal("fixture yields no entries")
			}
			store := filepath.Join(dir, "store")
			c.fill(t, store)
			root := filepath.Join(dir, "root")
			link := root
			if c.link != "" {
				write(t, filepath.Join(root, ".keep"), "")
				link = filepath.Join(root, c.link)
			}
			if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, store).CombinedOutput(); err != nil {
				t.Skip(err, string(out))
			}
			if got := len(mustLoad(t, c.name, root)); got != want {
				t.Errorf("read %d entries through a junction, want %d", got, want)
			}
		})
	}
}
