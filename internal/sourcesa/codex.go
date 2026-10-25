package sourcesa

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/RedwindA/ccusage_go/internal/fileread"
	"github.com/RedwindA/ccusage_go/internal/jsonscan"
	"github.com/RedwindA/ccusage_go/internal/types"
	"github.com/klauspost/compress/zstd"
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
	entries []types.UsageEntry
	usages  []codexUsage
	// compactions are kept apart from entries so they never align with a
	// parent's replayed usage. compacted dates every compaction request the
	// session paired, including those its cumulative counts already covered.
	compactions []types.UsageEntry
	compacted   map[string]time.Time
	id, parent  string
	fork        time.Time
	// burst is the first token count when the session opens with two
	// written back to back, as Codex writes a fork's replayed history.
	burst time.Time
}

// inCodexBurst reports whether usage at t continues a burst last seen at
// prev. Codex writes a replayed history within milliseconds, while the
// child's own first turn follows a pause of seconds.
func inCodexBurst(prev, t time.Time) bool {
	gap := t.Sub(prev)
	return gap >= 0 && gap <= time.Second
}

// codexRequest pairs a compaction request's token_usage_record with the
// compacted line naming it; Codex writes the two in either order.
type codexRequest struct {
	entry                        types.UsageEntry
	usage                        codexUsage
	thread                       object
	recorded, compacted, counted bool
}

// codexRowKeys are the top-level row fields parseCodex reads, except
// payload: that is decoded only for the row types that read it, because on
// response items it holds the transcript text and dominates the file.
var codexRowKeys = map[string]bool{
	"type": true, "timestamp": true, "created_at": true, "createdAt": true, "usage": true,
	"model": true, "model_name": true, "metadata": true, "data": true, "result": true, "response": true,
}

// codexPayloadKeys are the payload fields parseCodex reads: the session
// header's identity, model fields, event details and compaction usage.
// Session headers carry the full instructions, messages carry their text and
// compacted lines the replacement history; all are skipped.
var codexPayloadKeys = map[string]bool{
	"id": true, "forked_from_id": true, "source": true, "cwd": true,
	"model": true, "model_name": true, "metadata": true,
	"type": true, "thread_settings": true, "info": true,
	"response_id": true, "usage": true, "thread_token_usage": true, "compaction_response_id": true,
}

func decodeCodexPayload(payload []byte) (interface{}, error) {
	if payload[0] != '{' {
		return jsonscan.Decode(payload)
	}
	fields := object{}
	_, err := jsonscan.FieldsStrict(payload, func(key []byte, value jsonscan.Value) error {
		if !codexPayloadKeys[string(key)] {
			return nil
		}
		v, err := value.Decode()
		fields[string(key)] = v
		return err
	})
	return fields, err
}

// codexZstd decodes compressed rollouts; DecodeAll is safe for concurrent use.
// DecodeAll first allocates the content size a frame header claims, so that
// is capped at the largest window a stream may need, and a rollout claiming
// more is streamed. codexDecoded reuses the decoded buffers, as fileread
// reuses file buffers.
var (
	codexZstd, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(0), zstd.WithDecoderMaxMemory(zstd.MaxWindowSize))
	codexDecoded = sync.Pool{New: func() any { return new([]byte) }}
)

// codexPlainPath is the .jsonl path of a rollout whatever its encoding.
// Codex migrates cold rollouts to .jsonl.zst without changing the session.
func codexPlainPath(p string) string {
	if strings.HasSuffix(p, ".jsonl.zst") {
		return strings.TrimSuffix(p, ".zst")
	}
	return p
}

// readCodexRollout reads a plain or compressed rollout and returns the path it
// read: Codex may compress or restore the rollout between discovery and this
// read, which then falls back to the other encoding.
func readCodexRollout(path string) ([]byte, func(), string, error) {
	b, release, err := readCodexEncoding(path)
	if errors.Is(err, fs.ErrNotExist) {
		other := codexPlainPath(path)
		if other == path {
			other += ".zst"
		}
		if b, release, err := readCodexEncoding(other); err == nil {
			return b, release, other, nil
		}
	}
	return b, release, path, err
}

