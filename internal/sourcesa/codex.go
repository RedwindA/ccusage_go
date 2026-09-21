package sourcesa

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/RedwindA/ccusage_go/internal/types"
)

type codexUsage [6]int // inclusive input, cache read, cache creation, output, reasoning, total
func parseCodexUsage(m object) codexUsage {
	u := codexUsage{count(m, "input_tokens", "prompt_tokens", "input"), count(m, "cached_input_tokens", "cache_read_input_tokens", "cached_tokens"), count(m, "cache_write_input_tokens", "cache_creation_input_tokens"), count(m, "output_tokens", "completion_tokens", "output"), count(m, "reasoning_output_tokens", "reasoning_tokens"), count(m, "total_tokens")}
	u[1] = min(u[1], u[0])
	u[2] = min(u[2], u[0]-u[1])
	if u[5] == 0 {
		u[5] = u[0] + u[3]
	}
	return u
}
func codexModel(m object) string {
	return first(str(m["model"]), str(m["model_name"]), str(obj(m["metadata"])["model"]))
}
func codexAutoReview(t time.Time) string {
	date := t.Format("2006-01-02")
	for _, r := range [][2]string{{"2026-07-30", "gpt-5.6-luna"}, {"2026-03-05", "gpt-5.4"}, {"2026-02-05", "gpt-5.3-codex"}, {"2025-12-11", "gpt-5.2-codex"}, {"2025-11-13", "gpt-5.1-codex"}, {"2025-09-15", "gpt-5-codex"}, {"2025-08-07", "gpt-5"}} {
		if date >= r[0] {
			return r[1]
		}
	}
	return "gpt-5"
}

type codexSession struct {
	entries    []types.UsageEntry
	usages     []codexUsage
	id, parent string
	fork       time.Time
}

