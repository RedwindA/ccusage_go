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

// item is an entry as reports filter and group it. agent is the entry's,
// or the report's for an entry without one; the other fields hold what the
// reports read from its Raw, read once, since Raw maps are scattered over
// the heap and each pass over them misses the cache again.
type item struct {
	e                                     *types.UsageEntry
	agent, user, format                   string
	aggregate, advisor, fallback, missing bool
	requests                              int
	first, last                           time.Time
	window                                Window
	// day is the entry's date in its window's location, and session its
	// sessionGroupKey, once built.
	day, session string
}

func newItems(entries []types.UsageEntry, o Options) []item {
	items := make([]item, len(entries))
	for i := range entries {
		e, it := &entries[i], &items[i]
		it.e, it.agent = e, e.Agent
		if it.agent == "" && o.Agent != "" {
			it.agent = o.Agent
		}
		it.window = o.window(it.agent)
		it.requests = 1
		// One walk over a small Raw beats a lookup of each field.
		var first, last any
		for key, v := range e.Raw {
			switch key {
			case "system_user":
				it.user, _ = v.(string)
			case "source_format":
				it.format, _ = v.(string)
			case "session_aggregate":
				it.aggregate, _ = v.(bool)
			case "advisor":
				it.advisor = v != nil
			case "is_fallback":
				it.fallback, _ = v.(bool)
			case "missing_pricing":
				it.missing, _ = v.(bool)
			case "request_count":
				switch count := v.(type) {
				case int:
					it.requests = count
				case float64:
					it.requests = int(count)
				}
			case "first_activity":
				first = v
			case "last_activity":
				last = v
			}
		}
		it.first, it.last = activityBounds(e.Timestamp, first, last)
	}
	return items
}

// date returns the item's date in its window's location.
func (it *item) date() string {
	if it.day == "" {
		it.day = formatDay(it.e.Timestamp.In(it.window.Location))
	}
	return it.day
}

// formatDay is t.Format("2006-01-02") without parsing the layout.
func formatDay(t time.Time) string {
	y, m, d := t.Date()
	if y < 0 || y > 9999 {
		return t.Format("2006-01-02")
	}
	b := [10]byte{byte('0' + y/1000), byte('0' + y/100%10), byte('0' + y/10%10), byte('0' + y%10), '-', byte('0' + m/10), byte('0' + m%10), '-', byte('0' + d/10), byte('0' + d%10)}
	return string(b[:])
}

// Native sources disagree on the scope of a session date bound. Qwen and
// unified Claude select complete sessions by latest activity; the other
// adapters (including focused Claude) select usage events before grouping.
func wholeSessionDate(agent string, o Options) bool {
	return o.Kind == "session" && (agent == "qwen" || agent == "claude" && o.Agent == "")
}
func sessionGroupKey(it *item, o Options) string {
	if it.session == "" {
		it.session = it.e.SessionID + "\x00" + it.user + "\x00" + it.agent
		if it.agent == "claude" || it.agent == "pi" && o.Agent == "" {
			it.session += "\x00" + projectKey(it.agent, it.e.ProjectPath)
		}
	}
	return it.session
}

