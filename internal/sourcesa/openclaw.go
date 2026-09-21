package sourcesa

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/RedwindA/ccusage_go/internal/types"
	_ "modernc.org/sqlite"
)

type clawState struct{ model, provider string }

func (state *clawState) parse(p, session string, r object, fallback time.Time) *types.UsageEntry {
	typ := str(r["type"])
	if typ == "model_change" || (typ == "custom" && str(r["customType"]) == "model-snapshot") {
		source := obj(r["data"])
		if source == nil {
			source = r
		}
		state.model = first(str(source["modelId"]), str(source["model"]), state.model)
		state.provider = first(str(source["provider"]), state.provider)
		return nil
	}
	m := obj(r["message"])
	u := obj(m["usage"])
	if typ != "message" || str(m["role"]) != "assistant" || u == nil {
		return nil
	}
	v, ok := m["timestamp"]
	if !ok {
		v = r["timestamp"]
	}
	t := timestamp(v)
	if n, ok := v.(float64); ok && n >= 0 {
		t = time.UnixMilli(int64(n))
	}
	if t.IsZero() {
		t = fallback
	}
	e := entry(p, "openclaw", session, "OpenClaw", first(str(m["modelId"]), str(m["model"]), state.model), t)
	e.Workspace = "unknown"
	e.ID = str(r["id"])
	e.Raw["provider"] = first(str(m["provider"]), state.provider)
	e.InputTokens = count(u, "input")
	e.OutputTokens = count(u, "output")
	e.CacheReadInputTokens = count(u, "cacheRead")
	e.CacheCreationInputTokens = count(u, "cacheWrite")
	total(&e, count(u, "totalTokens"), 0)
	cost(&e, obj(u["cost"])["total"])
	return &e
}
func loadOpenClaw(p string) ([]types.UsageEntry, error) {
	rows, err := readLines(p)
	if err != nil {
		return nil, err
	}
	state := clawState{}
	session, _, _ := strings.Cut(filepath.Base(p), ".jsonl")
	var entries []types.UsageEntry
	for _, r := range rows {
		if e := state.parse(p, session, r, fileTime(p)); e != nil {
			entries = append(entries, *e)
		}
	}
	return entries, nil
}
func loadOpenClawDB(ctx context.Context, p string) ([]types.UsageEntry, error) {
	u := url.URL{Scheme: "file", Path: p, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT session_id, event_json, created_at FROM transcript_events ORDER BY session_id ASC, seq ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []types.UsageEntry
	session := ""
	state := clawState{}
	for rows.Next() {
		var sid, data string
		var created interface{}
		if rows.Scan(&sid, &data, &created) != nil {
			continue
		}
		if sid != session {
			session = sid
			state = clawState{}
		}
		var r object
		if json.Unmarshal([]byte(data), &r) != nil {
			continue
		}
		ms := int64(0)
		switch v := created.(type) {
		case int64:
			ms = v
		case float64:
			ms = int64(v)
		}
		if e := state.parse(p, session, r, time.UnixMilli(max(0, ms))); e != nil {
			e.Raw["sqlite"] = true
			entries = append(entries, *e)
		}
	}
	return entries, rows.Err()
}
func mergeOpenClaw(entries []types.UsageEntry) []types.UsageEntry {
	var out []types.UsageEntry
	seen := map[string]bool{}
	ids := map[string]bool{}
	for pass := 0; pass < 2; pass++ {
		for _, e := range entries {
			sqlite, _ := e.Raw["sqlite"].(bool)
			if sqlite != (pass == 1) {
				continue
			}
			if sqlite && e.ID != "" {
				id := e.SessionID + "|" + e.ID
				if ids[id] {
					continue
				}
				ids[id] = true
				replaced := false
				for i, old := range out {
					oldSQLite, _ := old.Raw["sqlite"].(bool)
					if !oldSQLite && signature(old, false) == signature(e, false) {
						out[i] = e
						replaced = true
						break
					}
				}
				if !replaced {
					out = append(out, e)
				}
			} else {
				key := signature(e, true)
				if !seen[key] {
					seen[key] = true
					out = append(out, e)
				}
			}
		}
	}
	return out
}