func parseCodex(p string) (codexSession, error) {
	rows, err := readLines(p)
	s := codexSession{}
	if err != nil {
		return s, err
	}
	session := stem(p)
	parts := strings.Split(filepath.ToSlash(p), "/")
	for i, part := range parts {
		if (part == "sessions" || part == "archived_sessions") && i+1 < len(parts) {
			session = strings.TrimSuffix(strings.Join(parts[i+1:], "/"), ".jsonl")
			break
		}
	}
	project := "unknown"
	if len(rows) > 0 && str(rows[0]["type"]) == "session_meta" {
		h := obj(rows[0]["payload"])
		s.id = str(h["id"])
		s.parent = first(str(h["forked_from_id"]), str(obj(obj(obj(h["source"])["subagent"])["thread_spawn"])["parent_thread_id"]))
		s.fork = timestamp(rows[0]["timestamp"])
		project = first(str(h["cwd"]), project)
	}
	if s.id == "" {
		s.id = session
	}
	currentModel, speed := "", ""
	fallbackModel := false
	previous := codexUsage{}
	hasPrevious := false
	for _, r := range rows {
		typ := str(r["type"])
		payload := obj(r["payload"])
		if typ == "turn_context" {
			if model := codexModel(payload); model != "" {
				currentModel = model
				fallbackModel = false
			}
			continue
		}
		t := timestamp(r["timestamp"])
		var usage codexUsage
		parsedModel := ""
		eventSpeed := ""
		if typ == "event_msg" {
			if t.IsZero() {
				continue
			}
			if str(payload["type"]) == "thread_settings_applied" {
				if v, ok := obj(payload["thread_settings"])["service_tier"].(string); ok {
					speed = ""
					switch v {
					case "default", "standard":
						speed = "standard"
					case "priority", "fast":
						speed = "fast"
					}
				}
				continue
			}
			if str(payload["type"]) != "token_count" {
				continue
			}
			info := obj(payload["info"])
			totals, last := obj(info["total_token_usage"]), obj(info["last_token_usage"])
			if totals == nil && last == nil {
				continue
			}
			curr := parseCodexUsage(totals)
			if last != nil && (totals == nil || !hasPrevious || curr != previous) {
				usage = parseCodexUsage(last)
			} else if totals != nil {
				for i := range usage {
					usage[i] = max(0, curr[i]-previous[i])
				}
				usage[1] = min(usage[1], usage[0])
				usage[2] = min(usage[2], usage[0]-usage[1])
			}
			if totals != nil {
				previous = curr
				hasPrevious = true
			}
			if usage[0]+usage[1]+usage[2]+usage[3]+usage[4] == 0 {
				continue
			}
			parsedModel = first(codexModel(payload), codexModel(info))
			eventSpeed = speed
		} else {
			var u object
			parts := []object{r, obj(r["data"]), obj(r["result"]), obj(r["response"])}
			for _, part := range parts {
				if u == nil {
					u = obj(part["usage"])
				}
				parsedModel = first(parsedModel, codexModel(part))
				if t.IsZero() {
					for _, k := range []string{"timestamp", "created_at", "createdAt"} {
						if t = timestamp(part[k]); !t.IsZero() {
							break
						}
					}
				}
			}
			if u == nil {
				continue
			}
			usage = parseCodexUsage(u)
			if t.IsZero() {
				t = fileTime(p)
			}
		}
		if parsedModel != "" {
			currentModel = parsedModel
			fallbackModel = false
		}
		if currentModel == "" {
			currentModel = "gpt-5"
			fallbackModel = true
		}
		model := currentModel
		if model == "codex-auto-review" {
			model = codexAutoReview(t)
		}
		e := entry(p, "codex", session, project, model, t)
		e.InputTokens = usage[0] - usage[1] - usage[2]
		e.CacheReadInputTokens = usage[1]
		e.CacheCreationInputTokens = usage[2]
		e.OutputTokens = usage[3]
		e.ReasoningTokens = usage[4]
		e.TotalTokens = max(usage[5], usage[0]+usage[3])
		e.Speed = eventSpeed
		e.Raw["is_fallback"] = fallbackModel || currentModel == "codex-auto-review"
		e.Raw["session_uuid"] = s.id
		if s.parent != "" {
			e.IsSidechain = true
			e.Raw["parent_session_id"] = s.parent
		}
		s.entries = append(s.entries, e)
		s.usages = append(s.usages, usage)
	}
	return s, nil
}
func loadCodex(ctx context.Context, files []string, loc *time.Location) ([]types.UsageEntry, error) {
	sessions := make([]codexSession, len(files))
	var loadErrors []error
	byID := map[string]int{}
	for i, p := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var err error
		sessions[i], err = parseCodex(p)
		if err != nil {
			loadErrors = append(loadErrors, fmt.Errorf("codex source %s: %w", p, err))
		}
		if _, ok := byID[sessions[i].id]; !ok {
			byID[sessions[i].id] = i
		}
	}
	var result []types.UsageEntry
	for _, s := range sessions {
		skip := 0
		if s.parent != "" {
			if j, ok := byID[s.parent]; ok {
				parent := sessions[j]
				for skip < len(s.usages) && skip < len(parent.usages) {
					if !s.fork.IsZero() && parent.entries[skip].Timestamp.After(s.fork) {
						break
					}
					if s.usages[skip] != parent.usages[skip] {
						break
					}
					skip++
				}
			}
			if skip == 0 && len(s.entries) >= 2 {
				gap := s.entries[1].Timestamp.Sub(s.entries[0].Timestamp)
				if gap >= 0 && gap <= time.Second {
					skip = 1
					for skip < len(s.entries) {
						gap = s.entries[skip].Timestamp.Sub(s.entries[skip-1].Timestamp)
						if gap < 0 || gap > time.Second {
							break
						}
						skip++
					}
				}
			}
		}
		result = append(result, s.entries[skip:]...)
	}
	// Copied sessions with different tier metadata retain the conservative explicit tier.
	indexes := map[string]int{}
	var merged []types.UsageEntry
	for _, e := range result {
		key := signature(e, false)
		if i, ok := indexes[key]; ok {
			old := merged[i].Speed
			if old == "" || (old == "fast" && e.Speed == "standard") {
				merged[i].Speed = e.Speed
			}
			continue
		}
		indexes[key] = len(merged)
		merged = append(merged, e)
	}
	return finalize(merged, loc, false), errors.Join(loadErrors...)
}
