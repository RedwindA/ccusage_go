package reports

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
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

func WriteTable(w io.Writer, rows []*Row, o Options) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No usage data found.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	title := strings.ToUpper(o.Kind[:1]) + o.Kind[1:] + " usage report (Go)"
	if o.Color {
		title = "\x1b[1;36m" + title + "\x1b[0m"
	}
	fmt.Fprintln(tw, title)
	headers := []string{strings.ToUpper(o.Kind[:1]) + o.Kind[1:]}
	showUser := false
	showCredits := o.Agent == "codebuff"
	for _, r := range rows {
		if r.Credits != 0 {
			showCredits = true
		}
		if r.User != "" {
			showUser = true
		}
	}
	if showUser {
		headers = append(headers, "User")
	}
	if o.Instances {
		headers = append(headers, "Project")
	}
	if o.Agent == "" {
		headers = append(headers, "Agent")
	}
	headers = append(headers, "Models", "Input", "Output")
	if !o.Compact {
		headers = append(headers, "Cache Create", "Cache Read")
	}
	headers = append(headers, "Total Tokens")
	if showCredits {
		headers = append(headers, "Credits")
	}
	if !o.NoCost {
		if !o.Compact {
			headers = append(headers, "API Cost", "CC Cost", "CR Cost")
		}
		headers = append(headers, "Cost (USD)")
	}
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	write := func(r *Row, label string) {
		v := []string{label}
		if showUser {
			v = append(v, r.User)
		}
		if o.Instances {
			v = append(v, r.Project)
		}
		if o.Agent == "" {
			v = append(v, r.Agent)
		}
		v = append(v, strings.Join(r.Models, ", "), strconv.Itoa(r.Input), strconv.Itoa(r.Output))
		if !o.Compact {
			v = append(v, strconv.Itoa(r.CacheCreate), strconv.Itoa(r.CacheRead))
		}
		v = append(v, strconv.Itoa(r.Total))
		if showCredits {
			v = append(v, fmt.Sprintf("%.2f", r.Credits))
		}
		if !o.NoCost {
			if !o.Compact {
				v = append(v, fmt.Sprintf("$%.4f", r.APICost), fmt.Sprintf("$%.4f", r.CacheCreateCost), fmt.Sprintf("$%.4f", r.CacheReadCost))
			}
			v = append(v, fmt.Sprintf("$%.4f", r.Cost))
		}
		for i := range v {
			v[i] = terminalText(v[i])
		}
		fmt.Fprintln(tw, strings.Join(v, "\t"))
	}
	var total Row
	for _, r := range rows {
		write(r, r.Period)
		if o.ByAgent {
			for _, a := range r.Agents {
				write(a, "  ↳")
			}
		}
		if o.Breakdown {
			for _, m := range r.Breakdowns {
				write(&Row{Models: []string{m.Name}, Input: m.Input, Output: m.Output, CacheCreate: m.CacheCreate, CacheRead: m.CacheRead, Total: m.Total, Cost: m.Cost, APICost: m.APICost, CacheCreateCost: m.CacheCreateCost, CacheReadCost: m.CacheReadCost}, "  ↳")
			}
		}
		total.Input += r.Input
		total.Output += r.Output
		total.CacheCreate += r.CacheCreate
		total.CacheRead += r.CacheRead
		total.Total += r.Total
		total.Cost += r.Cost
		total.APICost += r.APICost
		total.CacheCreateCost += r.CacheCreateCost
		total.CacheReadCost += r.CacheReadCost
		total.Credits += r.Credits
	}
	write(&total, "Total")
	return tw.Flush()
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
