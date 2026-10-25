package loader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeSinceFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(data), 0o644))
	}
}

func setModified(t *testing.T, path, at string) {
	t.Helper()
	require.NoError(t, os.Chtimes(path, sinceTime(t, at), sinceTime(t, at)))
}

func sinceTime(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	return at
}

func sinceFixtureFiles(t *testing.T) []string {
	t.Helper()
	root := t.TempDir()
	writeSinceFixture(t, root, map[string]string{"projects/p/old.jsonl": "{}", "projects/p/margin.jsonl": "{}", "projects/p/fresh.jsonl": "{}"})
	old, margin, fresh := filepath.Join(root, "projects/p/old.jsonl"), filepath.Join(root, "projects/p/margin.jsonl"), filepath.Join(root, "projects/p/fresh.jsonl")
	setModified(t, old, "2026-09-18T12:00:00Z")
	setModified(t, margin, "2026-09-19T12:00:00Z")
	setModified(t, fresh, "2026-09-20T12:00:00Z")
	return []string{fresh, margin, old}
}

func baseNames(files []string) []string {
	names := []string{}
	for _, file := range files {
		names = append(names, filepath.Base(file))
	}
	return names
}

func TestSplitFilesBeforeSincePrunesFilesLastWrittenBeforeTheMargin(t *testing.T) {
	kept, pruned := SplitFilesBeforeSince(sinceFixtureFiles(t), sinceTime(t, "2026-09-20T00:00:00Z"), sinceTime(t, "2026-09-24T00:00:00Z"))

	assert.Equal(t, []string{"fresh.jsonl", "margin.jsonl"}, baseNames(kept))
	assert.Equal(t, []string{"old.jsonl"}, baseNames(pruned))
}

func TestSplitFilesBeforeSinceKeepsEveryFileWithoutABound(t *testing.T) {
	kept, pruned := SplitFilesBeforeSince(sinceFixtureFiles(t), time.Time{}, sinceTime(t, "2026-09-24T00:00:00Z"))

	assert.Len(t, kept, 3)
	assert.Empty(t, pruned)
}

func TestSplitFilesBeforeSinceKeepsEveryFileWhenTheWindowOpensInTheFuture(t *testing.T) {
	kept, _ := SplitFilesBeforeSince(sinceFixtureFiles(t), sinceTime(t, "2026-09-30T00:00:00Z"), sinceTime(t, "2026-09-29T00:00:00Z"))

	assert.Len(t, kept, 3)
}

func TestSplitFilesBeforeSinceKeepsFilesWhoseMetadataCannotBeRead(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "projects/p/missing.jsonl")

	kept, _ := SplitFilesBeforeSince([]string{missing}, sinceTime(t, "2026-09-20T00:00:00Z"), sinceTime(t, "2026-09-24T00:00:00Z"))

	assert.Equal(t, []string{missing}, kept)
}

func TestSplitFilesBeforeSinceKeepsOrPrunesASessionTogetherWithItsSubagents(t *testing.T) {
	root := t.TempDir()
	writeSinceFixture(t, root, map[string]string{
		"projects/p/live.jsonl":                    "{}",
		"projects/p/live/subagents/agent-a.jsonl":  "{}",
		"projects/p/stale.jsonl":                   "{}",
		"projects/p/stale/subagents/agent-b.jsonl": "{}",
	})
	liveParent, liveAgent := filepath.Join(root, "projects/p/live.jsonl"), filepath.Join(root, "projects/p/live/subagents/agent-a.jsonl")
	staleParent, staleAgent := filepath.Join(root, "projects/p/stale.jsonl"), filepath.Join(root, "projects/p/stale/subagents/agent-b.jsonl")
	// Only the live session's subagent was written inside the window.
	setModified(t, liveParent, "2026-09-01T12:00:00Z")
	setModified(t, liveAgent, "2026-09-21T12:00:00Z")
	setModified(t, staleParent, "2026-09-01T12:00:00Z")
	setModified(t, staleAgent, "2026-09-02T12:00:00Z")

	kept, pruned := SplitFilesBeforeSince([]string{liveParent, liveAgent, staleParent, staleAgent}, sinceTime(t, "2026-09-20T00:00:00Z"), sinceTime(t, "2026-09-24T00:00:00Z"))

	assert.Equal(t, []string{liveParent, liveAgent}, kept)
	assert.Equal(t, []string{staleParent, staleAgent}, pruned)
}

func TestSplitFilesBeforeSinceKeepsFlatAgentTranscriptsAndConfigRootCopiesWithTheirSession(t *testing.T) {
	root := t.TempDir()
	writeSinceFixture(t, root, map[string]string{
		"a/projects/p/live.jsonl":       "{}",
		"a/projects/p/agent-1234.jsonl": `{"sessionId":"live","timestamp":"2026-09-01T12:00:00Z"}`,
		"a/projects/p/agent-5678.jsonl": `{"sessionId":"stale","timestamp":"2026-09-01T12:00:00Z"}`,
		"b/projects/p/live.jsonl":       "{}",
	})
	live, liveAgent := filepath.Join(root, "a/projects/p/live.jsonl"), filepath.Join(root, "a/projects/p/agent-1234.jsonl")
	staleAgent, otherRootCopy := filepath.Join(root, "a/projects/p/agent-5678.jsonl"), filepath.Join(root, "b/projects/p/live.jsonl")
	setModified(t, live, "2026-09-21T12:00:00Z")
	for _, stale := range []string{liveAgent, staleAgent, otherRootCopy} {
		setModified(t, stale, "2026-09-01T12:00:00Z")
	}

	kept, pruned := SplitFilesBeforeSince([]string{liveAgent, staleAgent, live, otherRootCopy}, sinceTime(t, "2026-09-20T00:00:00Z"), sinceTime(t, "2026-09-24T00:00:00Z"))

	assert.Equal(t, []string{liveAgent, live, otherRootCopy}, kept)
	assert.Equal(t, []string{staleAgent}, pruned)
}

func TestSplitFilesBeforeSinceKeepsSessionsLinkedByARecordedSessionIDInAnotherDirectory(t *testing.T) {
	root := t.TempDir()
	writeSinceFixture(t, root, map[string]string{
		"projects/p/session-y.jsonl": `{"sessionId":"session-y"}`,
		// Workflow transcripts can live under one session and record another.
		"projects/p/session-x/subagents/workflows/wf_1/agent-a.jsonl": "{\"type\":\"summary\"}\n{\"sessionId\" : \"session-y\"}",
	})
	parent, workflowAgent := filepath.Join(root, "projects/p/session-y.jsonl"), filepath.Join(root, "projects/p/session-x/subagents/workflows/wf_1/agent-a.jsonl")
	setModified(t, parent, "2026-09-01T12:00:00Z")
	setModified(t, workflowAgent, "2026-09-21T12:00:00Z")

	kept, pruned := SplitFilesBeforeSince([]string{parent, workflowAgent}, sinceTime(t, "2026-09-20T00:00:00Z"), sinceTime(t, "2026-09-24T00:00:00Z"))

	assert.Equal(t, []string{parent, workflowAgent}, kept)
	assert.Empty(t, pruned)
}

func TestRecordedSessionIDReadsPastLongLeadingLines(t *testing.T) {
	root := t.TempDir()
	padding := `{"type":"summary","text":"` + strings.Repeat("x", 100*1024) + `"}`
	writeSinceFixture(t, root, map[string]string{"projects/p/agent-1.jsonl": padding + "\n" + `{"sessionId":  "parent"}`})

	assert.Equal(t, "parent", recordedSessionID(filepath.Join(root, "projects/p/agent-1.jsonl")))
}
