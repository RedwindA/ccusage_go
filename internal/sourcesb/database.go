package sourcesb

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/RedwindA/ccusage_go/internal/types"
	_ "modernc.org/sqlite"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func openDB(file string) (*sql.DB, error) {
	absolute, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	path := filepath.ToSlash(absolute)
	// SQLite file URIs require /C:/... for Windows drive paths.
	if len(path) >= 2 && path[1] == ':' {
		path = "/" + path
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, e := sql.Open("sqlite", u.String())
	if e == nil {
		db.SetMaxOpenConns(1)
	}
	return db, e
}
func rows(ctx context.Context, db *sql.DB, query string) ([]obj, error) {
	r, e := db.QueryContext(ctx, query)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	cols, e := r.Columns()
	if e != nil {
		return nil, e
	}
	var out []obj
	for r.Next() {
		vals := make([]interface{}, len(cols))
		ptr := make([]interface{}, len(cols))
		for i := range vals {
			ptr[i] = &vals[i]
		}
		if e = r.Scan(ptr...); e != nil {
			return nil, e
		}
		m := obj{}
		for i, k := range cols {
			m[k] = vals[i]
		}
		out = append(out, m)
	}
	return out, r.Err()
}
func table(ctx context.Context, db *sql.DB, name string) bool {
	var x int
	return db.QueryRowContext(ctx, "SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&x) == nil
}

// hasColumns reports whether table exists with every one of columns.
func hasColumns(ctx context.Context, db *sql.DB, table string, columns ...string) bool {
	r, err := db.QueryContext(ctx, `SELECT * FROM "`+table+`" LIMIT 0`)
	if err != nil {
		return false
	}
	defer r.Close()
	names, err := r.Columns()
	if err != nil {
		return false
	}
	for _, column := range columns {
		if !slices.Contains(names, column) {
			return false
		}
	}
	return true
}
func loadDatabase(ctx context.Context, name, file string) ([]types.UsageEntry, error) {
	db, err := openDB(file)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var out []types.UsageEntry
	switch name {
	case "opencode", "kilo":
		for _, tab := range []string{"message", "session_message"} {
			if name == "kilo" && tab != "message" {
				continue
			}
			if !table(ctx, db, tab) {
				continue
			}
			rr, err := rows(ctx, db, "SELECT * FROM "+tab)
			if err != nil {
				return nil, err
			}
			var forkCopies map[string]int64
			if tab == "session_message" {
				forkCopies = forkCopyCutoffs(ctx, db)
			}
			for _, r := range rr {
				m := decode([]byte(s(r["data"])))
				if m == nil {
					continue
				}
				if tab == "session_message" {
					if s(r["type"]) != "assistant" {
						continue
					}
					// Copies neither count nor mark the fork as covered: the
					// fork's session_v2 counters start at zero and hold only
					// fork-local usage, so they stay a valid fallback. Without
					// a seq the row cannot be classified, so it stays.
					if cutoff, ok := forkCopies[s(r["session_id"])]; ok && r["seq"] != nil && int64(f(r["seq"])) <= cutoff {
						continue
					}
					model := object(m["model"])
					m["modelID"] = first(s(model["id"]), s(model["modelID"]), s(m["modelID"]))
					m["providerID"] = first(s(model["providerID"]), s(m["providerID"]))
					if object(m["time"])["created"] == nil {
						m["time"] = obj{"created": r["time_created"]}
					}
				}
				if e, ok := openMessage(m, name, file, s(r["id"]), s(r["session_id"]), r); ok {
					out = append(out, e)
				}
			}
		}

		if name == "opencode" {
			for _, tab := range []string{"session_v2", "session"} {
				if !table(ctx, db, tab) || tab == "session" && !table(ctx, db, "session_message") {
					continue
				}
				rr, err := rows(ctx, db, "SELECT * FROM "+tab)
				if err != nil {
					return nil, err
				}
				for _, r := range rr {
					if r["tokens_input"] == nil && r["cost"] == nil {
						continue
					}
					model, provider := "unknown", "unknown"
					rawModel := s(r["model"])
					if m := decode([]byte(rawModel)); m != nil {
						model = first(s(m["id"]), s(m["modelID"]), model)
						provider = first(s(m["providerID"]), s(m["provider"]), provider)
					} else if rawModel != "" {
						model = strings.Trim(rawModel, "\"")
					}
					e := types.UsageEntry{ID: "session:" + s(r["id"]), SessionID: s(r["id"]), SourceFile: file, Model: model, Timestamp: time.UnixMilli(int64(f(r["time_created"]))), InputTokens: n(r["tokens_input"]), OutputTokens: n(r["tokens_output"]), CacheReadInputTokens: n(r["tokens_cache_read"]), CacheCreationInputTokens: n(r["tokens_cache_write"]), ReasoningTokens: n(r["tokens_reasoning"]), Cost: max(0, f(r["cost"])), HasCost: r["cost"] != nil, Raw: obj{"session_aggregate": true, "provider": provider}}
					finish(&e, 0)
					if e.TotalTokens > 0 || e.Cost > 0 {
						out = append(out, e)
					}
				}
			}
		}
	case "hermes", "goose":
		if !table(ctx, db, "sessions") {
			return out, nil
		}
		rr, err := rows(ctx, db, "SELECT * FROM sessions")
		if err != nil {
			return nil, err
		}
		for _, r := range rr {
			e := types.UsageEntry{SourceFile: file, SessionID: s(r["id"]), Raw: r}
			if name == "hermes" {
				e.ID = "hermes:" + e.SessionID
				e.Model = s(r["model"])
				e.Timestamp = ts(r["started_at"])
				e.InputTokens = n(r["input_tokens"])
				e.OutputTokens = n(r["output_tokens"])
				e.CacheReadInputTokens = n(r["cache_read_tokens"])
				e.CacheCreationInputTokens = n(r["cache_write_tokens"])
				e.ReasoningTokens = n(r["reasoning_tokens"])
				v := pick(r, "actual_cost_usd", "estimated_cost_usd")
				e.HasCost = v != nil
				e.Cost = max(0, f(v))
				e.Raw["request_count"] = n(r["message_count"])
				e.Raw["provider"] = r["billing_provider"]
				finish(&e, 0)
			} else {
				e.ID = e.SessionID
				e.Model = s(decode([]byte(s(r["model_config_json"])))["model_name"])
				e.Timestamp = ts(r["created_at"])
				e.InputTokens = n(r["accumulated_input_tokens"])
				if e.InputTokens == 0 {
					e.InputTokens = n(r["input_tokens"])
				}
				e.OutputTokens = n(r["accumulated_output_tokens"])
				if e.OutputTokens == 0 {
					e.OutputTokens = n(r["output_tokens"])
				}
				total := n(r["accumulated_total_tokens"])
				if total == 0 {
					total = n(r["total_tokens"])
				}
				e.ReasoningTokens = max(0, total-e.InputTokens-e.OutputTokens)
				finish(&e, 0)
				e.Raw["provider"] = r["provider_name"]
			}
			if e.SessionID != "" && e.Model != "" && !e.Timestamp.IsZero() && (e.TotalTokens > 0 || e.Cost > 0) {
				out = append(out, e)
			}
		}
	case "zcode":
		// As upstream, skip databases without the columns it queries.
		if !hasColumns(ctx, db, "model_usage", "id", "session_id", "started_at", "model_id", "status", "input_tokens", "output_tokens") || !hasColumns(ctx, db, "session", "id", "directory") {
			return out, nil
		}
		sessionRows, err := rows(ctx, db, "SELECT * FROM session")
		if err != nil {
			return nil, err
		}
		sessions := map[string]obj{}
		for _, r := range sessionRows {
			sessions[s(r["id"])] = r
		}
		rr, err := rows(ctx, db, "SELECT * FROM model_usage WHERE status='completed'")
		if err != nil {
			return nil, err
		}
		for _, r := range rr {
			e := types.UsageEntry{ID: s(r["id"]), SessionID: s(r["session_id"]), SourceFile: file, Model: s(r["model_id"]), Timestamp: time.UnixMilli(int64(f(r["started_at"]))), Raw: r}
			if e.ID == "" || e.SessionID == "" || e.Model == "" || f(r["started_at"]) <= 0 {
				continue
			}
			input := n(r["input_tokens"])
			e.CacheReadInputTokens = min(input, n(r["cache_read_input_tokens"]))
			e.CacheCreationInputTokens = min(input-e.CacheReadInputTokens, n(r["cache_creation_input_tokens"]))
			e.InputTokens = input - e.CacheReadInputTokens - e.CacheCreationInputTokens
			e.OutputTokens = n(r["output_tokens"])
			e.ProjectPath = first(s(sessions[e.SessionID]["directory"]), "ZCode")
			e.Workspace = e.ProjectPath
			e.Raw["provider"] = r["provider_id"]
			finish(&e, n(r["computed_total_tokens"]))
			if e.TotalTokens > 0 {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// forkCopyCutoffs maps each OpenCode v2 fork session to the largest
// session_message seq that is still history copied from its parent: the
// boundary's seq for a "through" fork, or the parent's last seq below it for a
// "before" fork. Forks whose parent, boundary row or seq column is missing are
// left out, so they keep every row instead of being cut on a guess.
func forkCopyCutoffs(ctx context.Context, db *sql.DB) map[string]int64 {
	cutoffs := map[string]int64{}
	if !table(ctx, db, "session_v2") {
		return cutoffs
	}
	forks, err := rows(ctx, db, "SELECT id, fork_session_id, fork_boundary FROM session_v2 WHERE fork_session_id IS NOT NULL AND fork_boundary IS NOT NULL")
	if err != nil {
		return cutoffs
	}
	for _, r := range forks {
		fork, parent := s(r["id"]), s(r["fork_session_id"])
		boundary := decode([]byte(s(r["fork_boundary"])))
		if fork == "" || parent == "" {
			continue
		}
		// Every copy gets a new id, so the boundary message is looked up in
		// the parent and the fork's rows are compared by the preserved seq.
		var seq int64
		if db.QueryRowContext(ctx, "SELECT seq FROM session_message WHERE session_id = ? AND id = ? LIMIT 1", parent, s(boundary["messageID"])).Scan(&seq) != nil {
			continue
		}
		switch s(boundary["type"]) {
		case "through":
			cutoffs[fork] = seq
		case "before":
			// seq is sparse and the fork's own rows continue right after the
			// last copied row, so seq-1 could swallow them.
			var last sql.NullInt64
			if db.QueryRowContext(ctx, "SELECT MAX(seq) FROM session_message WHERE session_id = ? AND seq < ?", parent, seq).Scan(&last) == nil && last.Valid {
				cutoffs[fork] = last.Int64
			}
		}
	}
	return cutoffs
}
func openMessage(m obj, name, file, id, session string, row obj) (types.UsageEntry, bool) {
	e := types.UsageEntry{SourceFile: file, Raw: m}
	if m == nil {
		return e, false
	}
	if name == "kilo" && s(m["role"]) != "assistant" {
		return e, false
	}
	t := object(m["tokens"])
	if t == nil {
		return e, false
	}
	e.Model = s(m["modelID"])
	if e.Model == "" || name == "opencode" && s(m["providerID"]) == "" {
		return e, false
	}
	e.ID = first(id, s(m["id"]))
	e.SessionID = first(session, s(m["sessionID"]), s(m["session_id"]))
	if name == "kilo" {
		e.ID = first(s(m["id"]), file+":"+id)
		e.SessionID = first(s(m["session_id"]), session)
	}
	e.Timestamp = ts(object(m["time"])["created"])
	if name == "opencode" && f(object(m["time"])["created"]) > 0 {
		e.Timestamp = time.UnixMilli(int64(f(object(m["time"])["created"])))
	}
	if e.Timestamp.IsZero() {
		if name == "kilo" {
			return e, false
		}
		e.Timestamp = time.Unix(0, 0)
	}
	e.InputTokens = n(t["input"])
	e.OutputTokens = n(t["output"])
	e.ReasoningTokens = n(t["reasoning"])
	e.CacheCreationInputTokens = n(object(t["cache"])["write"])
	e.CacheReadInputTokens = n(object(t["cache"])["read"])
	e.HasCost = m["cost"] != nil
	e.Cost = max(0, f(m["cost"]))
	e.Raw["provider"] = m["providerID"]
	finish(&e, n(t["total"]))
	if e.ID == "" {
		e.ID = fmt.Sprintf("%s:%s:%d", name, file, e.Timestamp.UnixMilli())
	}
	return e, e.TotalTokens > 0
}
