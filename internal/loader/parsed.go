package loader

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/RedwindA/ccusage_go/internal/types"
)

// ParsedFiles keeps the transcripts loads parse, so the loads of one run,
// such as a date-bounded view and the whole history, read each file once and
// only deduplicate it again. A kept file keeps the content its first load
// read. Loads sharing ParsedFiles must use one timezone and debug setting;
// SetDailyLoader may differ between them. The zero value is ready to use.
type ParsedFiles struct {
	files map[string]parsedFile
}

// SetParsedFiles makes later loads reuse the files kept in parsed and keep
// the files they parse there.
func (l *Loader) SetParsedFiles(parsed *ParsedFiles) {
	l.parsed = parsed
}

// lookup returns the kept parse of path; a nil ParsedFiles keeps none.
func (p *ParsedFiles) lookup(path string) (parsedFile, bool) {
	if p == nil {
		return parsedFile{}, false
	}
	parsed, ok := p.files[path]
	return parsed, ok
}

func (p *ParsedFiles) keep(path string, parsed parsedFile) {
	if p == nil {
		return
	}
	if p.files == nil {
		p.files = map[string]parsedFile{}
	}
	p.files[path] = parsed
}

// parsedFile is one transcript as parseFile reads it, for loads of either
// kind.
type parsedFile struct {
	// entries holds the file's usage in line order. dailyOnly marks the ones
	// only daily loads count: those read from the record an AgentProgress
	// line wraps, and those whose line holds isApiErrorMessage as another
	// type than a bool.
	entries      []types.UsageEntry
	dailyOnly    []bool
	sessionNames map[string]string
	// entryDebugMsg and dailyDebugMsg summarize the parse errors entry and
	// daily loads see, when debugging.
	entryDebugMsg, dailyDebugMsg string
	err                          error
}

// usage returns the entries a load sees and the summary of its parse errors;
// daily selects a load that reads as upstream's daily loader.
func (p parsedFile) usage(daily bool) ([]types.UsageEntry, string) {
	if daily {
		return p.entries, p.dailyDebugMsg
	}
	if !slices.Contains(p.dailyOnly, true) {
		return p.entries, p.entryDebugMsg
	}
	var entries []types.UsageEntry
	for i, e := range p.entries {
		if !p.dailyOnly[i] {
			entries = append(entries, e)
		}
	}
	return entries, p.entryDebugMsg
}

// parseErrors tallies the lines of a file that fail to parse.
type parseErrors struct {
	count int
	first string
}

// add counts a line; with debug on, describe gives the first one's message.
func (p *parseErrors) add(debug bool, describe func() string) {
	p.count++
	if debug && p.first == "" {
		p.first = describe()
	}
}

// summary is the debug message of a file with these parse errors.
func (p parseErrors) summary(path string) string {
	if p.count == 0 {
		return ""
	}
	msg := fmt.Sprintf("Debug: File %s had %d parse errors\n", filepath.Base(path), p.count)
	if p.first != "" {
		msg += fmt.Sprintf("  First error: %s\n", p.first)
	}
	return msg
}
