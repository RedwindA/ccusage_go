package sourcesb

import (
	"fmt"
	"github.com/RedwindA/ccusage_go/internal/types"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func copilotModel(x string) string {
	return strings.TrimSuffix(strings.TrimSuffix(x, "-1m-internal"), "-1m")
}

var sessionAttrs = []string{"gen_ai.conversation.id", "copilot_chat.session_id", "copilot_chat.chat_session_id", "session.id", "github.copilot.interaction_id", "gen_ai.response.id"}

func copilotSession(a obj) (string, int) {
	for i, k := range sessionAttrs {
		if v := s(a[k]); v != "" {
			rank := 3
			if i == 4 {
				rank = 2
			}
			if i == 5 {
				rank = 1
			}
			return v, rank
		}
	}
	return "", 0
}
func trace(r obj) string { return first(s(r["traceId"]), s(object(r["spanContext"])["traceId"])) }
func copilotTime(r obj, fallback time.Time) time.Time {
	for _, k := range []string{"endTime", "startTime", "hrTime", "_hrTime", "time"} {
		if a, ok := r[k].([]interface{}); ok && len(a) >= 2 {
			return time.Unix(int64(f(a[0])), int64(f(a[1])))
		}
	}
	for _, k := range []string{"timestamp", "observedTimestamp", "timeUnixNano"} {
		if v := f(r[k]); v > 0 {
			switch {
			case k == "timeUnixNano" || v >= 1e17:
				v /= 1e6
			case v >= 1e14:
				v /= 1e3
			case v < 1e11:
				v *= 1000
			}
			return time.UnixMilli(int64(v))
		}
	}
	return fallback
}
func loadCopilot(file string) ([]types.UsageEntry, error) {
	rr, err := lines(file)
	if err != nil {
		return nil, err
	}
	var out []types.UsageEntry
	isSession := strings.Contains(filepath.ToSlash(file), "/session-state/")
	for _, r := range rr {
		if s(r["type"]) == "session.shutdown" {
			isSession = true
			break
		}
	}
	if isSession {
		for _, r := range rr {
			if s(r["type"]) != "session.shutdown" {
				continue
			}
			t := ts(r["timestamp"])
			if t.IsZero() {
				continue
			}
			metrics := object(object(r["data"])["modelMetrics"])
			keys := []string{}
			for k := range metrics {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, model := range keys {
				m := object(metrics[model])
				u := object(m["usage"])
				if u == nil {
					continue
				}
				model = copilotModel(model)
				e := types.UsageEntry{Model: model, SourceFile: file, SessionID: filepath.Base(filepath.Dir(file)), Timestamp: t, OutputTokens: n(u["outputTokens"]), CacheReadInputTokens: n(u["cacheReadTokens"]), CacheCreationInputTokens: n(u["cacheWriteTokens"]), Raw: obj{"shutdown": true, "request_count": n(object(m["requests"])["count"]), "reasoning_output_tokens": n(u["reasoningTokens"])}}
				e.InputTokens = max(0, n(u["inputTokens"])-e.CacheReadInputTokens-e.CacheCreationInputTokens)
				finish(&e, 0)
				e.ID = "shutdown:" + e.SessionID + ":" + first(s(r["id"]), t.Format(time.RFC3339Nano)) + ":" + model
				if e.TotalTokens > 0 || n(e.Raw["request_count"])+n(e.Raw["reasoning_output_tokens"]) > 0 {
					out = append(out, e)
				}
			}
		}
		return out, nil
	}
	contexts := map[string]obj{}
	for _, r := range rr {
		tr := trace(r)
		if tr == "" {
			continue
		}
		a := object(r["attributes"])
		c := contexts[tr]
		if c == nil {
			c = obj{}
			contexts[tr] = c
		}
		if s(c["model"]) == "" {
			c["model"] = copilotModel(s(pick(a, "gen_ai.response.model", "gen_ai.request.model")))
		}
		sess, rank := copilotSession(a)
		if rank > n(c["rank"]) {
			c["session"] = sess
			c["rank"] = rank
		}
	}
	for i, r := range rr {
		a := object(r["attributes"])
		if a == nil {
			continue
		}
		typ, name := s(r["type"]), s(r["name"])
		span := typ == "span" || typ == "" && name != "" && (r["spanId"] != nil || r["traceId"] != nil || r["startTime"] != nil || r["endTime"] != nil || r["duration"] != nil || r["kind"] != nil)
		op := s(a["gen_ai.operation.name"])
		event := s(a["event.name"])
		body := s(pick(r, "body", "_body"))
		rank := 0
		switch {
		case span && (op == "chat" || strings.HasPrefix(name, "chat ")):
			rank = 1
		case !span && (event == "gen_ai.client.inference.operation.details" || strings.HasPrefix(body, "GenAI inference:")):
			rank = 2
		case !span && (event == "copilot_chat.agent.turn" || strings.HasPrefix(body, "copilot_chat.agent.turn")):
			rank = 3
		case span && (op == "invoke_agent" || strings.HasPrefix(name, "invoke_agent ")):
			rank = 4
		default:
			continue
		}
		tr := trace(r)
		c := contexts[tr]
		sess, _ := copilotSession(a)
		sess = first(sess, s(c["session"]), tr, "unknown-session")
		e := types.UsageEntry{SourceFile: file, Model: copilotModel(first(s(pick(a, "gen_ai.response.model", "gen_ai.request.model")), s(c["model"]), "unknown")), SessionID: sess, Timestamp: copilotTime(r, mtime(file)), InputTokens: n(a["gen_ai.usage.input_tokens"]), OutputTokens: n(a["gen_ai.usage.output_tokens"]), CacheReadInputTokens: n(a["gen_ai.usage.cache_read.input_tokens"]), CacheCreationInputTokens: n(pick(a, "gen_ai.usage.cache_write.input_tokens", "gen_ai.usage.cache_creation.input_tokens")), Raw: obj{"rank": rank, "trace": tr, "response": s(a["gen_ai.response.id"]), "request_count": 1, "reasoning_output_tokens": n(pick(a, "gen_ai.usage.reasoning.output_tokens", "gen_ai.usage.reasoning_tokens"))}}
		// Copilot counts cache reads and writes in the input tokens, as it does
		// in session-state inputTokens.
		e.InputTokens = max(0, e.InputTokens-e.CacheReadInputTokens-e.CacheCreationInputTokens)
		finish(&e, n(pick(a, "gen_ai.usage.total_tokens", "gen_ai.usage.total.token_count")))
		if e.TotalTokens == 0 {
			continue
		}
		spanID := first(s(r["spanId"]), s(object(r["spanContext"])["spanId"]))
		prefix := "span"
		if rank == 2 {
			prefix = "log"
		}
		if tr != "" && spanID != "" {
			e.ID = tr + ":" + spanID
			if rank == 2 {
				e.ID = "log:" + e.ID
			}
		} else {
			e.ID = fmt.Sprintf("%s:%s:%d:%d", prefix, sess, e.Timestamp.UnixMilli(), i)
		}
		if rank == 3 {
			e.ID = fmt.Sprintf("agent-turn:%s:%v", first(tr, sess), pick(a, "turn.index", "copilot_chat.turn.index"))
			if pick(a, "turn.index", "copilot_chat.turn.index") == nil {
				e.ID += fmt.Sprintf(":%d", i)
			}
		}
		out = append(out, e)
	}
	// Each trace/response chooses the most specific telemetry source. Indexing
	// priorities avoids quadratic work on large OpenTelemetry exports.
	traceRanks, responseRanks := map[string]int{}, map[string]int{}
	for _, e := range out {
		rank := n(e.Raw["rank"])
		for _, item := range []struct {
			id    string
			ranks map[string]int
		}{{s(e.Raw["trace"]), traceRanks}, {s(e.Raw["response"]), responseRanks}} {
			if item.id != "" {
				old := item.ranks[item.id]
				if old == 0 || rank < old {
					item.ranks[item.id] = rank
				}
			}
		}
	}
	// The Copilot CLI exports some chat spans a second time, under a new trace
	// or span ID but with the response ID and usage of the original call. VS
	// Code reports one response ID on every call of a turn, so the usage tells
	// calls apart. Only the earliest span of a call is kept.
	type call struct {
		response, model                                           string
		input, output, cacheCreation, cacheRead, reasoning, extra int
	}
	earliest, repeated := map[call]int{}, map[int]bool{}
	for i, e := range out {
		if n(e.Raw["rank"]) != 1 || s(e.Raw["response"]) == "" {
			continue
		}
		c := call{s(e.Raw["response"]), e.Model, e.InputTokens, e.OutputTokens, e.CacheCreationInputTokens, e.CacheReadInputTokens, n(e.Raw["reasoning_output_tokens"]), e.ReasoningTokens}
		kept, ok := earliest[c]
		switch {
		case !ok:
			earliest[c] = i
		case e.Timestamp.Before(out[kept].Timestamp):
			repeated[kept] = true
			earliest[c] = i
		default:
			repeated[i] = true
		}
	}
	var result []types.UsageEntry
	for i, e := range out {
		rank := n(e.Raw["rank"])
		tr, resp := s(e.Raw["trace"]), s(e.Raw["response"])
		if repeated[i] || tr != "" && traceRanks[tr] < rank || resp != "" && responseRanks[resp] < rank {
			continue
		}
		result = append(result, e)
	}

	return result, nil
}

// copilotOtelStart reports from which shutdown OTel rows replace the shutdown
// intervals of one session and model. A Copilot process restores the breakdown
// of the session's last shutdown, so the calls of a process that ended without
// one (killed, crashed or restarted by the app) are missing from every later
// shutdown, while OTel still has a row for each of them. Counted from the first
// interval with OTel rows, OTel replaces the intervals when it holds more calls
// than their request counts. Shutdowns without request counts cannot be
// compared. The comparison covers every shutdown, so it does not depend on the
// date range.
func copilotOtelStart(shutdowns []types.UsageEntry, calls []time.Time) (int, bool) {
	start, counted, otel, reported, previous := -1, 0, 0, 0, 0
	for i, e := range shutdowns {
		requests := n(e.Raw["request_count"])
		if requests == 0 {
			return 0, false
		}
		through := sort.Search(len(calls), func(j int) bool { return calls[j].After(e.Timestamp) })
		if start < 0 && through > counted {
			start = i
		}
		if start >= 0 {
			otel += through - counted
			reported += max(0, requests-previous)
		}
		counted, previous = through, requests
	}
	return start, start >= 0 && otel > reported
}
func reconcileCopilot(all []types.UsageEntry, untilExclusive time.Time) []types.UsageEntry {
	sort.SliceStable(all, func(i, j int) bool { return all[i].Timestamp.Before(all[j].Timestamp) })
	var out []types.UsageEntry
	lastIdentity := map[string]int{}
	for i, e := range all {
		if e.Raw["shutdown"] == true {
			lastIdentity[e.ID] = i
		}
	}
	shutdowns, calls := map[string][]types.UsageEntry{}, map[string][]time.Time{}
	for i, e := range all {
		key := e.SessionID + ":" + e.Model
		if e.Raw["shutdown"] != true {
			calls[key] = append(calls[key], e.Timestamp)
		} else if lastIdentity[e.ID] == i {
			shutdowns[key] = append(shutdowns[key], e)
		}
	}
	// Per session and model: how many leading shutdowns are reported as
	// intervals, and the shutdown after which OTel rows are reported (every
	// row without one).
	kept, otelAfter := map[string]int{}, map[string]time.Time{}
	for key, list := range shutdowns {
		visible := sort.Search(len(list), func(i int) bool {
			return !untilExclusive.IsZero() && !list[i].Timestamp.Before(untilExclusive)
		})
		after := visible
		if start, ok := copilotOtelStart(list, calls[key]); ok {
			visible, after = min(visible, start), start
		}
		kept[key] = visible
		if after > 0 {
			otelAfter[key] = list[after-1].Timestamp
		}
	}
	latest, positions := map[string]types.UsageEntry{}, map[string]int{}
	for i, e := range all {
		if e.Raw["shutdown"] != true || lastIdentity[e.ID] != i {
			continue
		}
		key := e.SessionID + ":" + e.Model
		positions[key]++
		if positions[key] > kept[key] {
			continue
		}
		prev, ok := latest[key]
		latest[key] = e
		if ok {
			e.InputTokens = max(0, e.InputTokens-prev.InputTokens)
			e.OutputTokens = max(0, e.OutputTokens-prev.OutputTokens)
			e.CacheReadInputTokens = max(0, e.CacheReadInputTokens-prev.CacheReadInputTokens)
			e.CacheCreationInputTokens = max(0, e.CacheCreationInputTokens-prev.CacheCreationInputTokens)
			raw := obj{}
			for k, v := range e.Raw {
				raw[k] = v
			}
			e.Raw = raw
			e.Raw["request_count"] = max(0, n(e.Raw["request_count"])-n(prev.Raw["request_count"]))
			e.Raw["reasoning_output_tokens"] = max(0, n(e.Raw["reasoning_output_tokens"])-n(prev.Raw["reasoning_output_tokens"]))
			finish(&e, 0)
		}
		if e.TotalTokens > 0 || n(e.Raw["request_count"])+n(e.Raw["reasoning_output_tokens"]) > 0 {
			out = append(out, e)
		}
	}
	for _, e := range all {
		if e.Raw["shutdown"] == true {
			continue
		}
		after, ok := otelAfter[e.SessionID+":"+e.Model]
		if !ok || e.Timestamp.After(after) {
			out = append(out, e)
		}
	}
	return out
}
