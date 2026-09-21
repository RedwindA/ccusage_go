package sourcesb

import (
	"context"
	"encoding/binary"
	"fmt"
	"github.com/RedwindA/ccusage_go/internal/types"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type protoField struct {
	num   int
	wire  uint64
	value uint64
	data  []byte
}

func proto(b []byte) ([]protoField, error) {
	var fields []protoField
	for len(b) > 0 {
		k, l := binary.Uvarint(b)
		if l <= 0 || k>>3 == 0 {
			return nil, fmt.Errorf("invalid protobuf tag")
		}
		b = b[l:]
		f := protoField{num: int(k >> 3), wire: k & 7}
		switch k & 7 {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, fmt.Errorf("invalid protobuf varint")
			}
			f.value = v
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return nil, fmt.Errorf("truncated fixed64")
			}
			b = b[8:]
		case 2:
			n, l := binary.Uvarint(b)
			if l <= 0 || n > uint64(len(b)-l) {
				return nil, fmt.Errorf("truncated protobuf bytes")
			}
			b = b[l:]
			f.data = b[:int(n)]
			b = b[int(n):]
		case 5:
			if len(b) < 4 {
				return nil, fmt.Errorf("truncated fixed32")
			}
			b = b[4:]
		default:
			return nil, fmt.Errorf("unsupported protobuf wire type")
		}
		fields = append(fields, f)
	}
	return fields, nil
}
func pb(p []protoField, k int) []byte {
	for _, f := range p {
		if f.num == k && f.wire == 2 {
			return f.data
		}
	}
	return nil
}
func pn(p []protoField, k int) int {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i].num == k && p[i].wire == 0 {
			return int(p[i].value)
		}
	}
	return 0
}
func pt(p []protoField, k int) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i].num == k && p[i].wire == 2 {
			if utf8.Valid(p[i].data) {
				return strings.TrimSpace(string(p[i].data))
			}
			return ""
		}
	}
	return ""
}
func ptime(b []byte) time.Time {
	p, e := proto(b)
	if e != nil || pn(p, 1) <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(pn(p, 1)), int64(min(pn(p, 2), 999999999)))
}

type antiMeta struct {
	model     string
	timestamp time.Time
	usages    [][]byte
	provider  int
}

