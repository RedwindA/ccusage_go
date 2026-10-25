package loader

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The monitor reads a projects directory that is a symlink, as reports do.
func TestLoadFromPathReadsASymlinkedProjectsDirectory(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "store")
	require.NoError(t, os.MkdirAll(filepath.Join(store, "proj-a"), 0o755))
	line := createTestJSONLEntry(time.Now(), "claude-sonnet-4-20250514", 100, 50, "msg-1", "req-1")
	require.NoError(t, os.WriteFile(filepath.Join(store, "proj-a", "session.jsonl"), []byte(line+"\n"), 0o644))
	claude := filepath.Join(dir, "claude")
	require.NoError(t, os.MkdirAll(claude, 0o755))
	if err := os.Symlink(store, filepath.Join(claude, "projects")); err != nil {
		t.Skip(err)
	}
	entries, err := New().LoadFromPath(context.Background(), claude)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

// Like reports, the monitor skips a symlinked transcript.
func TestLoadFromPathSkipsSymlinkedTranscripts(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "session.jsonl")
	line := createTestJSONLEntry(time.Now(), "claude-sonnet-4-20250514", 100, 50, "msg-1", "req-1")
	require.NoError(t, os.WriteFile(store, []byte(line+"\n"), 0o644))
	project := filepath.Join(dir, "claude", "projects", "proj-a")
	require.NoError(t, os.MkdirAll(project, 0o755))
	if err := os.Symlink(store, filepath.Join(project, "session.jsonl")); err != nil {
		t.Skip(err)
	}
	entries, _ := New().LoadFromPath(context.Background(), filepath.Join(dir, "claude"))
	require.Empty(t, entries)
}