// projectKey names the project an entry is grouped under. Upstream names a
// Claude project by its directory under projects/, so one found under two
// Claude roots, such as one config directory per account, is one project.
func projectKey(agent, projectPath string) string {
	if agent == "claude" {
		return filepath.Base(projectPath)
	}
	return projectPath
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

// activityBounds widens an entry's timestamp to the first and last activity
// its Raw records.
func activityBounds(at time.Time, firstActivity, lastActivity any) (time.Time, time.Time) {
	first, last := at, at
	if t := activityTime(firstActivity); !t.IsZero() && t.Before(first) {
		first = t
	}
	if t := activityTime(lastActivity); !t.IsZero() && t.After(last) {
		last = t
	}
	return first, last
}
func Filter(entries []types.UsageEntry, o Options) []types.UsageEntry {
	items := filter(newItems(entries, o), o)
	out := make([]types.UsageEntry, len(items))
	for i := range items {
		out[i] = *items[i].e
		out[i].Agent = items[i].agent
	}
	return out
}

// filter keeps the items Filter keeps, reusing items.
func filter(items []item, o Options) []item {
	candidates := items[:0]
	latest := map[string]time.Time{}
	for i := range items {
		it := &items[i]
		if o.Agent != "" && o.Agent != "all" && it.agent != o.Agent {
			continue
		}
		w := it.window
		if it.aggregate && (o.Kind != "session" || w.Since != "" || w.Until != "") {
			continue
		}
		if w.Project != "" && it.e.ProjectPath != w.Project && filepath.Base(it.e.ProjectPath) != w.Project {
			continue
		}
		if o.SessionID != "" && it.e.SessionID != o.SessionID && it.e.RecordedSessionID != o.SessionID {
			continue
		}
		if o.SessionName != "" && it.e.SessionName != o.SessionName {
			continue
		}
		candidates = append(candidates, *it)
		if wholeSessionDate(it.agent, o) {
			key := sessionGroupKey(it, o)
			if it.last.After(latest[key]) {
				latest[key] = it.last
			}
		}
	}
	out := candidates[:0]
	for i := range candidates {
		it := &candidates[i]
		w := it.window
		if wholeSessionDate(it.agent, o) {
			if wholeSessionInWindow(latest[sessionGroupKey(it, o)], w) {
				out = append(out, *it)
			}
			continue
		}
		if w.Since != "" || w.Until != "" {
			if date := it.date(); w.Since != "" && date < w.Since || w.Until != "" && date > w.Until {
				continue
			}
		}
		out = append(out, *it)
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

// modelNames memoizes pricing.ResolveModelName over a report's entries,
// which name few models.
type modelNames map[string]string

func (names modelNames) displayModel(it *item) string {
	model := it.e.Model
	if model == "" {
		model = "unknown"
	}
	// Claude's Fast usage is its own <model>-fast model, as upstream's loader
	// names it, even when model already ends in -fast. A <synthetic> entry
	// has no model to name, and an advisor call keeps its model as written.
	if it.agent == "claude" && it.e.Speed == "fast" && model != "<synthetic>" && !it.advisor {
		model += "-fast"
	}
	if (it.agent == "pi" || it.format == "pi" || it.agent == "openclaw") && !strings.HasPrefix(model, "[") {
		model = "[" + it.agent + "] " + model
	}
	// The Codex loader already names a model by its alias.
	if it.agent == "codex" {
		return model
	}
	name, ok := names[model]
	if !ok {
		name = pricing.ResolveModelName(model)
		names[model] = name
	}
	return name
}

func Aggregate(entries []types.UsageEntry, o Options) []*Row {
	groups := map[string]*accumulator{}
	items := filter(newItems(entries, o), o)
	if o.Agent == "" && (o.Kind == "daily" || o.Kind == "weekly" || o.Kind == "monthly") {
		items = dropEmptyAgentDays(items, o)
	}
	names := modelNames{}
	for i := range items {
		it := &items[i]
		e := it.e
		model := names.displayModel(it)
		var period string
		switch o.Kind {
		case "monthly":
			period = e.Timestamp.In(it.window.Location).Format("2006-01")
		case "weekly":
			period = WeekStart(e.Timestamp.In(it.window.Location), o.WeekStart).Format("2006-01-02")
		case "session":
			period = e.SessionID
		case "model":
			period = model
			// A Claude <synthetic> entry, such as an API error, has no
			// model: its usage, if any, is unknown.
			if period == "<synthetic>" {
				if entryTokens(it) == 0 && e.Cost == 0 {
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
			period = it.date()
		}
		if period == "" {
			period = "unknown"
		}
		key := period + "\x00" + it.user
		project := ""
		if o.Instances {
			project = filepath.Base(e.ProjectPath)
			key += "\x00" + projectKey(it.agent, e.ProjectPath)
		}
		// Sessions from different sources must remain independent.
		if o.Kind == "session" {
			key = sessionGroupKey(it, o)
		}
		a := groups[key]
		if a == nil {
			agent := o.Agent
			if agent == "" {
				agent = "all"
			}
			if o.Kind == "session" {
				agent = it.agent
			}
			a = &accumulator{row: Row{Period: period, Agent: agent, User: it.user, Project: project}, models: map[string]*Model{}, agents: map[string]*accumulator{}}
			groups[key] = a
		}
		a.add(it, model)
		if o.Agent == "" && o.Kind != "session" {
			sub := a.agents[it.agent]
			if sub == nil {
				sub = &accumulator{row: Row{Period: period, Agent: it.agent, User: it.user, Project: project}, models: map[string]*Model{}}
				a.agents[it.agent] = sub
			}
			sub.add(it, model)
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
func dropEmptyAgentDays(items []item, o Options) []item {
	keys := make([]string, len(items))
	used := map[string]bool{}
	for i := range items {
		it := &items[i]
		keys[i] = it.date() + "\x00" + it.user + "\x00" + it.agent
		if o.Instances {
			keys[i] += "\x00" + projectKey(it.agent, it.e.ProjectPath)
		}
		if entryTokens(it) != 0 || it.e.Cost != 0 {
			used[keys[i]] = true
		}
	}
	out := items[:0]
	for i := range items {
		if used[keys[i]] {
			out = append(out, items[i])
		}
	}
	return out
}

// entryTokens is an entry's total, the sum of its tokens when it records
// none. Codex's is the total it records even when that is zero, as upstream
// counts it.
func entryTokens(it *item) int {
	e := it.e
	if e.TotalTokens != 0 || it.agent == "codex" {
		return e.TotalTokens
	}
	return e.InputTokens + e.OutputTokens + e.CacheCreationInputTokens + e.CacheReadInputTokens
}

// add counts it, named model, toward a.
func (a *accumulator) add(it *item, model string) {
	e := it.e
	r := &a.row
	r.Input += e.InputTokens
	r.Output += e.OutputTokens
	r.Reasoning += e.ReasoningTokens
	r.CacheCreate += e.CacheCreationInputTokens
	r.CacheRead += e.CacheReadInputTokens
	total := entryTokens(it)
	r.Total += total
	r.Cost += e.Cost
	r.APICost += e.APICost
	r.CacheCreateCost += e.CacheCreateCost
	r.CacheReadCost += e.CacheReadCost
	r.Credits += e.Credits
	r.Requests += it.requests
	if a.first.IsZero() || it.first.Before(a.first) {
		a.first = it.first
	}
	if a.last.IsZero() || it.last.After(a.last) {
		a.last = it.last
		r.Workspace = e.Workspace
		r.ProjectPath = e.ProjectPath
		r.SessionName = e.SessionName
	}
	// Like upstream, a Claude <synthetic> entry, such as an API error, has
	// no model: it counts toward the row but none of its models.
	if model == "<synthetic>" {
		return
	}
	name := model
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
	if it.fallback {
		m.IsFallback = true
	}
	m.CacheCreate += e.CacheCreationInputTokens
	m.CacheRead += e.CacheReadInputTokens
	m.Total += total
	m.Cost += e.Cost
	m.APICost += e.APICost
	m.CacheCreateCost += e.CacheCreateCost
	m.CacheReadCost += e.CacheReadCost
	if it.missing {
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
