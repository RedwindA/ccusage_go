package loader

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/RedwindA/ccusage_go/internal/types"
)

// deduplicateUsage delays selection until every candidate has been parsed.
// Streaming snapshots can grow, and sidechain replay can change request ids.
// Callers pass each file's entries in line order, and one pass over them in
// file path order matches upstream: ties keep the first candidate and a
// replay's match depends on earlier entries, so deduping each file first or
// going by timestamp picks other survivors.
func deduplicateUsage(entries []types.UsageEntry) []types.UsageEntry {
	// Sort indexes rather than the (large) entries themselves.
	order := make([]int, len(entries))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return entries[order[i]].SourceFile < entries[order[j]].SourceFile
	})
	out := make([]types.UsageEntry, 0, len(entries))
	exact := map[string]int{}
	// Sidechain replays are routed by message and session. A replay needs a
	// sidechain on at least one side, so parent candidates only look through
	// sidechain entries; gateways that reuse one message ID for every
	// response then do not rescan every earlier entry.
	type routeEntry struct {
		index int
		// session is the alias a cross-session match adds; empty means the
		// session of the entry now in the slot.
		session string
	}
	replays := map[string][]routeEntry{}
	sidechains := map[string][]routeEntry{}
	push := func(route map[string][]routeEntry, k string, r routeEntry) {
		if !slices.Contains(route[k], r) {
			route[k] = append(route[k], r)
		}
	}
	key := dedupeKey
	for _, k := range order {
		e := entries[k]
		message, _ := e.Raw["message_id"].(string)
		request, _ := e.Raw["request_id"].(string)
		if message == "" {
			out = append(out, e)
			continue
		}
		exactKey := exactDedupeKey(&e)
		sideKey := key(message, e.SessionID)
		i, found := exact[exactKey]
		// A replaced survivor stays indexed under its old keys, so only
		// match the entry now in that slot.
		if found && exactDedupeKey(&out[i]) != exactKey {
			found = false
		}
		if !found {
			route := sidechains
			if e.IsSidechain {
				route = replays
			}
			for _, r := range route[sideKey] {
				old := out[r.index]
				session := r.session
				if session == "" {
					session = old.SessionID
				}
				oldRequest, _ := old.Raw["request_id"].(string)
				// Requestless replays match across timestamps; replays with
				// a new request ID need the parent's timestamp.
				requestless := request == "" && oldRequest == ""
				if session == e.SessionID && (requestless || e.Timestamp.Equal(old.Timestamp)) && (e.IsSidechain || old.IsSidechain) {
					i, found = r.index, true
					break
				}
			}
		}
		if !found {
			i = len(out)
			out = append(out, e)
			exact[exactKey] = i
			replays[sideKey] = append(replays[sideKey], routeEntry{index: i})
			if e.IsSidechain {
				sidechains[sideKey] = append(sidechains[sideKey], routeEntry{index: i})
			}
		} else {
			old := out[i]
			if e.SessionID != old.SessionID {
				// Cross-session copies can become the survivor, so keep
				// both sessions' routes for later sidechain replays.
				for _, session := range []string{e.SessionID, old.SessionID} {
					for _, route := range []map[string][]routeEntry{replays, sidechains} {
						push(route, key(message, session), routeEntry{i, session})
					}
				}
			}
			replace := false
			if e.IsSidechain != old.IsSidechain {
				replace = old.IsSidechain
			} else if e.TotalTokens != old.TotalTokens {
				replace = e.TotalTokens > old.TotalTokens
			} else {
				replace = e.Speed != "" && old.Speed == ""
			}
			// Register the key only when this entry survives: a requestless
			// replay's own timestamp would otherwise pull the next response
			// at that instant into the parent it matched.
			if replace {
				out[i] = e
				exact[exactKey] = i
				// Route the new survivor under its own session and
				// sidechain flag, as if it were a new entry.
				if e.SessionID != old.SessionID || e.IsSidechain != old.IsSidechain {
					push(replays, sideKey, routeEntry{index: i})
					if e.IsSidechain {
						push(sidechains, sideKey, routeEntry{index: i})
					}
				}
			}
		}
	}
	return out
}

// exactDedupeKey identifies one response by its message and request IDs,
// or by requestlessKey when it has no request ID.
func exactDedupeKey(e *types.UsageEntry) string {
	message, _ := e.Raw["message_id"].(string)
	request, _ := e.Raw["request_id"].(string)
	if request == "" {
		return requestlessKey(message, e.SessionID, e.Timestamp)
	}
	return dedupeKey(message, request)
}

// requestlessKey scopes an entry without a request ID to its session and
// timestamp: gateways can reuse one message ID for every response, while
// repeated writes of one response share a timestamp. Anthropic (msg_) and
// OpenAI Responses (resp_) IDs are unique per response, and Claude Code
// writes one line per content block at distinct timestamps, so those
// collapse across timestamps within the session.
func requestlessKey(message, session string, timestamp time.Time) string {
	if strings.HasPrefix(message, "msg_") || strings.HasPrefix(message, "resp_") {
		return dedupeKey(message, "", session)
	}
	return dedupeKey(message, "", session, strconv.FormatInt(timestamp.UnixNano(), 10))
}

// dedupeKey joins parts into a map key. Keys were once json.Marshal(parts),
// which turns every invalid UTF-8 byte into the same \ufffd escape; marking
// those bytes with 0xFF (never valid UTF-8) behind length prefixes keeps
// exactly the same collisions without the reflection.
func dedupeKey(parts ...string) string {
	n := 0
	for _, p := range parts {
		n += len(p) + 4
	}
	b := make([]byte, 0, n)
	for _, p := range parts {
		b = strconv.AppendInt(b, int64(len(p)), 10)
		b = append(b, ':')
		if utf8.ValidString(p) {
			b = append(b, p...)
			continue
		}
		for i := 0; i < len(p); {
			r, size := utf8.DecodeRuneInString(p[i:])
			if r == utf8.RuneError && size == 1 {
				b = append(b, 0xff)
			} else {
				b = append(b, p[i:i+size]...)
			}
			i += size
		}
	}
	return string(b)
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
