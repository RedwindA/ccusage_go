package calculator

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RedwindA/ccusage_go/internal/pricing"
	"github.com/RedwindA/ccusage_go/internal/types"
)

type Calculator struct {
	pricingService PricingService
	mode           string
	speed          string
}

type PricingService interface {
	// GetModelPrice returns per-token (or per-request) prices for a model.
	// webSearchPrice / webFetchPrice are per-request (server_tool_use billing).
	GetModelPrice(ctx context.Context, model string) (
		inputPrice, outputPrice, cacheCreatePrice, cacheReadPrice,
		webSearchPrice, webFetchPrice float64, err error,
	)
}

func New(pricingService PricingService) *Calculator {
	return &Calculator{
		pricingService: pricingService,
	}
}

// SetMode selects auto (recorded cost preferred), calculate, or display.
func (c *Calculator) SetMode(mode string) { c.mode = mode }

// SetSpeed overrides recorded speed when set to fast or standard.
func (c *Calculator) SetSpeed(speed string) { c.speed = speed }
func (c *Calculator) CalculateCosts(ctx context.Context, entries []types.UsageEntry) ([]types.UsageEntry, error) {
	for i := range entries {
		c.applyCost(ctx, &entries[i])
	}
	return entries, nil
}
func (c *Calculator) CalculateCost(entry *types.UsageEntry) error {
	c.applyCost(context.Background(), entry)
	return nil
}
func (c *Calculator) applyCost(ctx context.Context, entry *types.UsageEntry) {
	recorded := entry.HasCost || entry.Cost != 0
	if entry.Agent == "hermes" || entry.Agent == "opencode" {
		if entry.Cost > 0 {
			return
		}
		c.calculateSingleCost(ctx, entry)
		return
	}
	if c.mode == "display" {
		if !recorded {
			entry.Cost = 0
		}
		return
	}
	if c.mode != "calculate" && recorded {
		return
	}
	c.calculateSingleCost(ctx, entry)
}
func number(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}
func (c *Calculator) calculateSingleCost(ctx context.Context, entry *types.UsageEntry) {
	entry.Cost = 0
	delete(entry.Raw, "missing_pricing")
	delete(entry.Raw, "missing_pricing_model")
	entry.APICost = 0
	entry.CacheCreateCost = 0
	entry.CacheReadCost = 0
	entry.WebSearchCost = 0
	entry.WebFetchCost = 0
	var p pricing.ModelPricing
	var err error
	if source, ok := c.pricingService.(interface {
		GetSourcePricing(context.Context, string, string, any, time.Time) (pricing.ModelPricing, error)
	}); ok {
		at := entry.Timestamp
		if entry.Agent == "opencode" {
			aggregate, _ := entry.Raw["session_aggregate"].(bool)
			if aggregate || at.UnixMilli() == 0 {
				at = time.Time{}
			}
		}
		p, err = source.GetSourcePricing(ctx, entry.Model, entry.Agent, entry.Raw["provider"], at)
	} else if detailed, ok := c.pricingService.(interface {
		GetPricing(context.Context, string, time.Time) (pricing.ModelPricing, error)
	}); ok {
		// An exact raw model override (including an agent prefix) wins before aliases.
		model := entry.Model
		if overrideService, ok := c.pricingService.(interface{ HasOverride(string) bool }); ok && entry.Agent != "" && overrideService.HasOverride("["+entry.Agent+"] "+model) {
			model = "[" + entry.Agent + "] " + model
		}
		p, err = detailed.GetPricing(ctx, model, entry.Timestamp)
	} else {
		p.InputCostPerToken, p.OutputCostPerToken, p.CacheCreationInputTokenCost, p.CacheReadInputTokenCost, p.WebSearchCostPerRequest, p.WebFetchCostPerRequest, err = c.pricingService.GetModelPrice(ctx, entry.Model)
		p.FastMultiplier = 1
	}
	if err != nil {
		if entry.Model == "" || (entry.InputTokens+entry.OutputTokens+entry.CacheReadInputTokens+entry.CacheCreationInputTokens+entry.ReasoningTokens == 0 && number(entry.Raw["extra_output_tokens"]) == 0) {
			return
		}
		if entry.Raw == nil {
			entry.Raw = map[string]interface{}{}
		}
		entry.Raw["missing_pricing"] = true
		entry.Raw["missing_pricing_model"] = entry.Model
		return
	}
	create5, create1 := entry.CacheCreationInputTokens, 0
	raw := entry.Raw
	if message, ok := raw["message"].(map[string]interface{}); ok {
		if usage, ok := message["usage"].(map[string]interface{}); ok {
			raw = usage
		}
	}
	if usage, ok := raw["usage"].(map[string]interface{}); ok {
		raw = usage
	}
	if breakdown, ok := raw["cache_creation"].(map[string]interface{}); ok {
		create5 = int(number(breakdown["ephemeral_5m_input_tokens"]))
		create1 = int(number(breakdown["ephemeral_1h_input_tokens"]))
	}
	contextTokens := float64(entry.InputTokens) + float64(entry.CacheReadInputTokens) + float64(create5) + float64(create1)
	inputForCost := entry.InputTokens
	if p.CacheCreationAsInput {
		inputForCost += create5 + create1
		create5, create1 = 0, 0
	}
	charge := func(tokens int, base float64, above *float64) float64 {
		if tokens <= 0 {
			return 0
		}
		if above == nil {
			return float64(tokens) * base
		}
		if p.LongContextThreshold > 0 {
			if contextTokens > float64(p.LongContextThreshold) {
				return float64(tokens) * *above
			}
			return float64(tokens) * base
		}
		if tokens > 200000 {
			return 200000*base + float64(tokens-200000)**above
		}
		return float64(tokens) * base
	}
	speed := entry.Speed
	if (entry.Agent == "codex" || entry.Agent == "") && (c.speed == "fast" || c.speed == "standard") {
		speed = c.speed
	}

	multiplier := 1.0
	if speed == "fast" || speed == "priority" || (speed != "standard" && strings.HasSuffix(entry.Model, "-fast")) {
		multiplier = p.FastMultiplier
	}
	outputTokens := entry.OutputTokens
	if extra, exists := entry.Raw["extra_output_tokens"]; exists {
		outputTokens += int(number(extra))
	} else if extra, _ := entry.Raw["reasoning_is_extra"].(bool); extra {
		outputTokens += entry.ReasoningTokens
	}
	entry.APICost = (charge(inputForCost, p.InputCostPerToken, p.InputAbove) + charge(outputTokens, p.OutputCostPerToken, p.OutputAbove)) * multiplier
	var hourAbove *float64
	if p.InputAbove != nil {
		rate := *p.InputAbove * 2
		hourAbove = &rate
	}
	entry.CacheCreateCost = (charge(create5, p.CacheCreationInputTokenCost, p.CacheCreateAbove) + charge(create1, p.InputCostPerToken*2, hourAbove)) * multiplier
	entry.CacheReadCost = charge(entry.CacheReadInputTokens, p.CacheReadInputTokenCost, p.CacheReadAbove) * multiplier
	entry.WebSearchCost = float64(entry.WebSearchRequests) * p.WebSearchCostPerRequest
	entry.WebFetchCost = float64(entry.WebFetchRequests) * p.WebFetchCostPerRequest
	entry.Cost = entry.APICost + entry.CacheCreateCost + entry.CacheReadCost + entry.WebSearchCost + entry.WebFetchCost
}

