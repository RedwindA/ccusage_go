package reports

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

func Key(kind string) string {
	switch kind {
	case "model":
		return "models"
	case "workspace":
		return "workspaces"
	default:
		return kind
	}
}

func JSONRows(rows []*Row, o Options) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		b, _ := json.Marshal(r)
		m := map[string]any{}
		_ = json.Unmarshal(b, &m)
		if o.Agent != "" {
			delete(m, "period")
			delete(m, "agent")
			delete(m, "metadata")
			label := map[string]string{"daily": "date", "monthly": "month", "weekly": "week", "session": "sessionId", "model": "model", "workspace": "workspace"}[o.Kind]
			m[label] = r.Period
			m["messageCount"] = m["requestCount"]
			delete(m, "requestCount")
		}
		if o.Kind != "session" {
			delete(m, "projectPath")
			delete(m, "sessionName")
			delete(m, "firstActivity")
			delete(m, "lastActivity")
			if o.Kind != "workspace" {
				delete(m, "workspace")
			}
		}
		if o.Kind == "model" {
			delete(m, "modelsUsed")
			delete(m, "modelBreakdowns")
		}
		if o.Agent == "droid" && o.Kind == "model" {
			m["attribution"] = "sessionSnapshot"
		}
		if o.NoCost {
			StripCosts(m)
		}
		out = append(out, m)
	}
	return out
}

func StripCosts(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if strings.Contains(strings.ToLower(k), "cost") || k == "unpricedModels" || k == "missingPricing" {
				delete(x, k)
			} else {
				StripCosts(v)
			}
		}
	case []any:
		for _, v := range x {
			StripCosts(v)
		}
	case []map[string]any:
		for _, v := range x {
			StripCosts(v)
		}
	case map[string][]map[string]any:
		for _, v := range x {
			StripCosts(v)
		}
	}
}

func WriteCSV(w io.Writer, rows []*Row, o Options) error {
	c := csv.NewWriter(w)
	showCredits := o.Agent == "codebuff"
	for _, row := range rows {
		if row.Credits != 0 {
			showCredits = true
		}
	}
	head := []string{o.Kind, "agent", "user", "project", "models", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_tokens"}
	if showCredits {
		head = append(head, "credits")
	}
	if !o.NoCost {
		head = append(head, "total_cost")
	}
	if err := c.Write(head); err != nil {
		return err
	}
	for _, r := range rows {
		values := []string{r.Period, r.Agent, r.User, r.Project, strings.Join(r.Models, ";"), strconv.Itoa(r.Input), strconv.Itoa(r.Output), strconv.Itoa(r.CacheCreate), strconv.Itoa(r.CacheRead), strconv.Itoa(r.Total)}
		if showCredits {
			values = append(values, strconv.FormatFloat(r.Credits, 'f', 2, 64))
		}
		if !o.NoCost {
			values = append(values, strconv.FormatFloat(r.Cost, 'f', 8, 64))
		}
		if err := c.Write(values); err != nil {
			return err
		}
	}
	c.Flush()
	return c.Error()
}

func terminalText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r >= 127 && r < 160 {
			return ' '
		}
		return r
	}, s)
}
