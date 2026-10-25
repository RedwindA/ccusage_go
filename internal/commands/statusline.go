package commands

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/RedwindA/ccusage_go/internal/calculator"
	"github.com/RedwindA/ccusage_go/internal/config"
	"github.com/RedwindA/ccusage_go/internal/pricing"
	"github.com/RedwindA/ccusage_go/internal/types"
	"github.com/spf13/cobra"
)

type statuslineFlags struct {
	offline, noOffline, cache, noCache, debug              bool
	visual, costSource, timezone, configPath, modelAliases string
	refresh                                                uint64
	low, medium                                            int
}
type statuslineHook struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Model          struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Cost *struct {
		Total float64 `json:"total_cost_usd"`
	} `json:"cost"`
	Context *struct {
		Input   int `json:"total_input_tokens"`
		Limit   int `json:"context_window_size"`
		Current *struct {
			Input  int `json:"input_tokens"`
			Read   int `json:"cache_read_input_tokens"`
			Create int `json:"cache_creation_input_tokens"`
		} `json:"current_usage"`
	} `json:"context_window"`
	Effort *struct {
		Level string `json:"level"`
	} `json:"effort"`
}
type statuslineCache struct {
	Updating bool      `json:"isUpdating,omitempty"`
	PID      int       `json:"pid,omitempty"`
	Output   string    `json:"output"`
	Updated  time.Time `json:"updated"`
	Mtime    int64     `json:"mtime"`
	Size     int64     `json:"size"`
}

