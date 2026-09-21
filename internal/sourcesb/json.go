package sourcesb

import (
	"encoding/json"
	"fmt"
	"github.com/RedwindA/ccusage_go/internal/types"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func loadKimi(file string) ([]types.UsageEntry, error) {
	rr, err := lines(file)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(file)
	session := filepath.Base(dir)
	if filepath.Base(filepath.Dir(dir)) == "agents" {
		session = filepath.Base(filepath.Dir(filepath.Dir(dir)))
	}
	root := dir
	for filepath.Base(root) != "sessions" && filepath.Dir(root) != root {
		root = filepath.Dir(root)
	}
	root = filepath.Dir(root)
	config, _ := os.ReadFile(filepath.Join(root, "config.json"))
	model := first(s(decode(config)["model"]), "kimi-for-coding")
	var out []types.UsageEntry
	for _, r := range rr {
		e := types.UsageEntry{Model: model, SourceFile: file, SessionID: session, Timestamp: mtime(file), Raw: r}
		var u obj
		if s(r["type"]) == "usage.record" {
			if s(r["usageScope"]) != "turn" {
				continue
			}
			u = object(r["usage"])
			e.Model = strings.TrimPrefix(first(s(r["model"]), "kimi-for-coding"), "kimi-code/")
			if r["time"] != nil {
				e.Timestamp = time.UnixMilli(int64(f(r["time"])))
			}
			e.InputTokens = n(u["inputOther"])
			e.OutputTokens = n(u["output"])
			e.CacheCreationInputTokens = n(u["inputCacheCreation"])
			e.CacheReadInputTokens = n(u["inputCacheRead"])
		} else {
			m := object(r["message"])
			if s(m["type"]) != "StatusUpdate" {
				continue
			}
			p := object(m["payload"])
			u = object(p["token_usage"])
			e.ID = s(p["message_id"])
			e.InputTokens = n(u["input_other"])
			e.OutputTokens = n(u["output"])
			e.CacheCreationInputTokens = n(u["input_cache_creation"])
			e.CacheReadInputTokens = n(u["input_cache_read"])
			if r["timestamp"] != nil {
				e.Timestamp = time.UnixMilli(int64(f(r["timestamp"]) * 1000))
			}
		}
		finish(&e, n(u["total"]))
		if e.TotalTokens == 0 {
			continue
		}
		e.ID = fmt.Sprintf("kimi:%s:%s:%d:%s:%d:%d:%d:%d:%d", session, e.ID, e.Timestamp.UnixMilli(), e.Model, e.InputTokens, e.OutputTokens, e.CacheCreationInputTokens, e.CacheReadInputTokens, e.ReasoningTokens)
		out = append(out, e)
	}
	return out, nil
}
func codeUsage(u obj) types.UsageEntry {
	e := types.UsageEntry{Model: s(u["model"]), Credits: max(0, f(u["credits"])), InputTokens: n(pick(u, "inputTokens", "input_tokens", "promptTokens", "prompt_tokens")), OutputTokens: n(pick(u, "outputTokens", "output_tokens", "completionTokens", "completion_tokens")), CacheCreationInputTokens: n(pick(u, "cacheCreationInputTokens", "cache_creation_input_tokens", "cacheCreationTokens", "cache_creation_tokens", "cachedTokensCreated", "cached_tokens_created")), CacheReadInputTokens: n(pick(u, "cacheReadInputTokens", "cache_read_input_tokens"))}
	e.CacheReadInputTokens = max(e.CacheReadInputTokens, n(object(u["promptTokensDetails"])["cachedTokens"]), n(object(u["prompt_tokens_details"])["cached_tokens"]))
	finish(&e, n(pick(u, "totalTokens", "total_tokens", "total")))
	return e
}
func mergeCode(e *types.UsageEntry, x types.UsageEntry) {
	if e.Model == "" {
		e.Model = x.Model
	}
	if e.InputTokens == 0 {
		e.InputTokens = x.InputTokens
	}
	if e.OutputTokens == 0 {
		e.OutputTokens = x.OutputTokens
	}
	if e.CacheReadInputTokens == 0 {
		e.CacheReadInputTokens = x.CacheReadInputTokens
	}
	if e.CacheCreationInputTokens == 0 {
		e.CacheCreationInputTokens = x.CacheCreationInputTokens
	}
	if e.ReasoningTokens == 0 {
		e.ReasoningTokens = x.ReasoningTokens
	}
	if e.Credits <= 0 {
		e.Credits = x.Credits
	}
}
func loadCodebuff(file string) ([]types.UsageEntry, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var rr []obj
	if json.Unmarshal(b, &rr) != nil {
		return nil, nil
	}
	chat := filepath.Base(filepath.Dir(file))
	projectDir := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	project := filepath.Base(projectDir)
	channel := filepath.Base(filepath.Dir(filepath.Dir(projectDir)))
	session := channel + "/" + project + "/" + chat
	var out []types.UsageEntry
	for i, r := range rr {
		role := s(pick(r, "variant", "role"))
		if role != "ai" && role != "agent" && role != "assistant" {
			continue
		}
		md := object(r["metadata"])
		e := types.UsageEntry{Model: s(md["model"]), SessionID: session, SourceFile: file, Raw: r}
		mergeCode(&e, codeUsage(object(md["usage"])))
		mergeCode(&e, codeUsage(object(object(md["codebuff"])["usage"])))
		h, _ := object(object(object(md["runState"])["sessionState"])["mainAgentState"])["messageHistory"].([]interface{})
		for j := len(h) - 1; j >= 0; j-- {
			item := object(h[j])
			if s(item["role"]) != "assistant" {
				continue
			}
			po := object(item["providerOptions"])
			x := codeUsage(object(po["usage"]))
			cb := object(po["codebuff"])
			mergeCode(&x, codeUsage(object(cb["usage"])))
			x.Model = first(s(cb["model"]), x.Model)
			mergeCode(&e, x)
		}
		if e.Credits <= 0 {
			e.Credits = max(0, f(r["credits"]))
		}
		finish(&e, 0)
		if e.TotalTokens == 0 && e.Credits <= 0 {
			continue
		}
		e.Model = first(e.Model, "codebuff-unknown")
		e.Timestamp = ts(pick(r, "timestamp", "createdAt"))
		if e.Timestamp.IsZero() {
			e.Timestamp = ts(md["timestamp"])
		}
		if e.Timestamp.IsZero() {
			parts := strings.SplitN(chat, "T", 2)
			if len(parts) == 2 {
				e.Timestamp = ts(parts[0] + "T" + strings.Replace(parts[1], "-", ":", 2))
			}
		}
		if e.Timestamp.IsZero() {
			e.Timestamp = mtime(file)
		}
		e.ID = "codebuff:" + session + ":" + first(s(r["id"]), fmt.Sprintf("%d:%d", e.Timestamp.UnixMilli(), i))
		out = append(out, e)
	}
	return out, nil
}
