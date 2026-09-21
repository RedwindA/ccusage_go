package sourcesa

import (
	"net/url"
	"path/filepath"
	"sort"
	"time"

	"github.com/RedwindA/ccusage_go/internal/types"
)

func loadQwen(p string) ([]types.UsageEntry, error) {
	rows, err := readLines(p)
	if err != nil {
		return nil, err
	}
	var entries []types.UsageEntry
	project := filepath.Base(filepath.Dir(filepath.Dir(p)))
	for _, r := range rows {
		u := obj(r["usageMetadata"])
		if str(r["type"]) != "assistant" || u == nil {
			continue
		}
		t := timestamp(r["timestamp"])
		if t.IsZero() {
			t = fileTime(p)
		}
		e := entry(p, "qwen", first(str(r["sessionId"]), project+"-"+stem(p)), project, str(r["model"]), t)
		e.Workspace = "unknown"
		e.InputTokens = count(u, "promptTokenCount")
		e.OutputTokens = count(u, "candidatesTokenCount")
		e.CacheReadInputTokens = count(u, "cachedContentTokenCount")
		e.ReasoningTokens = count(u, "thoughtsTokenCount")
		total(&e, count(u, "totalTokenCount"), e.ReasoningTokens)
		entries = append(entries, e)
	}
	return entries, nil
}
func geminiCount(m object, keys ...string) int {
	for _, k := range keys {
		if v, ok := m[k].(float64); ok {
			return num(v)
		}
	}
	return 0
}
func geminiEvent(p, session, model string, t time.Time, tokens object, direct bool) types.UsageEntry {
	e := entry(p, "gemini", session, "Gemini", model, t)
	e.Workspace = "unknown"
	input := geminiCount(tokens, "input", "prompt", "input_tokens", "prompt_tokens")
	e.OutputTokens = geminiCount(tokens, "output", "candidates", "output_tokens", "candidates_tokens")
	e.CacheReadInputTokens = geminiCount(tokens, "cached", "cached_tokens")
	e.ReasoningTokens = geminiCount(tokens, "thoughts", "reasoning", "thoughts_tokens", "reasoning_tokens")
	tool := geminiCount(tokens, "tool", "tool_tokens")
	declared := geminiCount(tokens, "total", "total_tokens")
	if !direct || (e.CacheReadInputTokens > 0 && declared == input+e.OutputTokens+e.ReasoningTokens+tool) {
		input = max(0, input-e.CacheReadInputTokens)
	}
	e.InputTokens = input + tool
	total(&e, declared, e.ReasoningTokens)
	return e
}
func loadGemini(p string) ([]types.UsageEntry, error) {
	var rows []object
	var err error
	jsonl := filepath.Ext(p) == ".jsonl"
	if jsonl {
		rows, err = readLines(p)
	} else {
		var m object
		m, err = readObject(p)
		rows = []object{m}
	}
	if err != nil {
		return nil, err
	}
	session, model := stem(p), ""
	fallback := fileTime(p)
	var entries []types.UsageEntry
	ids := map[string]int{}
	var direct func(object, string, time.Time)
	direct = func(r object, hint string, t time.Time) {
		u := obj(r["tokens"])
		model := first(str(r["model"]), hint)
		if u == nil || model == "" {
			return
		}
		if parsed := timestamp(r["timestamp"]); !parsed.IsZero() {
			t = parsed
		} else if parsed = timestamp(r["created_at"]); !parsed.IsZero() {
			t = parsed
		}
		e := geminiEvent(p, session, model, t, u, true)
		e.ID = str(r["id"])
		if jsonl && e.ID != "" {
			if i, ok := ids[e.ID]; ok {
				entries[i] = e
				return
			}
			ids[e.ID] = len(entries)
		}
		entries = append(entries, e)
	}
	for _, r := range rows {
		session = first(str(r["sessionId"]), str(r["session_id"]), session)
		model = first(str(r["model"]), model)
		if messages, ok := r["messages"].([]interface{}); ok && !jsonl {
			t := timestamp(r["startTime"])
			if t.IsZero() {
				t = timestamp(r["lastUpdated"])
			}
			if t.IsZero() {
				t = fallback
			}
			for _, v := range messages {
				m := obj(v)
				if str(m["type"]) == "gemini" {
					direct(m, "", t)
				}
			}
			continue
		}
		if str(r["type"]) == "gemini" {
			hint := ""
			if jsonl {
				hint = model
			}
			direct(r, hint, fallback)
			continue
		}
		stats := obj(r["stats"])
		if stats == nil {
			stats = obj(obj(r["result"])["stats"])
		}
		if stats == nil {
			continue
		}
		t := timestamp(r["timestamp"])
		if t.IsZero() {
			t = fallback
		}
		models := obj(stats["models"])
		added := false
		keys := make([]string, 0, len(models))
		for k := range models {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, m := range keys {
			u := obj(obj(models[m])["tokens"])
			if u == nil {
				continue
			}
			e := geminiEvent(p, session, m, t, u, false)
			if e.TotalTokens > 0 {
				entries = append(entries, e)
				added = true
			}
		}
		if !added {
			entries = append(entries, geminiEvent(p, session, first(model, "unknown"), t, stats, false))
		}
	}
	return entries, nil
}
func loadGrok(p string) ([]types.UsageEntry, error) {
	summary, _ := readObject(filepath.Join(filepath.Dir(p), "summary.json"))
	info := obj(summary["info"])
	session := first(str(info["id"]), filepath.Base(filepath.Dir(p)))
	project := filepath.Base(filepath.Dir(filepath.Dir(p)))
	if decoded, err := url.PathUnescape(project); err == nil {
		project = decoded
	}
	project = first(str(info["cwd"]), str(summary["git_root_dir"]), project)
	defaultModel := first(str(summary["current_model_id"]), "unknown")
	rows, err := readLines(p)
	if err != nil {
		return nil, err
	}
	var result []types.UsageEntry
	seen := map[string]bool{}
	for _, r := range rows {
		params := obj(r["params"])
		update := obj(params["update"])
		u := obj(update["usage"])
		if str(update["sessionUpdate"]) != "turn_completed" || u == nil {
			continue
		}
		meta := obj(params["_meta"])
		t := time.Unix(0, 0)
		if ms := num(meta["agentTimestampMs"]); ms > 0 {
			t = time.UnixMilli(int64(ms))
		} else if sec := num(r["timestamp"]); sec > 0 {
			t = time.Unix(int64(sec), 0)
		}
		models := obj(u["modelUsage"])
		if len(models) == 0 {
			models = object{defaultModel: u}
		}
		keys := make([]string, 0, len(models))
		for k := range models {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, model := range keys {
			u := obj(models[model])
			e := entry(p, "grok", first(str(params["sessionId"]), session), project, model, t)
			input := count(u, "inputTokens")
			e.CacheReadInputTokens = min(input, count(u, "cachedReadTokens"))
			e.CacheCreationInputTokens = min(input-e.CacheReadInputTokens, count(u, "cacheCreationTokens"))
			e.InputTokens = input - e.CacheReadInputTokens - e.CacheCreationInputTokens
			e.OutputTokens = count(u, "outputTokens")
			e.ReasoningTokens = count(u, "reasoningTokens")
			e.TotalTokens = input + e.OutputTokens
			e.ID = str(meta["eventId"])
			if ticks := count(u, "costUsdTicks"); ticks > 0 {
				cost(&e, float64(ticks)/1e10)
			}
			key := signature(e, false)
			if e.ID != "" {
				key = e.ID + "|" + model
			}
			if !seen[key] {
				seen[key] = true
				result = append(result, e)
			}
		}
	}
	return result, nil
}
