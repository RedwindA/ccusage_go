// Package sourcesb reads usage formats maintained by the upstream ccusage adapters.
package sourcesb

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/RedwindA/ccusage_go/internal/types"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type obj = map[string]interface{}

func Names() []string {
	return []string{"opencode", "codebuff", "hermes", "goose", "kilo", "kimi", "copilot", "antigravity", "zcode"}
}
func DefaultPathsForHome(name, home string) []string { return defaultPaths(name, home, false) }
func DefaultPaths(name, home string) []string        { return defaultPaths(name, home, true) }
func defaultPaths(name, home string, envEnabled bool) []string {
	env := map[string]string{"opencode": "OPENCODE_DATA_DIR", "codebuff": "CODEBUFF_DATA_DIR", "hermes": "HERMES_HOME", "kilo": "KILO_DATA_DIR", "kimi": "KIMI_DATA_DIR", "antigravity": "ANTIGRAVITY_DATA_DIR", "zcode": "ZCODE_HOME"}[name]
	var roots []string
	if v, ok := os.LookupEnv(env); ok && envEnabled {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				roots = append(roots, p)
			}
		}
		if name != "zcode" || len(roots) > 0 {
			return roots
		}
	}
	rel := map[string][]string{"opencode": {".local/share/opencode"}, "codebuff": {".config/manicode", ".config/manicode-dev", ".config/manicode-staging"}, "hermes": {".hermes"}, "goose": {".local/share/goose/sessions", "Library/Application Support/goose/sessions", ".local/share/Block/goose/sessions"}, "kilo": {".local/share/kilo"}, "kimi": {".kimi", ".kimi-code"}, "copilot": {".copilot"}, "antigravity": {".gemini/antigravity", ".gemini/antigravity-cli", ".gemini/antigravity-ide", ".gemini/antigravity-backup", ".config/antigravity"}, "zcode": {".zcode"}}[name]
	for _, p := range rel {
		roots = append(roots, filepath.Join(home, p))
	}
	if envEnabled && name == "opencode" && filepath.IsAbs(os.Getenv("XDG_DATA_HOME")) {
		roots = []string{filepath.Join(os.Getenv("XDG_DATA_HOME"), "opencode")}
	}
	if envEnabled && name == "goose" && strings.TrimSpace(os.Getenv("GOOSE_PATH_ROOT")) != "" {
		roots = []string{filepath.Join(strings.TrimSpace(os.Getenv("GOOSE_PATH_ROOT")), "data/sessions")}
	}
	if envEnabled && name == "copilot" {
		if v := strings.TrimSpace(os.Getenv("COPILOT_HOME")); v != "" {
			roots = []string{v}
		}
		if v := strings.TrimSpace(os.Getenv("COPILOT_OTEL_FILE_EXPORTER_PATH")); v != "" {
			roots = append(roots, v)
		}
	}
	return roots
}
func Load(ctx context.Context, name string, paths []string, loc *time.Location) ([]types.UsageEntry, error) {
	return LoadUntil(ctx, name, paths, loc, time.Time{})
}