func NewStatuslineCommand() *cobra.Command {
	f := &statuslineFlags{}
	cmd := &cobra.Command{Use: "statusline", Short: "Display compact status for Claude Code hooks", Args: cobra.NoArgs}
	p := cmd.Flags()
	p.BoolVarP(&f.offline, "offline", "O", true, "Use embedded model pricing")
	p.BoolVar(&f.noOffline, "no-offline", false, "Refresh pricing from the network")
	p.BoolVar(&f.cache, "cache", true, "Cache status output")
	p.BoolVar(&f.noCache, "no-cache", false, "Disable status output cache")
	p.StringVarP(&f.visual, "visual-burn-rate", "B", "off", "Burn indicator: off, emoji, text, emoji-text")
	p.StringVar(&f.costSource, "cost-source", "auto", "Session cost source: auto, ccusage, cc, both")
	p.Uint64Var(&f.refresh, "refresh-interval", 1, "Cache refresh interval in seconds")
	p.IntVar(&f.low, "context-low-threshold", 50, "Low context percentage threshold")
	p.IntVar(&f.medium, "context-medium-threshold", 80, "Medium context percentage threshold")
	p.StringVarP(&f.timezone, "timezone", "z", "", "Date grouping timezone")
	p.StringVar(&f.configPath, "config", "", "Path to JSON config")
	p.BoolVarP(&f.debug, "debug", "d", false, "Show load diagnostics")
	p.StringVar(&f.modelAliases, "model-label-aliases", "", "Model display aliases as a JSON object")
	completeValues(cmd, "visual-burn-rate", "off", "emoji", "text", "emoji-text")
	completeValues(cmd, "cost-source", "auto", "ccusage", "cc", "both")
	completeJSONFile(cmd, "config")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error { return runStatusline(cmd, f) }
	return cmd
}
func runStatusline(cmd *cobra.Command, f *statuslineFlags) error {
	cfg, err := config.Load(f.configPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate("statusline", "claude"); err != nil {
		return err
	}
	options := cfg.Options("statusline", "claude")
	for key, value := range options {
		name := optionFlag(key)
		flag := cmd.Flags().Lookup(name)
		if flag == nil || flag.Changed {
			continue
		}
		text := fmt.Sprint(value)
		if name == "model-label-aliases" {
			raw, _ := json.Marshal(value)
			text = string(raw)
		}
		if err := flag.Value.Set(text); err != nil {
			return fmt.Errorf("config %s: %w", name, err)
		}
	}
	if f.low < 0 || f.medium > 100 || f.low >= f.medium {
		return fmt.Errorf("context thresholds must satisfy 0 <= low < medium <= 100")
	}
	if !strings.Contains(",off,emoji,text,emoji-text,", ","+f.visual+",") {
		return fmt.Errorf("invalid visual burn rate %q", f.visual)
	}
	if !strings.Contains(",auto,ccusage,cc,both,", ","+f.costSource+",") {
		return fmt.Errorf("invalid cost source %q", f.costSource)
	}
	aliases := map[string]string{}
	if f.modelAliases != "" {
		if err := json.Unmarshal([]byte(f.modelAliases), &aliases); err != nil {
			return fmt.Errorf("model-label-aliases must be a JSON object: %w", err)
		}
	}
	loc := time.Local
	if f.timezone != "" {
		loc, err = time.LoadLocation(f.timezone)
		if err != nil {
			return err
		}
	}
	data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 4<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return fmt.Errorf("no input provided")
	}
	var hook statuslineHook
	if err = json.Unmarshal(data, &hook); err != nil {
		return fmt.Errorf("invalid statusline input: %w", err)
	}
	if hook.SessionID == "" || hook.Model.DisplayName == "" {
		return fmt.Errorf("statusline input requires session_id and model.display_name")
	}
	now := time.Now()
	mtime, size := statuslineTranscriptStamp(hook.TranscriptPath)
	keyData, _ := json.Marshal([]any{hook, options, f.offline, f.noOffline, f.visual, f.costSource, f.timezone, f.low, f.medium, aliases})
	key := fmt.Sprintf("%x", sha256.Sum256(keyData))
	cacheDir, cacheErr := os.UserCacheDir()
	cachePath := filepath.Join(cacheDir, "ccusage-go", "statusline", key+".json")
	useCache := f.cache && !f.noCache && cacheErr == nil
	var previous statuslineCache
	if useCache {
		if raw, e := os.ReadFile(cachePath); e == nil {
			if json.Unmarshal(raw, &previous) == nil {
				if output, ok := cachedStatuslineOutput(previous, mtime, size, now, f.refresh); ok {
					fmt.Fprintln(cmd.OutOrStdout(), output)
					return nil
				}
			}
		}
		writeStatuslineCache(cachePath, statuslineCache{Output: previous.Output, Updated: previous.Updated, Mtime: mtime, Size: size, Updating: true, PID: os.Getpid()})
	}

	offline := f.offline && !f.noOffline
	prefetch := startPricing(cmd, offline)
	home, _ := os.UserHomeDir()
	paths := sourcePaths("claude", home, true)
	service := newPricingService(offline, prefetch)
	service.SetOverrides(cfg.PricingOverrides("statusline", "claude"))
	calc := calculator.New(service)
	var entries []types.UsageEntry
	var block *types.SessionBlock
	var loadErr error
	if statuslineShowsUsageCost(hook, f) {
		// A session can run over any span of history.
		entries, loadErr = loadSource(cmd.Context(), "claude", paths, loc, f.debug, false)
		entries, _ = calc.CalculateCosts(cmd.Context(), entries)
		block = activeStatuslineBlock(calc, entries)
	} else {
		entries, block, loadErr = loadStatuslineUsage(cmd.Context(), paths, loc, now, f.debug, calc)
	}
	if loadErr != nil && f.debug {
		fmt.Fprintf(cmd.ErrOrStderr(), "statusline: %v\n", loadErr)
	}
	output := renderStatusline(hook, f, aliases, entries, block, loadErr == nil, loc, now, service)
	fmt.Fprintln(cmd.OutOrStdout(), output)
	if useCache {
		writeStatuslineCache(cachePath, statuslineCache{Output: output, Updated: now, Mtime: mtime, Size: size})
	}
	return nil
}
func statuslineTranscriptStamp(path string) (int64, int64) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0
	}
	return info.ModTime().UnixNano(), info.Size()
}
func statuslineCacheFresh(c statuslineCache, mtime, size int64, now time.Time, refresh uint64) bool {
	return c.Output != "" && c.Mtime == mtime && c.Size == size && !now.Before(c.Updated) && now.Sub(c.Updated).Seconds() < float64(refresh)
}
func cachedStatuslineOutput(c statuslineCache, mtime, size int64, now time.Time, refresh uint64) (string, bool) {
	if c.Output == "" {
		return "", false
	}
	if statuslineCacheFresh(c, mtime, size, now, refresh) || (c.Updating && statuslineProcessAlive(c.PID)) {
		return c.Output, true
	}
	return "", false
}
func statuslineProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		return pid == os.Getpid()
	}
	process, err := os.FindProcess(pid)
	return err == nil && process.Signal(syscall.Signal(0)) == nil
}

func writeStatuslineCache(path string, c statuslineCache) {
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(path), "status-*")
	if err != nil {
		return
	}
	name := f.Name()
	defer os.Remove(name)
	_, writeErr := f.Write(raw)
	closeErr := f.Close()
	if writeErr == nil && closeErr == nil {
		_ = os.Rename(name, path)
	}
}