// DetectCodexSpeed reads only the supplied Codex home roots. It deliberately
// ignores process-global environment so all-user scans retain user attribution.
func DetectCodexSpeed(homes []string) string {
	for _, home := range homes {
		data, err := os.ReadFile(filepath.Join(strings.TrimSpace(home), "config.toml"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line, _, _ = strings.Cut(line, "#")
			key, value, ok := strings.Cut(line, "=")
			if ok && strings.TrimSpace(key) == "service_tier" {
				value = strings.Trim(strings.TrimSpace(value), "\"'")
				if value == "fast" || value == "priority" {
					return "fast"
				}
			}
		}
	}
	return "standard"
}

func (c *Calculator) GenerateDailyReport(entries []types.UsageEntry, date time.Time) types.UsageReport {
	filteredEntries := c.filterByDate(entries, date)
	return c.generateReport(filteredEntries, "daily", date, date.Add(24*time.Hour))
}

func (c *Calculator) GenerateMonthlyReport(entries []types.UsageEntry, year int, month int) types.UsageReport {
	// Note: Since timezone conversion is now handled at the loader level via DateKey,
	// this method is primarily used for JSON/CSV output formats.
	// For table format, entries are already timezone-converted with DateKey set.
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)

	filteredEntries := c.filterByDateRange(entries, start, end)
	return c.generateReport(filteredEntries, "monthly", start, end)
}