func readCodexEncoding(path string) ([]byte, func(), error) {
	b, release, err := fileread.Read(path)
	if err != nil || codexPlainPath(path) == path {
		return b, release, err
	}
	defer release()
	bp := codexDecoded.Get().(*[]byte)
	// A damaged frame keeps the usage decoded before it: the line it cuts is
	// invalid JSON and skipped, like a torn plain tail.
	*bp, err = codexZstd.DecodeAll(b, (*bp)[:0])
	if errors.Is(err, zstd.ErrDecoderSizeExceeded) {
		d, _ := zstd.NewReader(bytes.NewReader(b), zstd.WithDecoderConcurrency(1))
		defer d.Close()
		out := bytes.NewBuffer((*bp)[:0])
		_, _ = d.WriteTo(out)
		*bp = out.Bytes()
	}
	return *bp, func() { codexDecoded.Put(bp) }, nil
}

// readCodexRows is readLines restricted to the fields parseCodex uses. It also
// returns the path readCodexRollout read.
func readCodexRows(path string) ([]object, string, error) {
	b, release, opened, err := readCodexRollout(path)
	if err != nil {
		return nil, "", err
	}
	defer release()
	var rows []object
	for len(b) > 0 {
		line := b
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line, b = b[:i], b[i+1:]
		} else {
			b = nil
		}
		row := object{}
		var payload []byte
		null, err := jsonscan.FieldsStrict(line, func(key []byte, value jsonscan.Value) error {
			if string(key) == "payload" {
				var err error
				payload, err = value.Raw()
				return err
			}
			if !codexRowKeys[string(key)] {
				return nil
			}
			v, err := value.Decode()
			row[string(key)] = v
			return err
		})
		if err != nil || null {
			continue
		}
		if payload != nil {
			switch str(row["type"]) {
			case "session_meta", "turn_context", "event_msg", "token_usage_record", "compacted":
				if row["payload"], err = decodeCodexPayload(payload); err != nil {
					continue
				}
			}
		}
		rows = append(rows, row)
	}
	return rows, opened, nil
}

