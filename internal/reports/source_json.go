package reports

import (
	"sort"
	"strings"
	"time"
)

// FocusedJSONRows preserves each agent's public report schema while using the
// shared token and cost aggregation. Codex exposes a model map and costUSD.
func FocusedJSONRows(rows []*Row, o Options) []map[string]any {
	if o.Agent == "" {
		return JSONRows(rows, o)
	}
	if o.Kind == "model" || o.Kind == "workspace" {
		return focusedDimensions(rows, o)
	}
	base := JSONRows(rows, o)
	for i, r := range rows {
		m := base[i]
		for _, key := range []string{"workspace", "sessionName", "reasoningOutputTokens"} {
			delete(m, key)
		}
		if o.Agent != "hermes" && o.Agent != "copilot" {
			delete(m, "messageCount")
		}
		if o.Agent == "copilot" && r.Requests == 0 {
			delete(m, "messageCount")
		}
		includeMetadata := o.Kind == "session" && (o.Agent == "claude" || o.Agent == "copilot" || o.Agent == "zcode" || o.Agent == "antigravity" || o.Agent == "pi" || o.Agent == "openclaw" || o.Agent == "qwen" || o.Agent == "grok")
		if !includeMetadata {
			for _, key := range []string{"firstActivity", "lastActivity", "projectPath"} {
				delete(m, key)
			}
		} else {
			m["firstActivity"] = jsonTimestamp(r.FirstActivity)
			m["lastActivity"] = jsonTimestamp(r.LastActivity)
			m["projectPath"] = nullableString(r.ProjectPath)
		}
		if o.Agent == "codex" {
			models := map[string]any{}
			for _, model := range r.Breakdowns {
				item := map[string]any{"inputTokens": model.Input, "cacheCreationTokens": model.CacheCreate, "cacheReadTokens": model.CacheRead, "outputTokens": model.Output, "reasoningOutputTokens": model.Reasoning, "totalTokens": model.Total, "isFallback": model.IsFallback}
				if model.MissingPricing && !o.NoCost {
					item["missingPricing"] = true
				}
				models[model.Name] = item
			}
			m = map[string]any{map[string]string{"daily": "date", "weekly": "week", "monthly": "month", "session": "sessionId"}[o.Kind]: r.Period, "inputTokens": r.Input, "cacheCreationTokens": r.CacheCreate, "cacheReadTokens": r.CacheRead, "outputTokens": r.Output, "reasoningOutputTokens": r.Reasoning, "totalTokens": r.Total, "costUSD": r.Cost, "models": models}
			if o.Kind == "session" {
				separator := strings.LastIndex(r.Period, "/")
				directory, file := "", r.Period
				if separator >= 0 {
					directory, file = r.Period[:separator], r.Period[separator+1:]
				}
				m["sessionFile"] = file
				m["directory"] = directory
				m["lastActivity"] = jsonTimestamp(r.LastActivity)
			}
			if r.User != "" {
				m["user"] = r.User
			}
			base[i] = m
		} else {
			breakdowns := make([]map[string]any, 0, len(r.Breakdowns))
			for _, model := range r.Breakdowns {
				breakdowns = append(breakdowns, focusedBreakdown(model))
			}
			m["modelBreakdowns"] = breakdowns
		}
		if o.NoCost {
			StripCosts(m)
		}
	}
	return base
}
func focusedBreakdown(m Model) map[string]any {
	v := map[string]any{"modelName": m.Name, "inputTokens": m.Input, "outputTokens": m.Output, "cacheCreationTokens": m.CacheCreate, "cacheReadTokens": m.CacheRead, "cost": m.Cost}
	if m.MissingPricing {
		v["missingPricing"] = true
	}
	return v
}
func jsonTimestamp(value string) any {
	if value == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return value
}
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func focusedDimensions(rows []*Row, o Options) []map[string]any {
	base := JSONRows(rows, o)
	for i, r := range rows {
		m := base[i]
		delete(m, "messageCount")
		delete(m, "reasoningOutputTokens")
		if o.Kind == "workspace" {
			breakdowns := make([]map[string]any, 0, len(r.Breakdowns))
			for _, model := range r.Breakdowns {
				v := focusedBreakdown(model)
				if o.Agent == "droid" {
					v["attribution"] = "sessionSnapshot"
				}
				breakdowns = append(breakdowns, v)
			}
			m["modelBreakdowns"] = breakdowns
		}
		if o.NoCost {
			StripCosts(m)
		}
	}
	return base
}

func FocusedTotals(rows []*Row, o Options) map[string]any {
	totals := Totals(rows)
	delete(totals, "requestCount")
	delete(totals, "messageCount")
	if o.Agent == "codex" && o.Kind != "model" && o.Kind != "workspace" {
		totals["costUSD"] = totals["totalCost"]
		delete(totals, "totalCost")
		reasoning := 0
		for _, row := range rows {
			reasoning += row.Reasoning
		}
		totals["reasoningOutputTokens"] = reasoning
	}
	missing := map[string]bool{}
	for _, row := range rows {
		for _, model := range row.Breakdowns {
			if model.MissingPricing {
				missing[model.Name] = true
			}
		}
	}
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for name := range missing {
			names = append(names, name)
		}
		sort.Strings(names)
		totals["unpricedModels"] = names
	}
	if o.NoCost {
		StripCosts(totals)
	}
	return totals
}