func antiMetadata(b []byte, step bool) (antiMeta, error) {
	m := antiMeta{}
	p, err := proto(b)
	if err != nil {
		return m, err
	}
	usageKey, retryKey := 4, 17
	if step {
		usageKey, retryKey = 9, 28
		info, e := proto(pb(p, 24))
		if e != nil {
			return m, e
		}
		m.model = first(pt(info, 12), pt(info, 8))
		if m.model == "" && pn(info, 1) > 0 {
			m.model = antiModelID(pn(info, 1))
		}
		m.provider = pn(info, 7)
		m.timestamp = ptime(pb(p, 8))
		if m.timestamp.IsZero() {
			m.timestamp = ptime(pb(p, 1))
		}
	} else {
		b := pb(p, 1)
		if b == nil {
			return m, fmt.Errorf("missing chat model field 1")
		}
		p, err = proto(b)
		if err != nil {
			return m, err
		}
		m.model = first(pt(p, 19), pt(p, 21))
		if m.model == "" && pn(p, 3) > 0 {
			m.model = antiModelID(pn(p, 3))
		}
		info, e := proto(pb(p, 9))
		if e != nil {
			return m, e
		}
		m.timestamp = ptime(pb(info, 4))
	}
	if b := pb(p, usageKey); b != nil {
		m.usages = append(m.usages, b)
		if !step && m.model == "" {
			usage, e := proto(b)
			if e != nil {
				return m, e
			}
			if pn(usage, 1) > 0 {
				m.model = antiModelID(pn(usage, 1))
			}
		}
	}
	for _, f := range p {
		if f.num == retryKey {
			retry, e := proto(f.data)
			if e != nil {
				return m, e
			}
			if b := pb(retry, 2); b != nil {
				m.usages = append(m.usages, b)
			}
		}
	}
	return m, nil
}
func loadAntigravity(ctx context.Context, file string) ([]types.UsageEntry, error) {
	db, err := openDB(file)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if !table(ctx, db, "gen_metadata") {
		return nil, fmt.Errorf("missing gen_metadata table")
	}
	var generations, steps []antiMeta
	for _, tab := range []string{"gen_metadata", "steps"} {
		if !table(ctx, db, tab) {
			continue
		}
		col := "data"
		if tab == "steps" {
			col = "metadata"
		}
		rr, e := rows(ctx, db, "SELECT idx, "+col+" FROM "+tab+" WHERE "+col+" IS NOT NULL ORDER BY idx")
		if e != nil {
			return nil, e
		}
		for _, r := range rr {
			b, ok := r[col].([]byte)
			if !ok {
				b = []byte(s(r[col]))
			}
			m, e := antiMetadata(b, tab == "steps")
			if e != nil {
				return nil, fmt.Errorf("%s row %v: %w", tab, r["idx"], e)
			}
			if tab == "steps" {
				steps = append(steps, m)
			} else {
				generations = append(generations, m)
			}
		}
	}
	trajectory := time.Time{}
	if table(ctx, db, "trajectory_metadata_blob") {
		rr, e := rows(ctx, db, "SELECT data FROM trajectory_metadata_blob ORDER BY rowid")
		if e != nil {
			return nil, e
		}
		for _, r := range rr {
			b, _ := r["data"].([]byte)
			p, e := proto(b)
			if e != nil {
				return nil, e
			}
			t := ptime(pb(p, 2))
			if !t.IsZero() && trajectory.IsZero() {
				trajectory = t
			}
		}
	}
	lastModel := ""
	for _, m := range generations {
		if m.model != "" {
			lastModel = m.model
		}
	}
	for i := range steps {
		if steps[i].model == "" {
			steps[i].model = lastModel
		}
	}
	current := ""
	for i := range generations {
		if generations[i].model != "" {
			current = generations[i].model
		}
		generations[i].model = current
	}
	metas := append(steps, generations...)
	var out []types.UsageEntry
	session := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	identityTimes := map[string]time.Time{}
	for _, m := range metas {
		for _, b := range m.usages {
			p, err := proto(b)
			if err != nil {
				return nil, err
			}
			model := m.model
			if pn(p, 1) > 0 {
				model = antiModelID(pn(p, 1))
			}
			model = normalizeAnti(first(model, "gemini-internal-model"))
			e := types.UsageEntry{SourceFile: file, SessionID: session, Model: model, InputTokens: pn(p, 2), CacheCreationInputTokens: pn(p, 4), CacheReadInputTokens: pn(p, 5), ReasoningTokens: pn(p, 9), Raw: obj{}}
			totalOutput := max(pn(p, 3), pn(p, 10)+pn(p, 9))
			e.OutputTokens = max(pn(p, 10), totalOutput-e.ReasoningTokens)
			e.ReasoningTokens = max(e.ReasoningTokens, totalOutput-e.OutputTokens)
			finish(&e, 0)
			if e.TotalTokens == 0 {
				continue
			}
			ids := []string{}
			for _, f := range []struct {
				k      int
				prefix string
			}{{11, "response:"}, {12, "provider:"}, {7, "message:"}} {
				if v := pt(p, f.k); v != "" {
					ids = append(ids, f.prefix+v)
				}
			}
			e.Raw["identities"] = ids
			e.Raw["provider"] = pn(p, 6)
			if pn(p, 6) == 0 {
				e.Raw["provider"] = m.provider
			}
			rank := 0
			e.Timestamp = m.timestamp
			if !e.Timestamp.IsZero() {
				rank = 3
			} else {
				for _, id := range ids {
					if t, ok := identityTimes[id]; ok {
						e.Timestamp = t
						rank = 3
						break
					}
				}
			}
			if e.Timestamp.IsZero() && !trajectory.IsZero() {
				e.Timestamp = trajectory
				rank = 1
			}
			if e.Timestamp.IsZero() {
				e.Timestamp = mtime(file)
			}
			e.Raw["timestamp_rank"] = rank
			for _, id := range ids {
				if rank == 3 {
					identityTimes[id] = e.Timestamp
				}
			}
			e.ID = first(pt(p, 11), pt(p, 12), pt(p, 7))
			idrank := 1
			if pt(p, 12) != "" {
				idrank = 2
			}
			if pt(p, 11) != "" {
				idrank = 3
			}
			e.Raw["id_rank"] = idrank
			if e.ID == "" {
				e.ID = fmt.Sprintf("antigravity:%s:%d", file, len(out))
			}
			out = append(out, e)
		}
	}
	return out, nil
}
func dedupAntigravity(all []types.UsageEntry) []types.UsageEntry {
	indexes := map[string]int{}
	var result []types.UsageEntry
	dead := map[int]bool{}
	for _, e := range all {
		ids, _ := e.Raw["identities"].([]string)
		matches := map[int]bool{}
		target := -1
		for _, id := range ids {
			if i, ok := indexes[id]; ok {
				matches[i] = true
				if target < 0 || i < target {
					target = i
				}
			}
		}
		if target < 0 {
			target = len(result)
			result = append(result, e)
		} else {
			for i := range matches {
				if i != target {
					mergeAnti(&result[target], result[i])
					dead[i] = true
				}
			}
			mergeAnti(&result[target], e)
		}
		allids, _ := result[target].Raw["identities"].([]string)
		for _, id := range allids {
			indexes[id] = target
		}
	}
	var out []types.UsageEntry
	for i, e := range result {
		if !dead[i] {
			out = append(out, e)
		}
	}
	return out
}
func mergeAnti(a *types.UsageEntry, b types.UsageEntry) {
	a.InputTokens = max(a.InputTokens, b.InputTokens)
	a.OutputTokens = max(a.OutputTokens, b.OutputTokens)
	a.ReasoningTokens = max(a.ReasoningTokens, b.ReasoningTokens)
	a.CacheReadInputTokens = max(a.CacheReadInputTokens, b.CacheReadInputTokens)
	a.CacheCreationInputTokens = max(a.CacheCreationInputTokens, b.CacheCreationInputTokens)
	finish(a, 0)
	if n(a.Raw["provider"]) == 0 {
		a.Raw["provider"] = b.Raw["provider"]
	}
	if a.Model == "gemini-internal-model" {
		a.Model = b.Model
	}
	ar, br := n(a.Raw["timestamp_rank"]), n(b.Raw["timestamp_rank"])
	if br > ar || br == ar && b.Timestamp.Before(a.Timestamp) {
		a.Timestamp = b.Timestamp
		a.Raw["timestamp_rank"] = br
	}
	if n(b.Raw["id_rank"]) > n(a.Raw["id_rank"]) {
		a.ID = b.ID
		a.Raw["id_rank"] = b.Raw["id_rank"]
	}
	ai, _ := a.Raw["identities"].([]string)
	bi, _ := b.Raw["identities"].([]string)
	a.Raw["identities"] = append(ai, bi...)
}