func parseCodex(p string) (codexSession, error) {
	rows, opened, err := readCodexRows(p)
	s := codexSession{}
	if err != nil {
		return s, err
	}
	plain := codexPlainPath(p)
	session := stem(plain)
	parts := strings.Split(filepath.ToSlash(plain), "/")
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
	newEntry := func(model string, fallback bool, t time.Time, usage codexUsage, speed string) types.UsageEntry {
		e := entry(p, "codex", session, project, model, t)
		if model == "codex-auto-review" {
			e.Model, fallback = codexAutoReview(t), true
		}
		e.InputTokens = usage[0] - usage[1] - usage[2]
		e.CacheReadInputTokens = usage[1]
		e.CacheCreationInputTokens = usage[2]
		e.OutputTokens = usage[3]
		e.ReasoningTokens = usage[4]
		e.TotalTokens = max(usage[5], usage[0]+usage[3])
		e.Speed = speed
		e.Raw["is_fallback"] = fallback
		e.Raw["session_uuid"] = s.id
		if s.parent != "" {
			e.IsSidechain = true
			e.Raw["parent_session_id"] = s.parent
		}
		return e
	}
	requests := map[string]*codexRequest{}
	request := func(id string) *codexRequest {
		if requests[id] == nil {
			requests[id] = &codexRequest{}
		}
		return requests[id]
	}
	latest := "" // the last valid usage record's response ID
	currentModel, speed := "", ""
	fallbackModel := false
	previous := codexUsage{}
	hasPrevious := false
	var heads []time.Time // the first two token counts
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
		// A token_usage_record carries one request's usage apart from the
		// cumulative token counts, which omit remote compactions. It counts
		// only when a compacted line names its response.
		switch typ {
		case "compacted":
			id := str(payload["compaction_response_id"])
			if id == "" {
				continue
			}
			req := request(id)
			req.compacted = true
			if req.recorded && !req.counted {
				req.counted = true
				s.compactions = append(s.compactions, req.entry)
			}
			continue
		case "token_usage_record":
			id := str(payload["response_id"])
			usage := parseCodexUsage(obj(payload["usage"]))
			if id == "" || t.IsZero() || usage == (codexUsage{}) || requests[id] != nil && requests[id].counted {
				continue
			}
			req := request(id)
			if !req.recorded {
				// The request's own model does not change the turn's.
				parsed := codexModel(payload)
				req.entry = newEntry(first(parsed, currentModel, "gpt-5"), parsed == "", t, usage, speed)
				req.entry.Raw["response_id"] = id
				req.usage, req.thread, req.recorded = usage, obj(payload["thread_token_usage"]), true
			}
			latest = id
			if req.compacted {
				req.counted = true
				s.compactions = append(s.compactions, req.entry)
			}
			continue
		}
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
					case "flex":
						speed = "flex"
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
			// A repeated snapshot adds no usage but still belongs to a burst.
			if len(heads) < 2 {
				heads = append(heads, t)
			}
			curr := parseCodexUsage(totals)
			advanced := totals == nil || !hasPrevious || curr != previous
			if last != nil && advanced {
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
			// Local compaction advances the cumulative counts between the
			// usage record and its marker, so they already include it; remote
			// compaction does not. Matching only the latest record keeps an
			// unrelated request with the same counts from consuming it.
			if req := requests[latest]; advanced && req != nil && !req.counted && (req.usage == usage || req.thread != nil && totals != nil && parseCodexUsage(req.thread) == curr) {
				req.counted = true
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
				t = fileTime(opened)
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
		s.entries = append(s.entries, newEntry(currentModel, fallbackModel, t, usage, eventSpeed))
		s.usages = append(s.usages, usage)
	}
	if len(heads) == 2 && inCodexBurst(heads[0], heads[1]) {
		s.burst = heads[0]
	}
	for id, req := range requests {
		if req.recorded && req.compacted {
			if s.compacted == nil {
				s.compacted = map[string]time.Time{}
			}
			s.compacted[id] = req.entry.Timestamp
		}
	}
	return s, nil
}
func loadCodex(ctx context.Context, files []string, loc *time.Location) ([]types.UsageEntry, error) {
	sessions := make([]codexSession, len(files))
	var loadErrors []error
	byID := map[string]int{}
	parseErrors := make([]error, len(files))
	if err := parallelEach(ctx, files, func(i int) {
		sessions[i], parseErrors[i] = parseCodex(files[i])
	}); err != nil {
		return nil, err
	}
	for i, p := range files {
		if err := parseErrors[i]; err != nil {
			loadErrors = append(loadErrors, fmt.Errorf("codex source %s: %w", p, err))
		}
		if _, ok := byID[sessions[i].id]; !ok {
			byID[sessions[i].id] = i
		}
	}
	var result []types.UsageEntry
	for _, s := range sessions {
		skip := 0
		var replayed map[string]time.Time
		if s.parent != "" {
			if j, ok := byID[s.parent]; ok {
				parent := sessions[j]
				replayed = parent.compacted
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
			if skip == 0 && !s.burst.IsZero() {
				for at := s.burst; skip < len(s.entries) && inCodexBurst(at, s.entries[skip].Timestamp); skip++ {
					at = s.entries[skip].Timestamp
				}
			}
		}
		result = append(result, s.entries[skip:]...)
		for _, e := range s.compactions {
			// The parent's compactions up to the fork were replayed, even
			// when the parent's cumulative counts covered them.
			if at, ok := replayed[e.Raw["response_id"].(string)]; ok && (s.fork.IsZero() || !at.After(s.fork)) {
				continue
			}
			result = append(result, e)
		}
	}
	// Copied sessions keep an explicit tier over an unclassified copy, and
	// conflicting tiers resolve to Standard, then Fast, then Flex, whatever
	// the file order. Copies of a compaction request share its response ID,
	// and the first copy keeps its session, time and model.
	tierRank := map[string]int{"flex": 1, "fast": 2, "standard": 3}
	indexes := map[[2]string]int{}
	var merged []types.UsageEntry
	for _, e := range result {
		key := [2]string{"", signature(e, false)}
		if id, ok := e.Raw["response_id"].(string); ok {
			key = [2]string{id, ""}
		}
		if i, ok := indexes[key]; ok {
			if tierRank[e.Speed] > tierRank[merged[i].Speed] {
				merged[i].Speed = e.Speed
			}
			continue
		}
		indexes[key] = len(merged)
		merged = append(merged, e)
	}
	return finalize(merged, loc, false), errors.Join(loadErrors...)
}
