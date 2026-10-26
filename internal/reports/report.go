// Package reports builds all report formats from a single filtered set of usage.
package reports

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RedwindA/ccusage_go/internal/pricing"
	"github.com/RedwindA/ccusage_go/internal/types"
)

type Options struct {
	TerminalWidth                                                     int
	DetectedAgents                                                    []string
	Color                                                             bool
	Kind, Agent, Since, Until, Project, SessionID, SessionName, Order string
	Instances, ByAgent, NoCost, Breakdown, Compact                    bool
	Location                                                          *time.Location
	WeekStart                                                         time.Weekday
	// Sources holds the window of each source a unified report resolved
	// its own options for; other sources use Since, Until, Project and
	// Location.
	Sources map[string]Window
}

// Window is the date window, project and timezone a source's usage is
// filtered and grouped with.
type Window struct {
	Since, Until, Project string
	Location              *time.Location
}

func (o Options) window(agent string) Window {
	w, ok := o.Sources[agent]
	if !ok {
		w = Window{Since: o.Since, Until: o.Until, Project: o.Project, Location: o.Location}
	}
	if w.Location == nil {
		w.Location = time.Local
	}
	return w
}

type Row struct {
	APICost         float64        `json:"-"`
	CacheCreateCost float64        `json:"-"`
	CacheReadCost   float64        `json:"-"`
	Reasoning       int            `json:"reasoningOutputTokens,omitempty"`
	Period          string         `json:"period,omitempty"`
	Agent           string         `json:"agent,omitempty"`
	User            string         `json:"user,omitempty"`
	Project         string         `json:"project,omitempty"`
	Workspace       string         `json:"workspace,omitempty"`
	ProjectPath     string         `json:"projectPath,omitempty"`
	SessionName     string         `json:"sessionName,omitempty"`
	FirstActivity   string         `json:"firstActivity,omitempty"`
	LastActivity    string         `json:"lastActivity,omitempty"`
	Models          []string       `json:"modelsUsed"`
	Input           int            `json:"inputTokens"`
	Output          int            `json:"outputTokens"`
	CacheCreate     int            `json:"cacheCreationTokens"`
	CacheRead       int            `json:"cacheReadTokens"`
	Total           int            `json:"totalTokens"`
	Cost            float64        `json:"totalCost"`
	Credits         float64        `json:"credits,omitempty"`
	Requests        int            `json:"requestCount"`
	Breakdowns      []Model        `json:"modelBreakdowns"`
	Agents          []*Row         `json:"agents,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

type Model struct {
	APICost         float64 `json:"-"`
	CacheCreateCost float64 `json:"-"`
	CacheReadCost   float64 `json:"-"`
	Reasoning       int     `json:"reasoningOutputTokens,omitempty"`
	IsFallback      bool    `json:"isFallback,omitempty"`
	Name            string  `json:"modelName"`
	Input           int     `json:"inputTokens"`
	Output          int     `json:"outputTokens"`
	CacheCreate     int     `json:"cacheCreationTokens"`
	CacheRead       int     `json:"cacheReadTokens"`
	Total           int     `json:"totalTokens"`
	Cost            float64 `json:"cost"`
	MissingPricing  bool    `json:"missingPricing,omitempty"`
	// StoredCost is the part of Cost that auto mode took from recorded
	// costs, nil when no entry used one.
	StoredCost *float64 `json:"storedCost,omitempty"`
}

type accumulator struct {
	row         Row
	models      map[string]*Model
	agents      map[string]*accumulator
	first, last time.Time
}

func NormalizeDate(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	layout := "2006-01-02"
	if len(s) == 8 {
		layout = "20060102"
	}
	t, err := time.Parse(layout, s)
	if err != nil || t.Year() < 1 {
		return "", fmt.Errorf("invalid date %q: use YYYY-MM-DD or YYYYMMDD", s)
	}
	return t.Format("2006-01-02"), nil
}

func WeekStart(t time.Time, start time.Weekday) time.Time {
	t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return t.AddDate(0, 0, -(int(t.Weekday())-int(start)+7)%7)
}

func LastSince(kind string, n int, now time.Time, start time.Weekday) (string, error) {
	if n < 1 {
		return "", fmt.Errorf("--last must be positive")
	}
	switch kind {
	case "daily":
		now = now.AddDate(0, 0, -(n - 1))
	case "weekly":
		now = WeekStart(now, start).AddDate(0, 0, -7*(n-1))
	case "monthly":
		now = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, -(n - 1), 0)
	default:
		return "", fmt.Errorf("--last is only available for daily, weekly and monthly reports")
	}
	return now.Format("2006-01-02"), nil
}

// Native sources disagree on the scope of a session date bound. Qwen and
// unified Claude select complete sessions by latest activity; the other
// adapters (including focused Claude) select usage events before grouping.
func wholeSessionDate(e types.UsageEntry, o Options) bool {
	return o.Kind == "session" && (e.Agent == "qwen" || e.Agent == "claude" && o.Agent == "")
}
func sessionGroupKey(e types.UsageEntry, o Options) string {
	user, _ := e.Raw["system_user"].(string)
	key := e.SessionID + "\x00" + user + "\x00" + e.Agent
	if e.Agent == "claude" || e.Agent == "pi" && o.Agent == "" {
		key += "\x00" + projectKey(e)
	}
	return key
}

// projectKey names the project an entry is grouped under. Upstream names a
// Claude project by its directory under projects/, so one found under two
// Claude roots, such as one config directory per account, is one project.
func projectKey(e types.UsageEntry) string {
	if e.Agent == "claude" {
		return filepath.Base(e.ProjectPath)
	}
	return e.ProjectPath
}
func activityTime(v any) time.Time {
	switch value := v.(type) {
	case time.Time:
		return value
	case string:
		t, _ := time.Parse(time.RFC3339Nano, value)
		return t
	case int64:
		return time.UnixMilli(value)
	case float64:
		return time.UnixMilli(int64(value))
	}
	return time.Time{}
}
func activityBounds(e types.UsageEntry) (time.Time, time.Time) {
	first, last := e.Timestamp, e.Timestamp
	if t := activityTime(e.Raw["first_activity"]); !t.IsZero() && t.Before(first) {
		first = t
	}
	if t := activityTime(e.Raw["last_activity"]); !t.IsZero() && t.After(last) {
		last = t
	}
	return first, last
}
func Filter(entries []types.UsageEntry, o Options) []types.UsageEntry {
	candidates := make([]types.UsageEntry, 0, len(entries))
	latest := map[string]time.Time{}
	for _, e := range entries {
		if e.Agent == "" && o.Agent != "" {
			e.Agent = o.Agent
		}
		if o.Agent != "" && o.Agent != "all" && e.Agent != o.Agent {
			continue
		}
		w := o.window(e.Agent)
		if aggregate, _ := e.Raw["session_aggregate"].(bool); aggregate && (o.Kind != "session" || w.Since != "" || w.Until != "") {
			continue
		}
		if w.Project != "" && e.ProjectPath != w.Project && filepath.Base(e.ProjectPath) != w.Project {
			continue
		}
		if o.SessionID != "" && e.SessionID != o.SessionID && e.RecordedSessionID != o.SessionID {
			continue
		}
		if o.SessionName != "" && e.SessionName != o.SessionName {
			continue
		}
		candidates = append(candidates, e)
		if wholeSessionDate(e, o) {
			_, last := activityBounds(e)
			key := sessionGroupKey(e, o)
			if last.After(latest[key]) {
				latest[key] = last
			}
		}
	}
	out := make([]types.UsageEntry, 0, len(candidates))
	for _, e := range candidates {
		w := o.window(e.Agent)
		if wholeSessionDate(e, o) {
			if wholeSessionInWindow(latest[sessionGroupKey(e, o)], w) {
				out = append(out, e)
			}
			continue
		}
		date := e.Timestamp.In(w.Location).Format("2006-01-02")
		if w.Since != "" && date < w.Since || w.Until != "" && date > w.Until {
			continue
		}
		out = append(out, e)
	}
	return out
}

// wholeSessionInWindow reports whether a session last active at last falls
// in w. Upstream compares the UTC RFC3339 last activity, dashes stripped,
// with the YYYYMMDD bounds as strings, so the bounds are UTC dates whatever
// the timezone, and a session last active on the --until day sorts after it
// and drops.
func wholeSessionInWindow(last time.Time, w Window) bool {
	date := last.UTC().Format("2006-01-02")
	return (w.Since == "" || date >= w.Since) && (w.Until == "" || date < w.Until)
}

func displayModel(e types.UsageEntry) string {
	model := e.Model
	if model == "" {
		model = "unknown"
	}
	// Claude's Fast usage is its own <model>-fast model, as upstream's loader
	// names it, even when model already ends in -fast. A <synthetic> entry
	// has no model to name.
	if e.Agent == "claude" && e.Speed == "fast" && model != "<synthetic>" {
		model += "-fast"
	}
	format, _ := e.Raw["source_format"].(string)
	if (e.Agent == "pi" || format == "pi" || e.Agent == "openclaw") && !strings.HasPrefix(model, "[") {
		model = "[" + e.Agent + "] " + model
	}
	// The Codex loader already names a model by its alias.
	if e.Agent == "codex" {
		return model
	}
	return pricing.ResolveModelName(model)
}

func Aggregate(entries []types.UsageEntry, o Options) []*Row {
	groups := map[string]*accumulator{}
	entries = Filter(entries, o)
	if o.Agent == "" && (o.Kind == "daily" || o.Kind == "weekly" || o.Kind == "monthly") {
		entries = dropEmptyAgentDays(entries, o)
	}
	for _, e := range entries {
		e.Model = displayModel(e)
		t := e.Timestamp.In(o.window(e.Agent).Location)
		var period string
		switch o.Kind {
		case "monthly":
			period = t.Format("2006-01")
		case "weekly":
			period = WeekStart(t, o.WeekStart).Format("2006-01-02")
		case "session":
			period = e.SessionID
		case "model":
			period = e.Model
			// A Claude <synthetic> entry, such as an API error, has no
			// model: its usage, if any, is unknown.
			if period == "<synthetic>" {
				if entryTokens(e) == 0 && e.Cost == 0 {
					continue
				}
				period = "unknown"
			}
		case "workspace":
			period = e.Workspace
			if strings.TrimSpace(period) == "" {
				period = "unknown"
			}
		default:
			period = t.Format("2006-01-02")
		}
		if period == "" {
			period = "unknown"
		}
		user, _ := e.Raw["system_user"].(string)
		key := period + "\x00" + user
		project := ""
		if o.Instances {
			project = filepath.Base(e.ProjectPath)
			key += "\x00" + projectKey(e)
		}
		// Sessions from different sources must remain independent.
		if o.Kind == "session" {
			key = sessionGroupKey(e, o)
		}
		a := groups[key]
		if a == nil {
			agent := o.Agent
			if agent == "" {
				agent = "all"
			}
			if o.Kind == "session" {
				agent = e.Agent
			}
			a = &accumulator{row: Row{Period: period, Agent: agent, User: user, Project: project}, models: map[string]*Model{}, agents: map[string]*accumulator{}}
			groups[key] = a
		}
		a.add(e)
		if o.Agent == "" && o.Kind != "session" {
			sub := a.agents[e.Agent]
			if sub == nil {
				sub = &accumulator{row: Row{Period: period, Agent: e.Agent, User: user, Project: project}, models: map[string]*Model{}}
				a.agents[e.Agent] = sub
			}
			sub.add(e)
		}
	}
	rows := make([]*Row, 0, len(groups))
	for _, a := range groups {
		row := a.finish()
		// Cost-only rows, such as Copilot credits missing from modelMetrics,
		// still bill.
		if row.Total == 0 && (o.Agent == "" && row.Cost == 0 || o.Agent == "claude" && o.Kind == "session") {
			continue
		}
		names := []string{}
		for name, sub := range a.agents {
			names = append(names, name)
			if o.ByAgent {
				row.Agents = append(row.Agents, sub.finish())
			}
		}
		sort.Strings(names)
		sort.Slice(row.Agents, func(i, j int) bool { return row.Agents[i].Agent < row.Agents[j].Agent })
		if len(names) > 0 {
			row.Metadata = map[string]any{"agents": names}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (o.Kind == "model" || o.Kind == "workspace" || o.Kind == "session" && (o.Agent == "" || o.Agent == "claude")) && a.Cost != b.Cost {
			if o.Order == "asc" && !(o.Kind == "session" && o.Agent == "claude") {
				return a.Cost < b.Cost
			}
			return a.Cost > b.Cost
		}
		ka, kb := a.Period+"\x00"+a.User+"\x00"+a.Project+"\x00"+a.Agent+"\x00"+a.ProjectPath, b.Period+"\x00"+b.User+"\x00"+b.Project+"\x00"+b.Agent+"\x00"+b.ProjectPath
		if o.Order == "desc" && o.Kind != "model" && o.Kind != "workspace" && (o.Kind != "session" || o.Agent != "") {
			return ka > kb
		}
		return ka < kb
	})
	return rows
}

// Unified daily, weekly and monthly reports merge each agent's daily usage,
// and upstream drops an agent's day with neither tokens nor cost before
// merging. A Copilot credit row that --mode calculate prices at $0 must not
// list Copilot or its unknown model on a day another agent used.
func dropEmptyAgentDays(entries []types.UsageEntry, o Options) []types.UsageEntry {
	key := func(e types.UsageEntry) string {
		user, _ := e.Raw["system_user"].(string)
		k := e.Timestamp.In(o.window(e.Agent).Location).Format("2006-01-02") + "\x00" + user + "\x00" + e.Agent
		if o.Instances {
			k += "\x00" + projectKey(e)
		}
		return k
	}
	used := map[string]bool{}
	for _, e := range entries {
		if entryTokens(e) != 0 || e.Cost != 0 {
			used[key(e)] = true
		}
	}
	out := make([]types.UsageEntry, 0, len(entries))
	for _, e := range entries {
		if used[key(e)] {
			out = append(out, e)
		}
	}
	return out
}

func entryTokens(e types.UsageEntry) int {
	if e.TotalTokens != 0 {
		return e.TotalTokens
	}
	return e.InputTokens + e.OutputTokens + e.CacheCreationInputTokens + e.CacheReadInputTokens
}

func (a *accumulator) add(e types.UsageEntry) {
	r := &a.row
	r.Input += e.InputTokens
	r.Output += e.OutputTokens
	r.Reasoning += e.ReasoningTokens
	r.CacheCreate += e.CacheCreationInputTokens
	r.CacheRead += e.CacheReadInputTokens
	total := entryTokens(e)
	r.Total += total
	r.Cost += e.Cost
	r.APICost += e.APICost
	r.CacheCreateCost += e.CacheCreateCost
	r.CacheReadCost += e.CacheReadCost
	r.Credits += e.Credits
	requests := 1
	switch count := e.Raw["request_count"].(type) {
	case int:
		requests = count
	case float64:
		requests = int(count)
	}
	r.Requests += requests
	first, last := activityBounds(e)
	if a.first.IsZero() || first.Before(a.first) {
		a.first = first
	}
	if a.last.IsZero() || last.After(a.last) {
		a.last = last
		r.Workspace = e.Workspace
		r.ProjectPath = e.ProjectPath
		r.SessionName = e.SessionName
	}
	// Like upstream, a Claude <synthetic> entry, such as an API error, has
	// no model: it counts toward the row but none of its models.
	if e.Model == "<synthetic>" {
		return
	}
	name := e.Model
	if name == "" {
		name = "unknown"
	}
	m := a.models[name]
	if m == nil {
		m = &Model{Name: name}
		a.models[name] = m
	}
	m.Input += e.InputTokens
	m.Output += e.OutputTokens
	m.Reasoning += e.ReasoningTokens
	if fallback, _ := e.Raw["is_fallback"].(bool); fallback {
		m.IsFallback = true
	}
	m.CacheCreate += e.CacheCreationInputTokens
	m.CacheRead += e.CacheReadInputTokens
	m.Total += total
	m.Cost += e.Cost
	m.APICost += e.APICost
	m.CacheCreateCost += e.CacheCreateCost
	m.CacheReadCost += e.CacheReadCost
	if missing, _ := e.Raw["missing_pricing"].(bool); missing {
		m.MissingPricing = true
	}
	if e.UsesStoredCost {
		stored := e.Cost
		if m.StoredCost != nil {
			stored += *m.StoredCost
		}
		m.StoredCost = &stored
	}
}

func (a *accumulator) finish() *Row {
	r := &a.row
	r.Models = []string{}
	r.Breakdowns = []Model{}
	for name, m := range a.models {
		r.Models = append(r.Models, name)
		r.Breakdowns = append(r.Breakdowns, *m)
	}
	sort.Strings(r.Models)
	sort.Slice(r.Breakdowns, func(i, j int) bool {
		a, b := r.Breakdowns[i], r.Breakdowns[j]
		if a.Cost != b.Cost {
			return a.Cost > b.Cost
		}
		return a.Name < b.Name
	})
	r.FirstActivity = a.first.Format(time.RFC3339Nano)
	r.LastActivity = a.last.Format(time.RFC3339Nano)
	return r
}

func Totals(rows []*Row) map[string]any {
	r := Row{}
	missing := map[string]bool{}
	for _, row := range rows {
		r.Input += row.Input
		r.Output += row.Output
		r.CacheCreate += row.CacheCreate
		r.CacheRead += row.CacheRead
		r.Total += row.Total
		r.Cost += row.Cost
		r.Credits += row.Credits
		r.Requests += row.Requests
		for _, model := range row.Breakdowns {
			if model.MissingPricing {
				missing[model.Name] = true
			}
		}
	}
	result := map[string]any{"inputTokens": r.Input, "outputTokens": r.Output, "cacheCreationTokens": r.CacheCreate, "cacheReadTokens": r.CacheRead, "totalTokens": r.Total, "totalCost": r.Cost, "requestCount": r.Requests}
	if r.Credits != 0 {
		result["credits"] = r.Credits
	}
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for name := range missing {
			names = append(names, name)
		}
		sort.Strings(names)
		result["unpricedModels"] = names
	}
	return result
}