func antiModelID(id int) string {
	switch id {
	case 246:
		return "gemini-2.5-pro"
	case 312:
		return "gemini-2.5-flash"
	case 313, 329:
		return "gemini-2.5-flash-thinking"
	case 330:
		return "gemini-2.5-flash-lite"
	case 281, 282:
		return "claude-4-sonnet"
	case 290, 291:
		return "claude-4-opus"
	case 333, 334:
		return "claude-4.5-sonnet"
	case 340, 341:
		return "claude-4.5-haiku"
	case 342:
		return "model_openai_gpt_oss_120b_medium"
	case 1318:
		return "gemini-3.8-flash-high"
	case 1319:
		return "gemini-3.8-flash-medium"
	case 1320:
		return "gemini-3.8-flash-low"
	case 1298:
		return "gemini-3.7-flash-high"
	case 1299:
		return "gemini-3.7-flash-medium"
	case 1300:
		return "gemini-3.7-flash-low"
	case 1071:
		return "gemini-3.6-flash-high"
	case 1072:
		return "gemini-3.6-flash-medium"
	case 1073:
		return "gemini-3.6-flash-low"
	}
	if id >= 1000 {
		return fmt.Sprintf("model_placeholder_m%d", id-1000)
	}
	return fmt.Sprintf("antigravity-model-%d", id)
}

