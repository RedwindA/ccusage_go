package sourcesa

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/RedwindA/ccusage_go/internal/types"
)

func loadAmp(p string) ([]types.UsageEntry, error) {
	m, err := readObject(p)
	if err != nil {
		return nil, err
	}
	session := str(m["id"])
	if session == "" {
		return nil, nil
	}
	messages := arr(m["messages"])
	var result []types.UsageEntry
	if events, ok := obj(m["usageLedger"])["events"].([]interface{}); ok {
		caches := map[int][2]int{}
		for _, v := range messages {
			message := obj(v)
			if str(message["role"]) != "assistant" {
				continue
			}
			if id, ok := message["messageId"].(float64); ok {
				u := obj(message["usage"])
				caches[int(id)] = [2]int{count(u, "cacheCreationInputTokens"), count(u, "cacheReadInputTokens")}
			}
		}
		for _, v := range events {
			ev := obj(v)
			u := obj(ev["tokens"])
			t := timestamp(ev["timestamp"])
			model := str(ev["model"])
			if t.IsZero() || model == "" || u == nil {
				continue
			}
			e := entry(p, "amp", session, "Amp", model, t)
			e.Workspace = "unknown"
			e.ID = str(ev["id"])
			e.InputTokens = count(u, "input")
			e.OutputTokens = count(u, "output")
			if id, ok := ev["toMessageId"].(float64); ok {
				c := caches[int(id)]
				e.CacheCreationInputTokens = c[0]
				e.CacheReadInputTokens = c[1]
			}
			total(&e, count(u, "total"), 0)
			e.Credits, _ = ev["credits"].(float64)
			result = append(result, e)
		}
		return result, nil
	}
	for _, v := range messages {
		m := obj(v)
		if str(m["role"]) != "assistant" {
			continue
		}
		u := obj(m["usage"])
		t := timestamp(first(str(u["timestamp"]), str(m["timestamp"])))
		model := first(str(u["model"]), str(m["model"]))
		if t.IsZero() || model == "" || u == nil {
			continue
		}
		e := entry(p, "amp", session, "Amp", model, t)
		e.Workspace = "unknown"
		e.ID = str(m["messageId"])
		e.InputTokens = count(u, "inputTokens")
		e.OutputTokens = count(u, "outputTokens")
		e.CacheCreationInputTokens = count(u, "cacheCreationInputTokens")
		e.CacheReadInputTokens = count(u, "cacheReadInputTokens")
		e.Credits, _ = u["credits"].(float64)
		total(&e, count(u, "totalTokens"), 0)
		result = append(result, e)
	}
	return result, nil
}
func normalizeDroidModel(s string) string {
	s = strings.TrimPrefix(s, "custom:")
	depth := 0
	var b strings.Builder
	for _, c := range s {
		switch c {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				if c == '.' || unicode.IsSpace(c) {
					c = '-'
				}
				b.WriteRune(unicode.ToLower(c))
			}
		}
	}
	s = b.String()
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}
func droidSessionStart(m object) bool {
	for _, k := range []string{"type", "event", "eventType", "hookEventName"} {
		s := strings.ToLower(str(m[k]))
		s = strings.NewReplacer("-", "", "_", "").Replace(s)
		if s == "sessionstart" {
			return true
		}
	}
	for _, k := range []string{"payload", "data", "session"} {
		if c := obj(m[k]); c != nil && droidSessionStart(c) {
			return true
		}
	}
	return false
}
func nestedCwd(m object) string {
	if s, ok := m["cwd"].(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	for _, k := range []string{"payload", "data", "session"} {
		if c := obj(m[k]); c != nil {
			if s := nestedCwd(c); s != "" {
				return s
			}
		}
	}
	return ""
}
func loadDroid(p string) ([]types.UsageEntry, error) {
	m, err := readObject(p)
	if err != nil {
		return nil, err
	}
	u := obj(m["tokenUsage"])
	if u == nil {
		return nil, nil
	}
	model := str(m["model"])
	provider := strings.ToLower(str(m["providerLock"]))
	cwd, _ := m["cwd"].(string)
	home, _ := os.UserHomeDir()
	custom, _ := readObject(filepath.Join(home, ".factory/settings.json"))
	for _, v := range arr(custom["customModels"]) {
		c := obj(v)
		if str(c["id"]) == model && str(c["model"]) != "" {
			model = str(c["model"])
			if provider == "" {
				provider = str(c["provider"])
			}
			break
		}
	}
	if model == "" || strings.TrimSpace(cwd) == "" {
		lines, _ := readLines(strings.TrimSuffix(p, ".settings.json") + ".jsonl")
		for i, line := range lines {
			if i >= 500 {
				break
			}
			if cwd == "" && droidSessionStart(line) {
				cwd = nestedCwd(line)
			}
		}
		if model == "" {
			b, _ := os.ReadFile(strings.TrimSuffix(p, ".settings.json") + ".jsonl")
			for i, line := range strings.Split(string(b), "\n") {
				if i >= 500 {
					break
				}
				if _, tail, ok := strings.Cut(line, "Model:"); ok {
					parts := strings.FieldsFunc(tail, func(r rune) bool { return r == '"' || r == '\\' || r == '[' })
					if len(parts) > 0 {
						model = strings.TrimSpace(parts[0])
					}
					break
				}
			}
		}
	}
	model = normalizeDroidModel(model)
	if model == "" {
		switch provider {
		case "anthropic", "claude":
			model = "claude-unknown"
		case "openai":
			model = "gpt-unknown"
		case "google", "gemini", "vertex", "vertex_ai", "google_ai":
			model = "gemini-unknown"
		case "xai", "x_ai", "grok":
			model = "grok-unknown"
		default:
			model = "unknown"
		}
	}
	t := timestamp(m["providerLockTimestamp"])
	fallback := t.IsZero()
	if fallback {
		t = fileTime(p)
	}
	e := entry(p, "droid", strings.TrimSuffix(filepath.Base(p), ".settings.json"), first(cwd, filepath.Base(filepath.Dir(p))), model, t)
	e.InputTokens = count(u, "inputTokens")
	e.OutputTokens = count(u, "outputTokens")
	e.CacheCreationInputTokens = count(u, "cacheCreationTokens")
	e.CacheReadInputTokens = count(u, "cacheReadTokens")
	e.ReasoningTokens = count(u, "thinkingTokens")
	e.Raw["provider"] = provider
	e.Raw["pricing_timestamp_missing"] = fallback
	total(&e, count(u, "totalTokens"), e.ReasoningTokens)
	return []types.UsageEntry{e}, nil
}
