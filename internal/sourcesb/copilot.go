package sourcesb

import (
	"fmt"
	"github.com/RedwindA/ccusage_go/internal/types"
	"math"
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

const (
	copilotShutdownEvent   = "session.shutdown"
	copilotCheckpointEvent = "session.usage_checkpoint"
)

// copilotNanoAIU reads a totalNanoAiu credit count. One AIU is one GitHub AI
// credit, billed at $0.01.
func copilotNanoAIU(v interface{}) (int, bool) {
	x, ok := v.(float64)
	return int(math.Round(x)), ok && x >= 0
}

// copilotSnapshotID keys a credit snapshot by its event ID, or by its time and
// total when the event has none.
func copilotSnapshotID(e types.UsageEntry, event string) types.UsageEntry {
	if event == "" {
		event = fmt.Sprintf("%v:%d:%v", e.Raw["credit_snapshot"], e.Timestamp.UnixMilli(), e.Raw["nano_aiu"])
	}
	e.ID = "credits:" + e.SessionID + ":" + event
	return e
}

func loadCopilot(file string) ([]types.UsageEntry, error) {
	rr, err := lines(file)
	if err != nil {
		return nil, err
	}
	var out []types.UsageEntry
	isSession := strings.Contains(filepath.ToSlash(file), "/session-state/")
	for _, r := range rr {
		if s(r["type"]) == copilotShutdownEvent {
			isSession = true
			break
		}
	}
	if isSession {
		session := filepath.Base(filepath.Dir(file))
		for _, r := range rr {
			typ := s(r["type"])
			if typ != copilotShutdownEvent && typ != copilotCheckpointEvent {
				continue
			}
			t := ts(r["timestamp"])
			if t.IsZero() {
				continue
			}
			data := object(r["data"])
			// Session-wide credits for copilotCreditGaps; nano_aiu stays unset
			// when they cannot be reconciled.
			snapshot := types.UsageEntry{SourceFile: file, SessionID: session, Timestamp: t, Raw: obj{"credit_snapshot": typ}}
			total, hasTotal := copilotNanoAIU(data["totalNanoAiu"])
			if typ == copilotCheckpointEvent {
				// A checkpoint without a total carries nothing to reconcile.
				if hasTotal {
					snapshot.Raw["nano_aiu"] = total
					out = append(out, copilotSnapshotID(snapshot, s(r["id"])))
				}
				continue
			}
			metrics := object(data["modelMetrics"])
			keys := []string{}
			for k := range metrics {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			modelsReportCredits := true
			for _, model := range keys {
				m := object(metrics[model])
				u := object(m["usage"])
				if u == nil {
					continue
				}
				model = copilotModel(model)
				e := types.UsageEntry{Model: model, SourceFile: file, SessionID: session, Timestamp: t, OutputTokens: n(u["outputTokens"]), CacheReadInputTokens: n(u["cacheReadTokens"]), CacheCreationInputTokens: n(u["cacheWriteTokens"]), Raw: obj{"shutdown": true, "request_count": n(object(m["requests"])["count"]), "reasoning_output_tokens": n(u["reasoningTokens"])}}
				e.InputTokens = max(0, n(u["inputTokens"])-e.CacheReadInputTokens-e.CacheCreationInputTokens)
				finish(&e, 0)
				e.ID = "shutdown:" + e.SessionID + ":" + first(s(r["id"]), t.Format(time.RFC3339Nano)) + ":" + model
				if e.TotalTokens > 0 || n(e.Raw["request_count"])+n(e.Raw["reasoning_output_tokens"]) > 0 {
					credits, ok := copilotNanoAIU(m["totalNanoAiu"])
					modelsReportCredits = modelsReportCredits && ok
					e.Raw["nano_aiu"] = credits
					out = append(out, e)
				}
			}
			if hasTotal && modelsReportCredits {
				snapshot.Raw["nano_aiu"] = total
			}
			out = append(out, copilotSnapshotID(snapshot, s(r["id"])))
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
	var snapshots []types.UsageEntry
	usage := all[:0]
	for _, e := range all {
		if e.Raw["credit_snapshot"] != nil {
			snapshots = append(snapshots, e)
		} else {
			usage = append(usage, e)
		}
	}
	all = usage
	sort.SliceStable(all, func(i, j int) bool { return all[i].Timestamp.Before(all[j].Timestamp) })
	var out []types.UsageEntry
	lastIdentity := map[string]int{}
	for i, e := range all {
		if e.Raw["shutdown"] == true {
			lastIdentity[e.ID] = i
		}
	}
	shutdowns, calls, otelSessions := map[string][]types.UsageEntry{}, map[string][]time.Time{}, map[string]bool{}
	for i, e := range all {
		key := e.SessionID + ":" + e.Model
		if e.Raw["shutdown"] != true {
			calls[key] = append(calls[key], e.Timestamp)
			otelSessions[e.SessionID] = true
		} else if lastIdentity[e.ID] == i {
			shutdowns[key] = append(shutdowns[key], e)
		}
	}
	// Per session and model: how many leading shutdowns are reported as
	// intervals, and the shutdown after which OTel rows are reported (every
	// row without one). Credits are attributed past --until too, so a
	// breakdown that a later shutdown catches up on is judged the same way in
	// every date range.
	kept, otelAfter, attributed := map[string]int{}, map[string]time.Time{}, map[copilotShutdownTime]int{}
	for key, list := range shutdowns {
		previous := 0
		for _, e := range list {
			credits := n(e.Raw["nano_aiu"])
			attributed[copilotShutdownTime{e.SessionID, e.Timestamp.UnixMilli()}] += max(0, credits-previous)
			previous = credits
		}
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
	// OTel rows record every model call, so sessions that have them are left
	// to that source rather than to the coarser credit totals.
	return append(out, copilotCreditGaps(snapshots, attributed, otelSessions)...)
}

type copilotShutdownTime struct {
	session string
	millis  int64
}

// copilotCreditGaps reports the credits of the session-wide totalNanoAiu that
// no modelMetrics entry accounts for, as cost-only entries of the unknown
// model.
//
// modelMetrics only covers the running Copilot process: a resumed session, or
// one the client restarted, reports an empty or partial breakdown, while
// totalNanoAiu keeps the whole session. At every shutdown, and at the
// checkpoints after the last one (sessions still open or never shut down
// cleanly), the unexplained amount is the session total minus the per-model
// credits reported so far. A shutdown can carry a breakdown that a later one
// catches up on, so only what stays unexplained at every later snapshot is
// reported, dated at the first snapshot from which it stays. Checkpoints
// before a shutdown are already part of that shutdown's total.
//
// Only sessions with checkpoints qualify: Copilot versions that predate them
// restart both totals on every resume, so the cumulative reading used for
// modelMetrics does not hold there. Without a credit total on every shutdown,
// the usage already reported cannot be told apart from what is missing.
func copilotCreditGaps(snapshots []types.UsageEntry, attributed map[copilotShutdownTime]int, otelSessions map[string]bool) []types.UsageEntry {
	seen, sessions := map[string]bool{}, map[string][]types.UsageEntry{}
	for _, e := range snapshots {
		if !seen[e.ID] {
			seen[e.ID] = true
			sessions[e.SessionID] = append(sessions[e.SessionID], e)
		}
	}
	ids := []string{}
	for id := range sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	shutdown := func(e types.UsageEntry) bool { return e.Raw["credit_snapshot"] == copilotShutdownEvent }
	var gaps []types.UsageEntry
	for _, id := range ids {
		list := sessions[id]
		checkpoints, incomplete, last := false, false, -1
		sort.SliceStable(list, func(i, j int) bool {
			if !list[i].Timestamp.Equal(list[j].Timestamp) {
				return list[i].Timestamp.Before(list[j].Timestamp)
			}
			return !shutdown(list[i]) && shutdown(list[j])
		})
		for i, e := range list {
			if shutdown(e) {
				incomplete = incomplete || e.Raw["nano_aiu"] == nil
				last = i
			} else {
				checkpoints = true
			}
		}
		if otelSessions[id] || !checkpoints || incomplete {
			continue
		}
		var points []types.UsageEntry
		var unexplained []int
		total, explained := 0, 0
		for i, e := range list {
			if !shutdown(e) && i < last {
				continue
			}
			total = max(total, n(e.Raw["nano_aiu"]))
			if shutdown(e) {
				explained += attributed[copilotShutdownTime{id, e.Timestamp.UnixMilli()}]
			}
			points = append(points, e)
			unexplained = append(unexplained, max(0, total-explained))
		}
		// Keep what stays unexplained at every later point, which never
		// decreases.
		for i := len(unexplained) - 2; i >= 0; i-- {
			unexplained[i] = min(unexplained[i], unexplained[i+1])
		}
		reported := 0
		for i, e := range points {
			if gap := unexplained[i] - reported; gap > 0 {
				// One AIU is one GitHub AI credit, billed at $0.01.
				gaps = append(gaps, types.UsageEntry{ID: e.ID, SourceFile: e.SourceFile, SessionID: id, Timestamp: e.Timestamp, Model: "unknown", Cost: float64(gap) / 1e11, HasCost: true, Raw: obj{"request_count": 0}})
			}
			reported = unexplained[i]
		}
	}
	return gaps
}