var antiAliases = map[string]string{
	"gemini 3.8 flash (high)":          "gemini-3.8-flash-high",
	"gemini 3.8 flash (medium)":        "gemini-3.8-flash-medium",
	"gemini 3.8 flash (low)":           "gemini-3.8-flash-low",
	"gemini 3.7 flash (high)":          "gemini-3.7-flash-high",
	"gemini 3.7 flash (medium)":        "gemini-3.7-flash-medium",
	"gemini 3.7 flash (low)":           "gemini-3.7-flash-low",
	"gemini 3.6 flash (high)":          "gemini-3.6-flash-high",
	"gemini 3.6 flash (medium)":        "gemini-3.6-flash-medium",
	"gemini 3.6 flash (low)":           "gemini-3.6-flash-low",
	"gemini 3.8 flash":                 "gemini-3.8-flash",
	"gemini 3.8 flash thinking":        "gemini-3.8-flash",
	"gemini 3.7 flash":                 "gemini-3.7-flash",
	"gemini 3.7 flash thinking":        "gemini-3.7-flash",
	"gemini 3.7 pro":                   "gemini-3.7-pro",
	"gemini 3.7 pro thinking":          "gemini-3.7-pro",
	"gemini 3.6 flash":                 "gemini-3.6-flash",
	"gemini 3 flash":                   "gemini-3.6-flash",
	"gemini 3.6 pro":                   "gemini-3.6-pro",
	"gemini 3 pro":                     "gemini-3-pro",
	"gemini 3 pro thinking":            "gemini-3-pro",
	"gemini 2.5 flash":                 "gemini-2.5-flash",
	"gemini 2.5 pro":                   "gemini-2.5-pro",
	"gemini 2.0 flash":                 "gemini-2.0-flash",
	"gemini 2 flash":                   "gemini-2.0-flash",
	"gemini 2.0 pro":                   "gemini-2.0-pro",
	"gemini 1.5 flash":                 "gemini-1.5-flash",
	"gemini 1.5 pro":                   "gemini-1.5-pro",
	"model_placeholder_m318":           "gemini-3.8-flash-high",
	"model_placeholder_m319":           "gemini-3.8-flash-medium",
	"model_placeholder_m320":           "gemini-3.8-flash-low",
	"model_placeholder_m298":           "gemini-3.7-flash-high",
	"model_placeholder_m299":           "gemini-3.7-flash-medium",
	"model_placeholder_m300":           "gemini-3.7-flash-low",
	"model_placeholder_m71":            "gemini-3.6-flash-high",
	"model_placeholder_m72":            "gemini-3.6-flash-medium",
	"model_placeholder_m73":            "gemini-3.6-flash-low",
	"model_placeholder_m26":            "claude-opus-4-6",
	"model_placeholder_m35":            "claude-sonnet-4-6",
	"model_placeholder_m36":            "gemini-3.1-pro",
	"model_placeholder_m37":            "gemini-3.1-pro",
	"model_placeholder_m16":            "gemini-3.1-pro",
	"model_placeholder_m18":            "gemini-3-flash-preview",
	"model_placeholder_m84":            "gemini-3-flash-preview",
	"model_placeholder_m47":            "gemini-3-flash-preview",
	"model_placeholder_m132":           "gemini-3.5-flash-high",
	"model_placeholder_m133":           "gemini-3.5-flash-high",
	"model_placeholder_m187":           "gemini-3.5-flash-extra-low",
	"model_placeholder_m20":            "gemini-3.5-flash-medium",
	"model_openai_gpt_oss_120b_medium": "gpt-oss-120b-medium",
	"gemini-pro-default":               "gemini-3.1-pro",
	"gemini-pro-agent":                 "gemini-3.1-pro",
	"gemini-3-flash-agent":             "gemini-3.5-flash-high",
	"gemini-3-flash-agent-a":           "gemini-3.5-flash-high",
	"gemini-3-flash-agent-b":           "gemini-3.5-flash-high",
	"gemini-3-flash-a":                 "gemini-3.5-flash-high",
	"gemini-3-flash-b":                 "gemini-3.5-flash-high",
	"gemini-3-flash-c":                 "gemini-3-flash-preview",
	"gemini-3-flash":                   "gemini-3-flash-preview",
	"gemini-3.5-flash-low":             "gemini-3.5-flash-medium",
	"gemini-3.1-pro-high":              "gemini-3.1-pro",
	"gemini-3.1-pro-low":               "gemini-3.1-pro",
	"gemini-3-pro-high":                "gemini-3-pro",
	"gemini-3-pro-low":                 "gemini-3-pro",
	"claude 3.7 sonnet":                "claude-3-7-sonnet",
	"claude 3.7 sonnet thinking":       "claude-3-7-sonnet",
	"claude 3.5 sonnet":                "claude-3-5-sonnet",
	"claude 3.5 haiku":                 "claude-3-5-haiku",
	"claude 3 opus":                    "claude-3-opus",
}

func normalizeAnti(model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	if v, ok := antiAliases[lower]; ok {
		return v
	}
	base := strings.TrimSpace(strings.SplitN(lower, "(", 2)[0])
	if v, ok := antiAliases[base]; ok {
		return v
	}
	converted := strings.ReplaceAll(base, " ", "-")
	if strings.HasPrefix(converted, "gemini-") || strings.HasPrefix(converted, "claude-") || strings.HasPrefix(converted, "gpt-") {
		return converted
	}
	return model
}