// statuslineShowsUsageCost reports whether the statusline shows the session
// cost ccusage totals itself rather than only the hook's.
func statuslineShowsUsageCost(h statuslineHook, f *statuslineFlags) bool {
	return f.costSource == "ccusage" || f.costSource == "both" || f.costSource == "auto" && h.Cost == nil
}

// statuslineBlockLookbackDays are the days behind the session window that
// each bounded statusline load reaches back, tried in turn before the
// active block falls back to the full history.
var statuslineBlockLookbackDays = []int{1, 7, 30}

const statuslineSessionLength = 5 * time.Hour

// loadStatuslineUsage loads every Claude entry dated today and finds the
// active billing block, reading only recently written files when the
// history allows it.
//
// Block boundaries chain from earlier entries until usage pauses for longer
// than a session, so a bounded load only reproduces the unbounded blocks
// once it reaches back past such a pause. Each window is widened until it
// does, and the full history is read as a last resort.
func loadStatuslineUsage(ctx context.Context, paths []string, loc *time.Location, now time.Time, debug bool, calc *calculator.Calculator) ([]types.UsageEntry, *types.SessionBlock, error) {
	for _, days := range statuslineBlockLookbackDays {
		since := statuslineBlockSince(now, loc, days)
		bound, _ := time.ParseInLocation("2006-01-02", since, loc)
		loaded, _, err := loadClaude(ctx, paths, loc, bound, debug, false)
		if err != nil {
			return nil, nil, err
		}
		// Entries dated before the window may miss skipped sessions; only
		// the ones inside it are complete. Loads return entries in time
		// order.
		var entries []types.UsageEntry
		for _, e := range loaded {
			if e.Timestamp.In(loc).Format("2006-01-02") >= since {
				entries = append(entries, e)
			}
		}
		timestamps := make([]time.Time, len(entries))
		for i, e := range entries {
			timestamps[i] = e.Timestamp
		}
		anchor, start := statuslineBlockAnchor(timestamps, now, statuslineSessionLength)
		if anchor == needsMoreHistory {
			continue
		}
		if anchor == needsFullHistory {
			break
		}
		entries, _ = calc.CalculateCosts(ctx, entries)
		if anchor == noActiveBlock {
			return entries, nil, nil
		}
		first := sort.Search(len(entries), func(i int) bool { return !entries[i].Timestamp.Before(start) })
		return entries, activeStatuslineBlock(calc, entries[first:]), nil
	}
	entries, err := loadSource(ctx, "claude", paths, loc, debug, false)
	entries, _ = calc.CalculateCosts(ctx, entries)
	return entries, activeStatuslineBlock(calc, entries), err
}

func activeStatuslineBlock(calc *calculator.Calculator, entries []types.UsageEntry) *types.SessionBlock {
	for _, block := range calc.IdentifySessionBlocksDuration(entries, statuslineSessionLength) {
		if block.IsActive && !block.IsGap {
			return &block
		}
	}
	return nil
}

type blockAnchor int

const (
	// noActiveBlock: no entry falls inside the last session window, so no
	// block is active.
	noActiveBlock blockAnchor = iota
	// anchoredBlock: blocks from the returned entry onward match an
	// unbounded load.
	anchoredBlock
	// needsMoreHistory: the window holds no pause long enough to anchor
	// block boundaries.
	needsMoreHistory
	// needsFullHistory: future-dated entries can form blocks after the
	// current one, so only the unbounded selection is safe.
	needsFullHistory
)

// statuslineBlockAnchor decides whether the sorted timestamps of every entry
// dated inside a window pin down the active block, and for anchoredBlock
// returns where block boundaries become independent of older history.
func statuslineBlockAnchor(timestamps []time.Time, now time.Time, session time.Duration) (blockAnchor, time.Time) {
	// The window reaches further back than one session, so without an entry
	// in the last session window no block can be active, whatever lies
	// before it.
	if len(timestamps) == 0 {
		return noActiveBlock, time.Time{}
	}
	last := timestamps[len(timestamps)-1]
	if last.After(now) {
		return needsFullHistory, time.Time{}
	}
	if now.Sub(last) >= session {
		return noActiveBlock, time.Time{}
	}
	// A pause longer than a session starts a fresh block at the next entry,
	// the same rule IdentifySessionBlocks applies.
	for i := len(timestamps) - 1; i > 0; i-- {
		if timestamps[i].Sub(timestamps[i-1]) > session {
			return anchoredBlock, timestamps[i]
		}
	}
	return needsMoreHistory, time.Time{}
}