// LoadUntil bounds cumulative snapshot reconciliation before the report filters rows.
func LoadUntil(ctx context.Context, name string, paths []string, loc *time.Location, untilExclusive time.Time) ([]types.UsageEntry, error) {
	known := false
	for _, n := range Names() {
		known = known || n == name
	}
	if !known {
		return nil, fmt.Errorf("unknown source %q", name)
	}
	if loc == nil {
		loc = time.Local
	}
	files := []string{}
	seenPaths := map[string]bool{}
	for _, root := range paths {
		info, err := os.Stat(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			switch name {
			case "hermes":
				root = filepath.Join(root, "state.db")
			case "zcode":
				root = filepath.Join(root, "cli/db/db.sqlite")
			case "goose":
				root = filepath.Join(root, "sessions.db")
			case "kilo":
				root = filepath.Join(root, "kilo.db")
			case "codebuff":
				if filepath.Base(root) != "projects" {
					root = filepath.Join(root, "projects")
				}
			case "kimi":
				if filepath.Base(root) != "sessions" {
					root = filepath.Join(root, "sessions")
				}
			case "antigravity":
				if i, e := os.Stat(filepath.Join(root, "conversations")); e == nil && i.IsDir() {
					root = filepath.Join(root, "conversations")
				}
			}
		}
		if resolved, e := filepath.EvalSymlinks(root); e == nil {
			root = resolved
		}
		selectedDB := ""
		if name == "opencode" && info.IsDir() {
			selectedDB = openCodeDB(root)
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e != nil {
				if os.IsNotExist(e) {
					return nil
				}
				return e
			}
			if d.IsDir() {
				return nil
			}
			base := d.Name()
			accept := false
			switch name {
			case "opencode":
				accept = (!info.IsDir() && strings.HasSuffix(base, ".db") || p == selectedDB) || strings.HasSuffix(p, ".json") && strings.Contains(filepath.ToSlash(p), "/storage/message/")
			case "codebuff":
				accept = base == "chat-messages.json"
			case "kimi":
				rel, _ := filepath.Rel(root, p)
				count := len(strings.Split(filepath.ToSlash(rel), "/"))
				accept = base == "wire.jsonl" && (!info.IsDir() || count == 3 || count == 5)
			case "copilot":
				rel, _ := filepath.Rel(root, p)
				parts := strings.Split(filepath.ToSlash(rel), "/")
				accept = !info.IsDir() || len(parts) == 3 && parts[0] == "session-state" && base == "events.jsonl" || strings.HasSuffix(base, ".jsonl") && len(parts) > 1 && parts[0] == "otel"
			default:
				accept = strings.HasSuffix(base, ".db") || strings.HasSuffix(base, ".sqlite")
			}
			if accept {
				canonical, _ := filepath.EvalSymlinks(p)
				if canonical == "" {
					canonical = p
				}
				if !seenPaths[canonical] {
					seenPaths[canonical] = true
					files = append(files, p)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		di := strings.HasSuffix(files[i], ".db")
		dj := strings.HasSuffix(files[j], ".db")
		if di != dj {
			return di
		}
		return false
	})
	var out []types.UsageEntry
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var entries []types.UsageEntry
		var err error
		switch name {
		case "codebuff":
			entries, err = loadCodebuff(file)
		case "kimi":
			entries, err = loadKimi(file)
		case "copilot":
			entries, err = loadCopilot(file)
		case "opencode":
			if strings.HasSuffix(file, ".json") {
				var b []byte
				b, err = os.ReadFile(file)
				if err == nil {
					m := decode(b)
					if e, ok := openMessage(m, "opencode", file, "", "", nil); ok {
						entries = append(entries, e)
					}
				}
			} else {
				entries, err = loadDatabase(ctx, name, file)
			}
		case "antigravity":
			entries, err = loadAntigravity(ctx, file)
		default:
			entries, err = loadDatabase(ctx, name, file)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		out = append(out, entries...)
	}
	if name == "copilot" {
		out = reconcileCopilot(out, untilExclusive)
	}
	if name == "antigravity" {
		out = dedupAntigravity(out)
	}
	messageSessions := map[string]bool{}
	if name == "opencode" {
		for _, e := range out {
			if e.Raw["session_aggregate"] != true {
				messageSessions[e.SessionID] = true
			}
		}
	}
	dedup := map[string]bool{}
	result := make([]types.UsageEntry, 0, len(out))
	for _, e := range out {
		if e.Raw["session_aggregate"] == true && messageSessions[e.SessionID] {
			continue
		}
		if e.ID != "" && name != "antigravity" {
			key := e.ID
			if name == "goose" {
				key = e.SourceFile + ":" + key
			}
			if dedup[key] {
				continue
			}
			dedup[key] = true
		}
		e.Agent = name
		// These adapters keep reasoning/remainder tokens outside visible output.
		// Copilot included-output reasoning is separately kept in Raw.
		if e.ReasoningTokens > 0 {
			if e.Raw == nil {
				e.Raw = obj{}
			}
			e.Raw["reasoning_is_extra"] = true
		}
		e.SourceFile = filepath.Clean(e.SourceFile)
		e.DateKey = e.Timestamp.In(loc).Format("2006-01-02")
		if e.ProjectPath == "" {
			e.ProjectPath = map[string]string{"opencode": "OpenCode", "kilo": "Kilo", "kimi": "Kimi", "codebuff": "Codebuff", "hermes": "Hermes", "goose": "Goose", "copilot": "GitHub Copilot CLI", "antigravity": "Antigravity", "zcode": "ZCode"}[name]
			if e.Workspace == "" {
				e.Workspace = "unknown"
				if name == "antigravity" {
					e.Workspace = "Antigravity"
				}
			}
		}
		if e.SessionID == "" {
			e.SessionID = "unknown"
		}
		result = append(result, e)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Timestamp.Before(result[j].Timestamp) })
	return result, nil
}
func decode(b []byte) obj      { var m obj; _ = json.Unmarshal(b, &m); return m }
func object(v interface{}) obj { m, _ := v.(map[string]interface{}); return m }
func s(v interface{}) string {
	switch v := v.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return string(v)
	}
	return ""
}
func f(v interface{}) float64 {
	switch v := v.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		n, _ := v.Float64()
		return n
	case string:
		n, _ := strconv.ParseFloat(v, 64)
		return n
	}
	return 0
}
func n(v interface{}) int { return max(0, int(f(v))) }
func pick(m obj, keys ...string) interface{} {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil && v != "" {
			return v
		}
	}
	return nil
}
func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
func ts(v interface{}) time.Time {
	if str := s(v); str != "" {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
			if t, e := time.Parse(layout, str); e == nil {
				return t
			}
		}
	}
	num := f(v)
	if num > 0 {
		if num < 1e11 {
			num *= 1000
		}
		return time.UnixMilli(int64(num))
	}
	return time.Time{}
}
func mtime(p string) time.Time {
	i, e := os.Stat(p)
	if e == nil {
		return i.ModTime()
	}
	return time.Unix(0, 0)
}
func finish(e *types.UsageEntry, total int) {
	known := e.InputTokens + e.OutputTokens + e.CacheCreationInputTokens + e.CacheReadInputTokens + e.ReasoningTokens
	if total > known {
		if e.OutputTokens == 0 {
			e.OutputTokens = total - known
		} else {
			e.ReasoningTokens += total - known
		}
	}
	e.TotalTokens = e.InputTokens + e.OutputTokens + e.CacheCreationInputTokens + e.CacheReadInputTokens + e.ReasoningTokens
}
func lines(file string) ([]obj, error) {
	f, e := os.Open(file)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	var records []obj
	for sc.Scan() {
		if m := decode(sc.Bytes()); m != nil {
			records = append(records, m)
		}
	}
	return records, sc.Err()
}

func openCodeDB(root string) string {
	defaultPath := filepath.Join(root, "opencode.db")
	if i, e := os.Stat(defaultPath); e == nil && !i.IsDir() {
		return defaultPath
	}
	rr, _ := os.ReadDir(root)
	for _, r := range rr {
		name := r.Name()
		if r.IsDir() || !strings.HasPrefix(name, "opencode-") || !strings.HasSuffix(name, ".db") {
			continue
		}
		valid := true
		for _, c := range strings.TrimSuffix(strings.TrimPrefix(name, "opencode-"), ".db") {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				valid = false
				break
			}
		}
		if valid {
			return filepath.Join(root, name)
		}
	}
	return ""
}
