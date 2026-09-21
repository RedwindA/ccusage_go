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

type piSession struct {
	entries            []types.UsageEntry
	active             []int
	valid, headerValid bool
	parent             string
	fork               time.Time
}

func parsePi(p string) (piSession, error) {
	rows, err := readLines(p)
	s := piSession{valid: true}
	if err != nil {
		return s, err
	}
	if len(rows) > 0 && str(rows[0]["type"]) == "session" {
		h := rows[0]
		s.fork = timestamp(h["timestamp"])
		s.parent = str(h["parentSession"])
		s.headerValid = !s.fork.IsZero()
		if v, ok := h["parentSession"]; ok && str(v) == "" {
			s.headerValid = false
		}
	}
	session := stem(p)
	if _, id, ok := strings.Cut(session, "_"); ok {
		session = id
	}
	project := "unknown"
	parts := strings.Split(filepath.ToSlash(p), "/")
	for i, part := range parts {
		if part == "sessions" && i+1 < len(parts) {
			project = parts[i+1]
			break
		}
	}
	links := map[string]string{}
	linked := false
	leaf := ""
	usageIndex := map[string]int{}
	for _, r := range rows {
		if str(r["type"]) != "session" {
			id, parent := str(r["id"]), str(r["parentId"])
			if id != "" || parent != "" {
				linked = true
			}
			if _, ok := links[id]; ok {
				s.valid = false
			}
			links[id] = parent
			leaf = id
		}
		m := obj(r["message"])
		u := obj(m["usage"])
		typ := str(r["type"])
		t := timestamp(r["timestamp"])
		if (typ != "" && typ != "message") || str(m["role"]) != "assistant" || u == nil || t.IsZero() {
			continue
		}
		e := entry(p, "pi", session, project, str(m["model"]), t)
		e.ID = str(r["id"])
		e.Raw["source_format"] = "pi"
		e.InputTokens = count(u, "input")
		e.OutputTokens = count(u, "output")
		e.CacheReadInputTokens = count(u, "cacheRead")
		e.CacheCreationInputTokens = count(u, "cacheWrite")
		total(&e, count(u, "totalTokens"), 0)
		cost(&e, obj(u["cost"])["total"])
		if e.TotalTokens == 0 {
			continue
		}
		usageIndex[e.ID] = len(s.entries)
		s.entries = append(s.entries, e)
	}
	if !linked {
		s.valid = true
		for i := range s.entries {
			s.active = append(s.active, i)
		}
		return s, nil
	}
	if _, ok := links[""]; ok {
		s.valid = false
	}
	for id := range links {
		visited := map[string]bool{}
		for cur := id; cur != ""; cur = links[cur] {
			if visited[cur] {
				s.valid = false
				break
			}
			visited[cur] = true
			if _, ok := links[cur]; !ok {
				s.valid = false
				break
			}
		}
	}
	if s.valid {
		for cur := leaf; cur != ""; cur = links[cur] {
			if index, ok := usageIndex[cur]; ok {
				s.active = append(s.active, index)
			}
		}
		for i, j := 0, len(s.active)-1; i < j; i, j = i+1, j-1 {
			s.active[i], s.active[j] = s.active[j], s.active[i]
		}
	}
	return s, nil
}
func piEqual(a, b types.UsageEntry, mode string) bool {
	if mode == "calculate" {
		a.Cost = 0
		b.Cost = 0
		a.HasCost = false
		b.HasCost = false
	}
	if mode == "display" {
		a.HasCost = true
		b.HasCost = true
	}
	a.SessionID = ""
	b.SessionID = ""
	return signature(a, true) == signature(b, true)
}
func loadPi(ctx context.Context, files []string, loc *time.Location, mode string) ([]types.UsageEntry, error) {
	sessions := make([]piSession, len(files))
	var loadErrors []error
	byPath := map[string]int{}
	for i, p := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var err error
		sessions[i], err = parsePi(p)
		if err != nil {
			loadErrors = append(loadErrors, fmt.Errorf("pi source %s: %w", p, err))
		}
		abs, _ := filepath.Abs(p)
		byPath[abs] = i
	}
	parents := map[int]int{}
	invalid := map[int]bool{}
	for i, s := range sessions {
		invalid[i] = !s.headerValid
		if s.parent == "" {
			continue
		}
		abs, _ := filepath.Abs(s.parent)
		j, ok := byPath[abs]
		if !ok && !filepath.IsAbs(s.parent) {
			abs, _ = filepath.Abs(filepath.Join(filepath.Dir(files[i]), s.parent))
			j, ok = byPath[abs]
		}
		if !ok || j == i {
			invalid[i] = true
			continue
		}
		parents[i] = j
	}
	var result []types.UsageEntry
	for i, s := range sessions {
		skip := 0
		j, hasParent := parents[i]
		if hasParent {
			valid := true
			visited := map[int]bool{}
			for cur := i; ; {
				if invalid[cur] || visited[cur] {
					valid = false
					break
				}
				visited[cur] = true
				parent, ok := parents[cur]
				if !ok {
					break
				}
				cur = parent
			}
			parent := sessions[j]
			if valid && s.valid && parent.valid {
				for _, index := range parent.active {
					if skip >= len(s.entries) {
						break
					}
					e := parent.entries[index]
					if e.Timestamp.After(s.fork) || !piEqual(s.entries[skip], e, mode) {
						break
					}
					skip++
				}
			}
		}
		result = append(result, s.entries[skip:]...)
	}
	seen := map[string]bool{}
	deduped := make([]types.UsageEntry, 0, len(result))
	for _, e := range result {
		identity := e
		if mode == "calculate" {
			identity.Cost = 0
			identity.HasCost = false
		}
		if mode == "display" {
			identity.HasCost = true
		}
		key := e.ProjectPath + "|" + signature(identity, true)
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, e)
		}
	}
	return finalize(deduped, loc, false), errors.Join(loadErrors...)
}