// statuslineBlockSince is the first date, in loc, of a statusline load that
// reaches lookbackDays behind the session window. The load stays open-ended
// because an active block ends in the future.
func statuslineBlockSince(now time.Time, loc *time.Location, lookbackDays int) string {
	return now.Add(-statuslineSessionLength - time.Duration(lookbackDays)*24*time.Hour).In(loc).Format("2006-01-02")
}

func renderStatusline(h statuslineHook, f *statuslineFlags, aliases map[string]string, entries []types.UsageEntry, block *types.SessionBlock, loaded bool, loc *time.Location, now time.Time, service *pricing.Service) string {
	local, today := 0.0, 0.0
	for _, e := range entries {
		if e.SessionID == h.SessionID {
			local += e.Cost
		}
		if e.Timestamp.In(loc).Format("2006-01-02") == now.In(loc).Format("2006-01-02") {
			today += e.Cost
		}
	}
	cc, usage := "N/A", "N/A"
	if h.Cost != nil {
		cc = fmt.Sprintf("$%.2f", h.Cost.Total)
	}
	if loaded {
		usage = fmt.Sprintf("$%.2f", local)
	}
	session := cc
	switch f.costSource {
	case "both":
		session = "(" + cc + " cc / " + usage + " ccusage)"
	case "ccusage":
		session = usage
	case "auto":
		if h.Cost == nil {
			session = usage
		}
	}
	blockInfo, burnInfo := "No active block", ""
	if block != nil {
		remaining := int(block.EndTime.Sub(now).Minutes())
		if remaining < 0 {
			remaining = 0
		}
		blockInfo = fmt.Sprintf("$%.2f block (%dh %dm remaining)", block.CostUSD, remaining/60, remaining%60)
		if rate := calculator.CalculateBurnRate(*block); rate != nil {
			burnInfo = fmt.Sprintf(" | 🔥 $%.2f/hr", rate.CostPerHour)
			emoji, label := "🟢", "Normal"
			if rate.TokensPerMinuteForIndicator >= 5000 {
				emoji, label = "🚨", "High"
			} else if rate.TokensPerMinuteForIndicator >= 2000 {
				emoji, label = "⚠️", "Moderate"
			}
			if f.visual == "emoji" || f.visual == "emoji-text" {
				burnInfo += " " + emoji
			}
			if f.visual == "text" || f.visual == "emoji-text" {
				burnInfo += " (" + label + ")"
			}
		}
	}
	contextInfo := "N/A"
	input, limit, hasContext := 0, 0, false
	if h.Context != nil {
		input, limit, hasContext = h.Context.Input, h.Context.Limit, true
		if h.Context.Current != nil {
			input = h.Context.Current.Input + h.Context.Current.Read + h.Context.Current.Create
		}
	} else {
		input, limit, hasContext = statuslineTranscriptContext(h.TranscriptPath, h.Model.ID, service)
	}
	if hasContext {
		percent := 0
		if limit > 0 {
			percent = int(math.Round(float64(input) * 100 / float64(limit)))
		}
		percentText := fmt.Sprintf("%d%%", percent)
		if os.Getenv("FORCE_COLOR") != "" && os.Getenv("NO_COLOR") == "" {
			color := 32
			if percent >= f.medium {
				color = 31
			} else if percent >= f.low {
				color = 33
			}
			percentText = fmt.Sprintf("\x1b[%dm%s\x1b[0m", color, percentText)
		}
		contextInfo = fmt.Sprintf("%s (%s)", statuslineNumber(input), percentText)
	}
	model := h.Model.DisplayName
	if alias, ok := aliases[model]; ok {
		model = alias
	}
	if h.Effort != nil && h.Effort.Level != "" {
		model += " (" + h.Effort.Level + ")"
	}
	return fmt.Sprintf("🤖 %s | 💰 %s session / $%.2f today / %s%s | 🧠 %s", model, session, today, blockInfo, burnInfo, contextInfo)
}
func statuslineNumber(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
func statuslineTranscriptContext(path, model string, service *pricing.Service) (int, int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var v struct {
			Type    string `json:"type"`
			Message struct {
				Usage *struct {
					Input  int `json:"input_tokens"`
					Read   int `json:"cache_read_input_tokens"`
					Create int `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(lines[i]), &v) != nil || v.Type != "assistant" || v.Message.Usage == nil {
			continue
		}
		usage := v.Message.Usage
		limit := 200000
		if n, ok := service.ContextLimit(context.Background(), model); ok {
			limit = n
		}
		return usage.Input + usage.Read + usage.Create, limit, true
	}
	return 0, 0, false
}