func (c *Calculator) GenerateWeeklyReport(entries []types.UsageEntry, year int, week int) types.UsageReport {
	start := c.getWeekStart(year, week)
	end := start.Add(7 * 24 * time.Hour)

	filteredEntries := c.filterByDateRange(entries, start, end)
	return c.generateReport(filteredEntries, "weekly", start, end)
}

func (c *Calculator) GenerateSessionReport(entries []types.UsageEntry) []types.SessionInfo {
	sessionMap := make(map[string][]types.UsageEntry)

	// Group by project path instead of session ID (like TypeScript version)
	for _, entry := range entries {
		// Use project path as the grouping key
		projectKey := entry.ProjectPath
		if projectKey == "" {
			projectKey = "unknown"
		}
		sessionMap[projectKey] = append(sessionMap[projectKey], entry)
	}

	var sessions []types.SessionInfo
	for projectPath, sessionEntries := range sessionMap {
		if len(sessionEntries) == 0 {
			continue
		}

		sort.Slice(sessionEntries, func(i, j int) bool {
			return sessionEntries[i].Timestamp.Before(sessionEntries[j].Timestamp)
		})

		session := types.SessionInfo{
			SessionID:    projectPath, // Use project path as session ID for display
			StartTime:    sessionEntries[0].Timestamp,
			EndTime:      sessionEntries[len(sessionEntries)-1].Timestamp,
			RequestCount: len(sessionEntries),
			ProjectPath:  projectPath,
			LastActivity: sessionEntries[len(sessionEntries)-1].Timestamp, // Use last entry timestamp
		}

		session.Duration = session.EndTime.Sub(session.StartTime)

		// Track unique models, session IDs, and source files
		modelSet := make(map[string]bool)
		sessionIDSet := make(map[string]bool)
		sourceFileSet := make(map[string]bool)

		for _, entry := range sessionEntries {
			session.TotalCost += entry.Cost
			session.TotalAPICost += entry.APICost
			session.CacheCreateCost += entry.CacheCreateCost
			session.CacheReadCost += entry.CacheReadCost
			session.TotalTokens += entry.TotalTokens
			session.InputTokens += entry.InputTokens
			session.OutputTokens += entry.OutputTokens

			// Collect session name from first entry that has one
			if session.SessionName == "" && entry.SessionName != "" {
				session.SessionName = entry.SessionName
			}

			// Track unique session IDs
			if entry.SessionID != "" {
				sessionIDSet[entry.SessionID] = true
			}

			// Track unique source files
			if entry.SourceFile != "" {
				sourceFileSet[entry.SourceFile] = true
			}

			// Track models (exclude synthetic)
			if entry.Model != "" && entry.Model != "<synthetic>" {
				modelSet[entry.Model] = true
			}

			// Cache tokens come from first-class fields
			session.CacheCreationTokens += entry.CacheCreationInputTokens
			session.CacheReadTokens += entry.CacheReadInputTokens
		}

		// Convert model set to sorted slice
		for model := range modelSet {
			session.ModelsUsed = append(session.ModelsUsed, model)
		}
		sort.Strings(session.ModelsUsed)

		// Convert session ID set to sorted slice
		for sid := range sessionIDSet {
			session.SessionIDs = append(session.SessionIDs, sid)
		}
		sort.Strings(session.SessionIDs)

		// Convert source file set to sorted slice
		for sf := range sourceFileSet {
			session.SourceFiles = append(session.SourceFiles, sf)
		}
		sort.Strings(session.SourceFiles)

		sessions = append(sessions, session)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartTime.Before(sessions[j].StartTime)
	})

	return sessions
}

