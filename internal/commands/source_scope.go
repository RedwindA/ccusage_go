package commands

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/RedwindA/ccusage_go/internal/calculator"
	"github.com/RedwindA/ccusage_go/internal/config"
	"github.com/RedwindA/ccusage_go/internal/pricing"
	"github.com/RedwindA/ccusage_go/internal/reports"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// sourceScope holds the options a report loads, prices and filters one
// source with.
type sourceScope struct {
	f            *reportFlags
	loc          *time.Location
	since, until string
	overrides    map[string]pricing.Override
	service      *pricing.Service
	calc         *calculator.Calculator
}

// sourceScopes maps "" to the report's own scope and, in a unified report,
// each source the config has sections for to the scope
// `ccusage <source> <report>` would use, so `<source>.defaults`,
// `<source>.commands.<report>` and `commands."<source>:<report>"` reach that
// source while explicit flags still win.
type sourceScopes map[string]*sourceScope

// of returns the scope agent loads with: its own, or the report's.
func (s sourceScopes) of(agent string) *sourceScope {
	if scope, ok := s[agent]; ok {
		return scope
	}
	return s[""]
}

// newSourceScope resolves the timezone and inclusive date window f selects.
func newSourceScope(cmd *cobra.Command, kind string, f *reportFlags, start time.Weekday) (*sourceScope, error) {
	s := &sourceScope{f: f, loc: time.Local}
	var err error
	if f.timezone != "" {
		if s.loc, err = time.LoadLocation(f.timezone); err != nil {
			return nil, fmt.Errorf("invalid timezone: %w", err)
		}
	}
	if s.since, err = reports.NormalizeDate(f.since); err != nil {
		return nil, err
	}
	if s.until, err = reports.NormalizeDate(f.until); err != nil {
		return nil, err
	}
	if f.last != 0 || cmd.Flags().Changed("last") {
		if s.since != "" || s.until != "" {
			return nil, fmt.Errorf("--last cannot be combined with --since or --until")
		}
		if s.since, err = reports.LastSince(kind, f.last, time.Now().In(s.loc), start); err != nil {
			return nil, err
		}
	}
	if f.date != "" {
		if s.since, err = reports.NormalizeDate(f.date); err != nil {
			return nil, err
		}
		s.until = s.since
	}
	if f.month != "" {
		t, err := time.ParseInLocation("2006-01", f.month, s.loc)
		if err != nil {
			return nil, fmt.Errorf("invalid month: %w", err)
		}
		s.since = t.Format("2006-01-02")
		s.until = t.AddDate(0, 1, -1).Format("2006-01-02")
	}
	if f.week != "" {
		date, err := reports.NormalizeDate(f.week)
		if err != nil {
			return nil, err
		}
		t, _ := time.ParseInLocation("2006-01-02", date, s.loc)
		t = reports.WeekStart(t, start)
		s.since = t.Format("2006-01-02")
		s.until = t.AddDate(0, 0, 6).Format("2006-01-02")
	}
	if s.since != "" && s.until != "" && s.since > s.until {
		return nil, fmt.Errorf("--since must not be after --until")
	}
	return s, nil
}

// addSources gives each source the config has sections for its own scope:
// the command line cli read through that source's view of the config. Weeks
// keep the unified start.
func (s sourceScopes) addSources(cmd *cobra.Command, kind string, cli reportFlags, cfg config.Config, start time.Weekday) error {
	for _, name := range SourceNames() {
		if !cfg.Addresses(kind, name) {
			continue
		}
		scope, err := sourceScopeFor(cmd, kind, name, cli, cfg, start)
		if err != nil {
			return fmt.Errorf("%w (in %s config)", err, name)
		}
		s[name] = scope
	}
	return nil
}

func sourceScopeFor(cmd *cobra.Command, kind, source string, cli reportFlags, cfg config.Config, start time.Weekday) (*sourceScope, error) {
	if err := cfg.Validate(kind, source); err != nil {
		return nil, err
	}
	f := &reportFlags{}
	flags := pflag.NewFlagSet(source, pflag.ContinueOnError)
	bindReportFlags(flags, f, kind)
	// Binding resets f to the defaults; start from the command line instead.
	*f = cli
	if err := applyConfig(cmd, flags, cfg.Options(kind, source)); err != nil {
		return nil, err
	}
	scope, err := newSourceScope(cmd, kind, f, start)
	if err != nil {
		return nil, err
	}
	if err := checkCostFlags(f); err != nil {
		return nil, err
	}
	scope.overrides = cfg.PricingOverrides(kind, source)
	return scope, nil
}

// offline reports whether no scope fetches online pricing.
func (s sourceScopes) offline() bool {
	for _, scope := range s {
		if !scope.f.offline || scope.f.noOffline {
			return false
		}
	}
	return true
}

// price gives each scope its calculator; scopes priced alike share a
// pricing service.
func (s sourceScopes) price(prefetch *pricing.Prefetch) {
	services := map[string]*pricing.Service{}
	for _, scope := range s {
		offline := scope.f.offline && !scope.f.noOffline
		key, _ := json.Marshal([]any{offline, scope.overrides})
		service := services[string(key)]
		if service == nil {
			service = newPricingService(offline, prefetch)
			service.SetOverrides(scope.overrides)
			services[string(key)] = service
		}
		scope.service = service
		scope.calc = calculator.New(service)
		scope.calc.SetMode(scope.f.mode)
		scope.calc.SetSpeed(scope.f.speed)
	}
}

// windows returns the report window of each source with a scope of its own.
func (s sourceScopes) windows() map[string]reports.Window {
	windows := map[string]reports.Window{}
	for name, scope := range s {
		if name != "" {
			windows[name] = reports.Window{Since: scope.since, Until: scope.until, Project: scope.f.project, Location: scope.loc}
		}
	}
	return windows
}
