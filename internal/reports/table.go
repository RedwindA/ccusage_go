package reports

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// WriteTable mirrors ccusage's terminal reports; JSON/CSV keep the full data.
func WriteTable(w io.Writer, rows []*Row, o Options) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No usage data found.")
		return err
	}
	headers := []string{map[string]string{"daily": "Date", "weekly": "Week", "monthly": "Month", "session": "Session", "model": "Model", "workspace": "Workspace", "blocks": "Block"}[o.Kind]}
	showUser, showCredits := false, o.Agent == "codebuff"
	for _, r := range rows {
		showUser = showUser || r.User != ""
		showCredits = showCredits || r.Credits != 0
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
	headers = append(headers, "Models")
	textColumns := len(headers)
	headers = append(headers, "Input", "Output")
	if !o.Compact {
		headers = append(headers, "Cache Create", "Cache Read", "Total Tokens")
	}
	if showCredits {
		headers = append(headers, "Credits")
	}
	if !o.NoCost {
		headers = append(headers, "Cost (USD)")
	}
	table := terminalTable{headers: headers, textColumns: textColumns, width: o.TerminalWidth, color: o.Color, compactDates: o.Kind == "daily" || o.Kind == "weekly"}
	add := func(r *Row, label, agent, models string, color int) {
		cells := []string{terminalText(label)}
		if showUser {
			cells = append(cells, terminalText(r.User))
		}
		if o.Instances {
			cells = append(cells, terminalText(r.Project))
		}
		if o.Agent == "" {
			cells = append(cells, terminalText(agent))
		}
		cells = append(cells, models, tableNumber(r.Input), tableNumber(r.Output))
		if !o.Compact {
			cells = append(cells, tableNumber(r.CacheCreate), tableNumber(r.CacheRead), tableNumber(r.Total))
		}
		if showCredits {
			cells = append(cells, fmt.Sprintf("%.2f", r.Credits))
		}
		if !o.NoCost {
			cells = append(cells, fmt.Sprintf("$%.2f", r.Cost))
		}
		table.rows = append(table.rows, terminalRow{cells: cells, color: color})
	}
	breakdown := func(r *Row) {
		for _, m := range r.Breakdowns {
			add(&Row{Input: m.Input, Output: m.Output, CacheCreate: m.CacheCreate, CacheRead: m.CacheRead, Total: m.Total, Cost: m.Cost}, "", "", "  └─ "+shortTableModel(m.Name), 90)
		}
	}
	var total Row
	for _, r := range rows {
		models := tableModels(r.Models)
		if o.ByAgent && len(r.Agents) > 0 {
			models = ""
		}
		add(r, r.Period, agentLabel(r.Agent), models, 0)
		if o.ByAgent && len(r.Agents) > 0 {
			for _, a := range r.Agents {
				child := *a
				child.User, child.Project = "", ""
				add(&child, "", "- "+agentLabel(a.Agent), tableModels(a.Models), 0)
				if o.Breakdown {
					breakdown(a)
				}
			}
		} else if o.Breakdown {
			breakdown(r)
		}
		total.Input += r.Input
		total.Output += r.Output
		total.CacheCreate += r.CacheCreate
		total.CacheRead += r.CacheRead
		total.Total += r.Total
		total.Cost += r.Cost
		total.Credits += r.Credits
	}
	add(&total, "Total", "", "", 33)
	_, err := io.WriteString(w, boxTitle(reportTitle(rows, o), o.Color, o.TerminalWidth)+table.render())
	return err
}

func reportTitle(rows []*Row, o Options) string {
	kind := map[string]string{"daily": "Daily", "weekly": "Weekly", "monthly": "Monthly", "session": "Session", "model": "Model", "workspace": "Workspace", "blocks": "Blocks"}[o.Kind]
	if o.Agent != "" {
		name := agentLabel(o.Agent)
		if o.Agent == "claude" {
			name = "Claude Code"
		}
		return name + " Token Usage Report - " + kind
	}
	agents := map[string]bool{}
	for _, name := range o.DetectedAgents {
		agents[name] = true
	}
	if len(agents) == 0 {
		for _, r := range rows {
			if r.Agent != "" && r.Agent != "all" {
				agents[r.Agent] = true
			}
			if names, ok := r.Metadata["agents"].([]string); ok {
				for _, name := range names {
					agents[name] = true
				}
			}
			for _, a := range r.Agents {
				agents[a.Agent] = true
			}
		}
	}
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		names[i] = terminalText(agentLabel(name))
	}
	detected := strings.Join(names, ", ")
	if detected == "" {
		detected = "None"
	}
	return "Coding (Agent) CLI Usage Report - " + kind + "\nDetected: " + detected
}

func agentLabel(agent string) string {
	labels := map[string]string{"all": "All", "claude": "Claude", "codex": "Codex", "opencode": "OpenCode", "amp": "Amp", "droid": "Droid", "codebuff": "Codebuff", "hermes": "Hermes", "pi": "pi-agent", "goose": "Goose", "openclaw": "OpenClaw", "kilo": "Kilo", "copilot": "GitHub Copilot CLI", "gemini": "Gemini CLI", "antigravity": "Antigravity", "kimi": "Kimi", "qwen": "Qwen", "grok": "Grok", "zcode": "ZCode"}
	if label, ok := labels[agent]; ok {
		return label
	}
	return agent
}

func shortTableModel(model string) string {
	model = terminalText(model)
	model = strings.TrimPrefix(strings.TrimPrefix(model, "anthropic/claude-"), "claude-")
	parts := strings.Split(model, "-")
	if len(parts) >= 3 && len(parts[len(parts)-1]) == 8 {
		if _, err := time.Parse("20060102", parts[len(parts)-1]); err == nil {
			model = strings.Join(parts[:len(parts)-1], "-")
		}
	}
	return model
}

func tableModels(models []string) string {
	unique := map[string]bool{}
	for _, model := range models {
		if model != "" && model != "<synthetic>" {
			unique[shortTableModel(model)] = true
		}
	}
	lines := make([]string, 0, len(unique))
	for model := range unique {
		lines = append(lines, "- "+model)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func tableNumber(n int) string {
	s := strconv.Itoa(n)
	start := 0
	if n < 0 {
		start = 1
	}
	for i := len(s) - 3; i > start; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