func (c *Calculator) AggregateBySourceFile(entries []types.UsageEntry) []types.SourceFileStat {
	fileMap := make(map[string]*types.SourceFileStat)

	for _, entry := range entries {
		if entry.SourceFile == "" {
			continue
		}

		stat, exists := fileMap[entry.SourceFile]
		if !exists {
			stat = &types.SourceFileStat{FilePath: entry.SourceFile}
			fileMap[entry.SourceFile] = stat
		}

		stat.InputTokens += entry.InputTokens
		stat.OutputTokens += entry.OutputTokens
		stat.Cost += entry.Cost
		stat.APICost += entry.APICost
		stat.CacheCreateCost += entry.CacheCreateCost
		stat.CacheReadCost += entry.CacheReadCost
		stat.EntryCount++

		stat.CacheCreateTokens += entry.CacheCreationInputTokens
		stat.CacheReadTokens += entry.CacheReadInputTokens

		// Total tokens
		stat.TotalTokens = stat.InputTokens + stat.OutputTokens + stat.CacheCreateTokens + stat.CacheReadTokens

		// Track models
		if entry.Model != "" && entry.Model != "<synthetic>" {
			found := false
			for _, m := range stat.ModelsUsed {
				if m == entry.Model {
					found = true
					break
				}
			}
			if !found {
				stat.ModelsUsed = append(stat.ModelsUsed, entry.Model)
			}
		}

		// Track last activity
		if entry.Timestamp.After(stat.LastActivity) {
			stat.LastActivity = entry.Timestamp
		}
	}

	var stats []types.SourceFileStat
	for _, stat := range fileMap {
		sort.Strings(stat.ModelsUsed)
		stats = append(stats, *stat)
	}
	sort.Slice(stats, func(i, j int) bool {
		return stats[i].FilePath < stats[j].FilePath
	})
	return stats
}

func (c *Calculator) GenerateBlocksReport(entries []types.UsageEntry) []types.BlockInfo {
	blockMap := make(map[string]*types.BlockInfo)

	for _, entry := range entries {
		if entry.BlockType == "" {
			continue
		}

		if block, exists := blockMap[entry.BlockType]; exists {
			block.Count++
			block.TotalTokens += entry.TotalTokens
			block.TotalCost += entry.Cost

			if entry.Timestamp.Before(block.FirstSeen) {
				block.FirstSeen = entry.Timestamp
			}
			if entry.Timestamp.After(block.LastSeen) {
				block.LastSeen = entry.Timestamp
			}
		} else {
			blockMap[entry.BlockType] = &types.BlockInfo{
				BlockType:   entry.BlockType,
				Count:       1,
				TotalTokens: entry.TotalTokens,
				TotalCost:   entry.Cost,
				FirstSeen:   entry.Timestamp,
				LastSeen:    entry.Timestamp,
			}
		}
	}

	var blocks []types.BlockInfo
	for _, block := range blockMap {
		blocks = append(blocks, *block)
	}

	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].Count > blocks[j].Count
	})

	return blocks
}

func (c *Calculator) filterByDate(entries []types.UsageEntry, date time.Time) []types.UsageEntry {
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	end := start.Add(24 * time.Hour)
	return c.filterByDateRange(entries, start, end)
}

func (c *Calculator) filterByDateRange(entries []types.UsageEntry, start, end time.Time) []types.UsageEntry {
	var filtered []types.UsageEntry
	for _, entry := range entries {
		// Include entries that are >= start and < end
		// This ensures we don't miss entries exactly at the start time
		if (entry.Timestamp.Equal(start) || entry.Timestamp.After(start)) && entry.Timestamp.Before(end) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func (c *Calculator) generateReport(entries []types.UsageEntry, period string, start, end time.Time) types.UsageReport {
	summary := c.calculateSummary(entries)

	return types.UsageReport{
		Period:      period,
		StartTime:   start,
		EndTime:     end,
		TotalCost:   summary.TotalCost,
		TotalTokens: summary.TotalTokens,
		Entries:     entries,
		Summary:     summary,
	}
}

func (c *Calculator) calculateSummary(entries []types.UsageEntry) types.UsageSummary {
	summary := types.UsageSummary{
		Models:   make(map[string]int),
		Projects: make(map[string]int),
	}

	for _, entry := range entries {
		summary.TotalRequests++
		summary.TotalCost += entry.Cost
		summary.TotalTokens += entry.TotalTokens
		summary.InputTokens += entry.InputTokens
		summary.OutputTokens += entry.OutputTokens

		// Skip synthetic model in statistics
		if entry.Model != "<synthetic>" {
			summary.Models[entry.Model]++
		}
		summary.Projects[entry.ProjectPath]++
	}

	if summary.TotalRequests > 0 {
		summary.AverageCost = summary.TotalCost / float64(summary.TotalRequests)
	}

	return summary
}

func (c *Calculator) getWeekStart(year, week int) time.Time {
	jan1 := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)

	// Find first Monday
	daysToMonday := (8 - int(jan1.Weekday())) % 7
	firstMonday := jan1.AddDate(0, 0, daysToMonday)

	// Week 1 starts on first Monday
	weekStart := firstMonday.AddDate(0, 0, (week-1)*7)

	return weekStart
}
