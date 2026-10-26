package loader

import (
	"bufio"
	"bytes"
	"os"
	"time"
)

// mtimePruneMargin is subtracted from a --since bound before comparing it
// with file mtimes. It absorbs the gap between the timezone-resolved bound
// and wall-clock mtimes, plus sessions flushed well after the entries they
// hold.
const mtimePruneMargin = 24 * time.Hour

// SplitFilesBeforeSince drops the sessions whose files were all last written
// before since.
//
// Claude session JSONL files are append-only, so a file last written before
// the window opened cannot hold an entry inside it. Deduplication matches
// sidechain replays and requestless repeats by session ID, which spans files
// (subagent and workflow transcripts, legacy flat agent-*.jsonl files,
// copies in another config root), so files are kept or dropped per connected
// group of session IDs: dropping a parent file alone would let its replay
// count twice. Copies of a message in other sessions, such as resumed
// transcripts, keep the original timestamp, so they fall on the same side of
// the window as the original.
//
// Every file is kept when since is zero or after now, or when a file's mtime
// cannot be read. kept and pruned each keep the order of files.
func SplitFilesBeforeSince(files []string, since, now time.Time) (kept, pruned []string) {
	// Entry timestamps can outrun any mtime the filesystem reports when the
	// window starts in the future, so an mtime cannot rule a file out.
	if since.IsZero() || since.After(now) {
		return files, nil
	}
	threshold := since.Add(-mtimePruneMargin)

	// Union every file's session IDs so files sharing any ID share a group.
	groups := sessionGroups{ids: map[string]int{}}
	fileGroups := make([]int, len(files))
	for i, file := range files {
		fileGroups[i] = groups.id(fileSessionID(file))
		if recorded := recordedSessionID(file); recorded != "" {
			groups.union(fileGroups[i], groups.id(recorded))
		}
	}
	live := map[int]bool{}
	for i, file := range files {
		if info, err := os.Stat(file); err != nil || !info.ModTime().Before(threshold) {
			live[groups.root(fileGroups[i])] = true
		}
	}
	for i, file := range files {
		if live[groups.root(fileGroups[i])] {
			kept = append(kept, file)
		} else {
			pruned = append(pruned, file)
		}
	}
	return kept, pruned
}

// sessionGroups is a union-find over session IDs.
type sessionGroups struct {
	ids     map[string]int
	parents []int
}

func (g *sessionGroups) id(session string) int {
	if id, ok := g.ids[session]; ok {
		return id
	}
	id := len(g.parents)
	g.parents = append(g.parents, id)
	g.ids[session] = id
	return id
}

func (g *sessionGroups) root(id int) int {
	for g.parents[id] != id {
		// Path halving: point each visited node at its grandparent.
		g.parents[id] = g.parents[g.parents[id]]
		id = g.parents[id]
	}
	return id
}

func (g *sessionGroups) union(a, b int) {
	g.parents[g.root(a)] = g.root(b)
}

// recordedSessionID returns the first sessionId a transcript records, which
// is the session the loader dedupes its entries within. Transcripts record a
// single session, so reading stops at the first line that names one.
func recordedSessionID(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"sessionId"`)) {
			if raw, _, scanErr := scanLine(bytes.TrimSpace(line)); scanErr == nil {
				if session, _ := raw["sessionId"].(string); session != "" {
					return session
				}
			}
		}
		if err != nil {
			return ""
		}
	}
}
