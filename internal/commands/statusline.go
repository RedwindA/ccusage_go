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

	service := pricing.NewService()
	service.SetOffline(f.offline && !f.noOffline)
	service.SetOverrides(cfg.PricingOverrides("statusline", "claude"))
	calc := calculator.New(service)
	home, _ := os.UserHomeDir()
	entries, loadErr := loadSource(cmd.Context(), "claude", sourcePaths("claude", home, true), loc, f.debug)
	if loadErr != nil && f.debug {
		fmt.Fprintf(cmd.ErrOrStderr(), "statusline: %v\n", loadErr)
	}
	entries, _ = calc.CalculateCosts(cmd.Context(), entries)
	output := renderStatusline(hook, f, aliases, entries, loadErr == nil, loc, now, calc, service)
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
func renderStatusline(h statuslineHook, f *statuslineFlags, aliases map[string]string, entries []types.UsageEntry, loaded bool, loc *time.Location, now time.Time, calc *calculator.Calculator, service *pricing.Service) string {
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
	for _, block := range calc.IdentifySessionBlocks(entries, 5) {
		if !block.IsActive || block.IsGap {
			continue
		}
		remaining := int(block.EndTime.Sub(now).Minutes())
		if remaining < 0 {
			remaining = 0
		}
		blockInfo = fmt.Sprintf("$%.2f block (%dh %dm remaining)", block.CostUSD, remaining/60, remaining%60)
		if rate := calculator.CalculateBurnRate(block); rate != nil {
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
		break
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
		if p, err := service.GetPricing(context.Background(), model, time.Time{}); err == nil && p.MaxInputTokens > 0 {
			limit = p.MaxInputTokens
		}
		return usage.Input + usage.Read + usage.Create, limit, true
	}
	return 0, 0, false
}
