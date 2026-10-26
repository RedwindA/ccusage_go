package commands

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/RedwindA/ccusage_go/internal/types"
)

// zoneDirs are where ccusage's jiff finds the zone database on Unix.
var zoneDirs = []string{"/usr/share/zoneinfo", "/usr/share/lib/zoneinfo", "/etc/zoneinfo"}

//go:generate go run gen_zone_names.go $GOROOT/lib/time/zoneinfo.zip

// loadTimezone resolves a --timezone or configured timezone as ccusage
// does: "local" is the system timezone, and an IANA name matches the zone
// database's in any case and takes its spelling. Windows has no zone
// directory, and jiff bundles its database there, so a name also matches
// one of zoneNames.
func loadTimezone(name string) (*time.Location, error) {
	if name == "local" {
		return time.Local, nil
	}
	return time.LoadLocation(zoneSpelling(name))
}

// zoneSpelling returns the zone database's spelling of name, or name when
// no zone matches it. It is found before the zone is loaded: on a file
// system that ignores case, as macOS's does, time.LoadLocation loads a zone
// written in another case but names it as written.
func zoneSpelling(name string) string {
	if slices.Contains(zoneNames, name) {
		return name
	}
	for _, dir := range zoneDirs {
		if zone, ok := zoneName(dir, name); ok {
			return zone
		}
	}
	if i := slices.IndexFunc(zoneNames, func(zone string) bool { return strings.EqualFold(zone, name) }); i >= 0 {
		return zoneNames[i]
	}
	return name
}

// zoneName returns the path of the file under dir that name, a
// slash-separated path, names in any case.
func zoneName(dir, name string) (string, bool) {
	var parts []string
	for _, part := range strings.Split(name, "/") {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", false
		}
		i := slices.IndexFunc(entries, func(e os.DirEntry) bool { return strings.EqualFold(e.Name(), part) })
		if i < 0 {
			return "", false
		}
		parts = append(parts, entries[i].Name())
		dir = filepath.Join(dir, entries[i].Name())
	}
	return strings.Join(parts, "/"), true
}

func getDefaultDataPath() string {
	// Check environment variable first
	if claudeConfigDir := os.Getenv("CLAUDE_CONFIG_DIR"); claudeConfigDir != "" {
		return claudeConfigDir
	}

	// Default paths based on Claude Code configuration
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "."
	}

	// Check ~/.claude/projects first
	claudePath := filepath.Join(homeDir, ".claude", "projects")
	if _, err := os.Stat(claudePath); err == nil {
		return claudePath
	}

	// Check ~/.config/claude/projects
	configPath := filepath.Join(homeDir, ".config", "claude", "projects")
	if _, err := os.Stat(configPath); err == nil {
		return configPath
	}

	// Fall back to ~/.claude/projects as default
	return claudePath
}

func filterEntriesBySessionID(entries []types.UsageEntry, sessionID string) []types.UsageEntry {
	var filtered []types.UsageEntry
	for _, entry := range entries {
		if entry.SessionID == sessionID {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func filterEntriesBySessionName(entries []types.UsageEntry, sessionName string) []types.UsageEntry {
	if sessionName == "" {
		return entries
	}
	var filtered []types.UsageEntry
	for _, entry := range entries {
		if entry.SessionName == sessionName {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func filterEntriesByDate(entries []types.UsageEntry, since, until string) []types.UsageEntry {
	var filtered []types.UsageEntry

	for _, entry := range entries {
		// Use DateKey if available, otherwise format timestamp
		dateStr := entry.DateKey
		if dateStr == "" {
			dateStr = entry.Timestamp.Format("2006-01-02")
		}

		// Apply date filter
		if since != "" && dateStr < since {
			continue
		}
		if until != "" && dateStr > until {
			continue
		}

		filtered = append(filtered, entry)
	}

	return filtered
}
