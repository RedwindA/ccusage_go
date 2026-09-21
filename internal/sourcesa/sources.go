// Package sourcesa reads native usage stores for Codex, Droid, Amp, Pi,
// OpenClaw, Gemini, Grok, and Qwen. It does not assign estimated prices.
package sourcesa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RedwindA/ccusage_go/internal/types"
)

type object = map[string]interface{}

func Names() []string {
	return []string{"codex", "droid", "amp", "pi", "openclaw", "gemini", "grok", "qwen"}
}
func DefaultPaths(name, home string) []string {
	env := map[string]string{"codex": "CODEX_HOME", "droid": "DROID_SESSIONS_DIR", "amp": "AMP_DATA_DIR", "pi": "PI_AGENT_DIR", "openclaw": "OPENCLAW_DIR", "gemini": "GEMINI_DATA_DIR", "grok": "GROK_HOME", "qwen": "QWEN_DATA_DIR"}[name]
	if v, ok := os.LookupEnv(env); ok && (strings.TrimSpace(v) != "" || (name != "pi" && name != "openclaw" && name != "grok")) {
		if name == "grok" {
			return []string{strings.TrimSpace(v)}
		}
		return splitPaths(v)
	}
	return DefaultPathsForHome(name, home)
}

// DefaultPathsForHome returns native defaults without consulting environment overrides.
func DefaultPathsForHome(name, home string) []string {
	defaults := map[string][]string{"codex": {".codex"}, "droid": {".factory/sessions"}, "amp": {".local/share/amp"}, "pi": {".pi/agent/sessions"}, "openclaw": {".openclaw", ".clawdbot", ".moltbot", ".moldbot"}, "gemini": {".gemini/tmp"}, "grok": {".grok"}, "qwen": {".qwen"}}
	var out []string
	for _, p := range defaults[name] {
		out = append(out, filepath.Join(home, p))
	}
	return out
}
func splitPaths(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
func Load(ctx context.Context, name string, paths []string, loc *time.Location) ([]types.UsageEntry, error) {
	return LoadWithMode(ctx, name, paths, loc, "auto")
}

// LoadWithMode preserves cost-mode-sensitive replay identity in Pi histories.
func LoadWithMode(ctx context.Context, name string, paths []string, loc *time.Location, mode string) ([]types.UsageEntry, error) {
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != "calculate" && mode != "display" {
		return nil, fmt.Errorf("invalid cost mode %q", mode)
	}
	valid := false
	for _, n := range Names() {
		valid = valid || n == name
	}
	if !valid {
		return nil, fmt.Errorf("unsupported source %q", name)
	}
	if loc == nil {
		loc = time.Local
	}
	if paths == nil {
		h, _ := os.UserHomeDir()
		paths = DefaultPaths(name, h)
	}
	files, discoveryErr := discover(ctx, name, paths)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if name == "codex" {
		entries, err := loadCodex(ctx, files, loc)
		return entries, errors.Join(discoveryErr, err)
	}
	if name == "pi" {
		entries, err := loadPi(ctx, files, loc, mode)
		return entries, errors.Join(discoveryErr, err)
	}
	var loadErrors []error
	if discoveryErr != nil {
		loadErrors = append(loadErrors, discoveryErr)
	}
	var err error
	var result []types.UsageEntry
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var entries []types.UsageEntry
		switch name {
		case "amp":
			entries, err = loadAmp(f)
		case "droid":
			entries, err = loadDroid(f)
		case "gemini":
			entries, err = loadGemini(f)
		case "qwen":
			entries, err = loadQwen(f)
		case "grok":
			entries, err = loadGrok(f)
		case "openclaw":
			if filepath.Ext(f) == ".sqlite" {
				entries, err = loadOpenClawDB(ctx, f)
			} else {
				entries, err = loadOpenClaw(f)
			}
		}
		// Preserve usable records while reporting damaged or unreadable source files.
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			loadErrors = append(loadErrors, fmt.Errorf("%s source %s: %w", name, f, err))
		}
		result = append(result, entries...)
	}
	if name == "openclaw" {
		result = mergeOpenClaw(result)
	}
	return finalize(result, loc, name != "amp" && name != "droid" && name != "openclaw" && name != "grok"), errors.Join(loadErrors...)
}
func discover(ctx context.Context, name string, paths []string) ([]string, error) {
	var files []string
	var discoveryErrors []error
	seen := map[string]bool{}
	archive := map[string]bool{}
	for _, root := range paths {
		root = filepath.Clean(root)
		st, e := os.Stat(root)
		if e != nil {
			if os.IsNotExist(e) {
				continue
			}
			discoveryErrors = append(discoveryErrors, fmt.Errorf("%s source %s: %w", name, root, e))
			continue
		}
		roots := []string{root}
		if st.IsDir() {
			switch name {
			case "codex":
				roots = nil
				for _, d := range []string{"sessions", "archived_sessions"} {
					p := filepath.Join(root, d)
					if s, e := os.Stat(p); e == nil && s.IsDir() {
						roots = append(roots, p)
					}
				}
				if len(roots) == 0 {
					roots = []string{root}
				}
			case "amp":
				if filepath.Base(root) != "threads" {
					roots = []string{filepath.Join(root, "threads")}
				}
			case "grok":
				if filepath.Base(root) != "sessions" {
					roots = []string{filepath.Join(root, "sessions")}
				}
			case "qwen":
				if filepath.Base(root) != "projects" {
					roots = []string{filepath.Join(root, "projects")}
				}
			}
		}
		for _, r := range roots {
			e := filepath.WalkDir(r, func(p string, d fs.DirEntry, e error) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if e != nil {
					if os.IsNotExist(e) {
						return nil
					}
					return e
				}
				if d.Type()&os.ModeSymlink != 0 {
					return nil
				}
				if d.IsDir() {
					if name == "pi" && d.Name() == "subagent-artifacts" {
						return filepath.SkipDir
					}
					return nil
				}
				ok := false
				b := d.Name()
				switch name {
				case "codex", "pi":
					ok = strings.HasSuffix(b, ".jsonl")
				case "amp":
					ok = strings.HasSuffix(b, ".json")
				case "droid":
					ok = strings.HasSuffix(b, ".settings.json")
				case "gemini":
					ok = strings.HasSuffix(b, ".json") || strings.HasSuffix(b, ".jsonl")
				case "qwen":
					ok = strings.HasSuffix(b, ".jsonl") && filepath.Base(filepath.Dir(p)) == "chats" && filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(p)))) == "projects"
				case "grok":
					ok = b == "updates.jsonl"
				case "openclaw":
					ok = strings.HasSuffix(b, ".jsonl") || strings.Contains(b, ".jsonl.deleted.") || strings.Contains(b, ".jsonl.reset.") || b == "openclaw-agent.sqlite"
				}
				if ok && !seen[p] {
					if name == "codex" && len(roots) > 1 {
						rel, _ := filepath.Rel(r, p)
						key := root + "/" + rel
						if archive[key] {
							return nil
						}
						archive[key] = true
					}
					seen[p] = true
					files = append(files, p)
				}
				return nil
			})
			if e != nil {
				if ctx.Err() != nil {
					return files, ctx.Err()
				}
				discoveryErrors = append(discoveryErrors, fmt.Errorf("%s source %s: %w", name, r, e))
			}
		}
	}
	// Preserve root order and active-before-archive ordering; WalkDir is lexical.
	return files, errors.Join(discoveryErrors...)
}
func readObject(path string) (object, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var m object
	e = json.Unmarshal(b, &m)
	return m, e
}
func readLines(path string) ([]object, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var rows []object
	for _, line := range strings.Split(string(b), "\n") {
		var m object
		if json.Unmarshal([]byte(line), &m) == nil && m != nil {
			rows = append(rows, m)
		}
	}
	return rows, nil
}
func obj(v interface{}) object        { m, _ := v.(map[string]interface{}); return m }
func arr(v interface{}) []interface{} { a, _ := v.([]interface{}); return a }
func str(v interface{}) string        { s, _ := v.(string); return strings.TrimSpace(s) }
func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
func num(v interface{}) int {
	n, ok := v.(float64)
	if !ok || n < 0 || math.IsInf(n, 0) || math.IsNaN(n) || n > float64(math.MaxInt) {
		return 0
	}
	return int(n)
}
func integer(v interface{}) int {
	n, ok := v.(float64)
	if !ok || n != math.Trunc(n) {
		return 0
	}
	return num(v)
}
func count(m object, keys ...string) int {
	for _, k := range keys {
		if v, ok := m[k].(float64); ok && v >= 0 && v == math.Trunc(v) {
			return integer(v)
		}
	}
	return 0
}
func timestamp(v interface{}) time.Time {
	if s, ok := v.(string); ok {
		t, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
		return t
	}
	if n, ok := v.(float64); ok && n >= 0 {
		if n < 1e11 {
			return time.Unix(int64(n), int64((n-math.Trunc(n))*1e9))
		}
		return time.UnixMilli(int64(n))
	}
	return time.Time{}
}
func fileTime(p string) time.Time {
	s, e := os.Stat(p)
	if e != nil {
		return time.Unix(0, 0)
	}
	return s.ModTime()
}
func entry(p, agent, session, project, model string, t time.Time) types.UsageEntry {
	return types.UsageEntry{SourceFile: p, Agent: agent, SessionID: session, ProjectPath: project, Workspace: project, Model: first(model, "unknown"), Timestamp: t, Raw: object{}}
}
func cost(e *types.UsageEntry, v interface{}) {
	if n, ok := v.(float64); ok && !math.IsNaN(n) && !math.IsInf(n, 0) {
		e.Cost = n
		e.HasCost = true
	}
}
func total(e *types.UsageEntry, declared, extra int) {
	known := e.InputTokens + e.OutputTokens + e.CacheReadInputTokens + e.CacheCreationInputTokens + extra
	if missing := declared - known; missing > 0 {
		if e.OutputTokens == 0 {
			e.OutputTokens = missing
		} else {
			extra += missing
		}
	}
	e.TotalTokens = e.InputTokens + e.OutputTokens + e.CacheReadInputTokens + e.CacheCreationInputTokens + extra
	if extra > 0 {
		e.Raw["extra_output_tokens"] = extra
	}
}
func signature(e types.UsageEntry, withCost bool) string {
	s := fmt.Sprintf("%s|%s|%s|%d|%d|%d|%d|%d|%d", e.SessionID, e.Timestamp.Format(time.RFC3339Nano), e.Model, e.InputTokens, e.OutputTokens, e.CacheReadInputTokens, e.CacheCreationInputTokens, e.ReasoningTokens, e.TotalTokens)
	if withCost {
		s += fmt.Sprintf("|%t|%g", e.HasCost, e.Cost)
	}
	return s
}
func finalize(entries []types.UsageEntry, loc *time.Location, dedupe bool) []types.UsageEntry {
	out := make([]types.UsageEntry, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		if e.TotalTokens == 0 && e.ReasoningTokens == 0 {
			continue
		}
		key := signature(e, true)
		if dedupe && seen[key] {
			continue
		}
		seen[key] = true
		e.DateKey = e.Timestamp.In(loc).Format("2006-01-02")
		if e.ID == "" {
			e.ID = key
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	return out
}
func stem(p string) string { return strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)) }
