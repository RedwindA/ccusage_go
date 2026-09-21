package loader

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RedwindA/ccusage_go/internal/types"
)

// deduplicateUsage delays selection until every candidate has been parsed.
// Streaming snapshots can grow, and sidechain replay can change request ids.
func deduplicateUsage(entries []types.UsageEntry) []types.UsageEntry {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Timestamp.Equal(entries[j].Timestamp) {
			return entries[i].SourceFile < entries[j].SourceFile
		}
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})
	out := make([]types.UsageEntry, 0, len(entries))
	exact := map[string]int{}
	aliases := map[string][]int{}
	key := func(parts ...string) string { b, _ := json.Marshal(parts); return string(b) }
	for _, e := range entries {
		message, _ := e.Raw["message_id"].(string)
		request, _ := e.Raw["request_id"].(string)
		if message == "" {
			out = append(out, e)
			continue
		}
		exactKey := key(message, request)
		if request == "" {
			exactKey = key(message, "", e.SessionID)
		}
		sideKey := key(message, e.SessionID)
		i, found := exact[exactKey]
		if !found {
			for _, candidate := range aliases[sideKey] {
				old := out[candidate]
				if e.Timestamp.Equal(old.Timestamp) && (e.IsSidechain || old.IsSidechain) {
					i, found = candidate, true
					break
				}
			}
		}
		if !found {
			i = len(out)
			out = append(out, e)
		} else {
			old := out[i]
			replace := false
			if e.IsSidechain != old.IsSidechain {
				replace = old.IsSidechain
			} else if e.TotalTokens != old.TotalTokens {
				replace = e.TotalTokens > old.TotalTokens
			} else {
				replace = e.Speed != "" && old.Speed == ""
			}
			if replace {
				out[i] = e
			}
		}
		exact[exactKey] = i
		exists := false
		for _, v := range aliases[sideKey] {
			exists = exists || v == i
		}
		if !exists {
			aliases[sideKey] = append(aliases[sideKey], i)
		}
	}
	return out
}
func fileSessionID(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, p := range parts {
		if p == "projects" && i+2 < len(parts) {
			s := parts[i+2]
			if s == "subagents" && i+3 < len(parts) {
				s = parts[i+3]
			}
			return strings.TrimSuffix(s, ".jsonl")
		}
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}
func cloneObject(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// advisorRecords extracts independently billed advisor calls without duplicating
// the ordinary message iterations already represented by parent usage.
func advisorRecords(raw map[string]interface{}) []map[string]interface{} {
	message, _ := raw["message"].(map[string]interface{})
	usage, _ := message["usage"].(map[string]interface{})
	iterations, _ := usage["iterations"].([]interface{})
	var out []map[string]interface{}
	for _, v := range iterations {
		iteration, _ := v.(map[string]interface{})
		model, _ := iteration["model"].(string)
		if iteration["type"] != "advisor_message" || model == "" {
			continue
		}
		child := cloneObject(raw)
		msg := cloneObject(message)
		child["message"] = msg
		delete(child, "cost")
		delete(child, "costUSD")
		msg["model"] = model
		msg["usage"] = iteration
		if id, ok := message["id"].(string); ok {
			msg["id"] = id + ":advisor:" + itoa(len(out))
		}
		out = append(out, child)
	}
	return out
}
func itoa(v int) string { b, _ := json.Marshal(v); return string(b) }
