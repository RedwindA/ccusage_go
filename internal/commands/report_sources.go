package commands

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RedwindA/ccusage_go/internal/calculator"
	"github.com/RedwindA/ccusage_go/internal/config"
	"github.com/RedwindA/ccusage_go/internal/dirwalk"
	"github.com/RedwindA/ccusage_go/internal/loader"
	"github.com/RedwindA/ccusage_go/internal/parallel"
	"github.com/RedwindA/ccusage_go/internal/sourcesa"
	"github.com/RedwindA/ccusage_go/internal/sourcesb"
	"github.com/RedwindA/ccusage_go/internal/types"
	"github.com/spf13/cobra"
)

func SourceNames() []string {
	return []string{"claude", "codex", "opencode", "amp", "droid", "codebuff", "hermes", "pi", "goose", "openclaw", "kilo", "kimi", "qwen", "copilot", "gemini", "antigravity", "grok", "zcode"}
}

type sourceJob struct {
	name, label, user string
	paths             []string
}

// loadReportEntries loads every source the report reads into the views asked
// for. Bounded entries serve sections that drop usage dated before each
// source's --since, which lets Claude skip the sessions last written before
// it; whole entries keep Claude's full history. daily reads and dedupes
// bounded Claude entries as upstream's daily loader does for daily, weekly
// and monthly totals, and counts a usage event that several Codex sessions
// record once, as upstream does outside session reports; whole entries
// always follow its entry loader, and keep each Codex session's copy unless
// the Codex scope is bounded by date. Only Claude is read twice when both are
// asked for. detected names the agents with usage, including Claude usage
// that only skipped files hold. The scopes must be priced.
func loadReportEntries(cmd *cobra.Command, agent string, scopes sourceScopes, cfg config.Config, bounded, daily, whole bool) (boundedEntries, wholeEntries []types.UsageEntry, detected []string, err error) {
	f := scopes[""].f
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, nil, err
	}
	names := SourceNames()
	if agent != "" {
		names = []string{agent}
	}
	var jobs []sourceJob
	if f.allUsers {
		if agent != "" {
			return nil, nil, nil, fmt.Errorf("--all-users requires a unified report")
		}
		if runtime.GOOS != "linux" || os.Geteuid() != 0 {
			return nil, nil, nil, fmt.Errorf("--all-users requires Linux root")
		}
		users, err := systemUsers()
		if err != nil {
			return nil, nil, nil, err
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "Warning: --all-users ignores source path environment variables, explicit paths and pi.stores")
		for _, u := range users {
			for _, name := range names {
				paths, _ := sourcePaths(name, u.Home, false)
				jobs = append(jobs, sourceJob{name: name, user: u.Name, paths: paths})
			}
		}
	} else {
		for _, name := range names {
			scope := scopes.of(name)
			paths, err := sourcePaths(name, home, true)
			// --data-path names the focused source's directories, or
			// Claude's in a unified report.
			if scope.f.dataPath != "" && (agent != "" || name == "claude") {
				paths, err = splitPaths(scope.f.dataPath, home), nil
				for _, path := range paths {
					if _, err = os.Stat(path); err != nil {
						return nil, nil, nil, fmt.Errorf("--data-path: %w", err)
					}
				}
			}
			if err != nil {
				return nil, nil, nil, err
			}
			if name == "pi" && scope.f.piPath != "" {
				paths = splitPaths(scope.f.piPath, home)
			}
			if name == "openclaw" && scope.f.openclawPath != "" {
				paths = splitPaths(scope.f.openclawPath, home)
			}
			for i, path := range paths {
				if path == "~" {
					paths[i] = home
				} else if strings.HasPrefix(path, "~/") {
					paths[i] = filepath.Join(home, path[2:])
				}
			}
			jobs = append(jobs, sourceJob{name: name, paths: paths})
		}
		if agent == "" {
			occupied, _ := sourcePaths("pi", home, true)
			if piPath := scopes.of("pi").f.piPath; piPath != "" {
				occupied = splitPaths(piPath, home)
			}
			for _, store := range cfg.PiStores {
				paths := splitPaths(store.Path, home)
				for _, p := range paths {
					for _, q := range occupied {
						if pathsOverlap(p, q) {
							return nil, nil, nil, fmt.Errorf("pi store %q overlaps another pi store: %s", store.Name, p)
						}
					}
					occupied = append(occupied, p)
				}
				jobs = append(jobs, sourceJob{name: "pi", label: store.Name, paths: paths})
			}
		}
	}
	type result struct {
		entries, whole []types.UsageEntry
		detected       bool
		err            error
	}
	results := make([]result, len(jobs))
	var wg sync.WaitGroup
	ctx := cmd.Context()
	if f.singleThread {
		ctx = parallel.WithLimit(ctx, 1)
	}
	work := func(i int) {
		defer wg.Done()
		job := jobs[i]
		// Named pi stores load with the report's own options.
		scope := scopes[""]
		if job.label == "" {
			scope = scopes.of(job.name)
		}
		var entries, history []types.UsageEntry
		var detected bool
		var err error
		if job.name == "copilot" && scope.until != "" {
			bound, e := time.ParseInLocation("2006-01-02", scope.until, scope.loc)
			if e != nil {
				err = e
			} else {
				entries, err = sourcesb.LoadUntil(ctx, job.name, job.paths, scope.loc, bound.AddDate(0, 0, 1))
			}
		} else if job.name == "pi" {
			entries, err = sourcesa.LoadWithMode(ctx, job.name, job.paths, scope.loc, scope.f.mode)
		} else if job.name == "claude" {
			var since time.Time
			var cost func(*types.UsageEntry) float64
			if bounded {
				if scope.since != "" {
					since, _ = time.ParseInLocation("2006-01-02", scope.since, scope.loc)
				}
				if daily {
					cost = dailyDedupeCost(scope.calc)
				}
			}
			// The whole history beside a bounded or daily view reads each
			// transcript once for both.
			var parsed *loader.ParsedFiles
			if whole && (!since.IsZero() || cost != nil) {
				parsed = new(loader.ParsedFiles)
			}
			entries, detected, err = loadClaude(ctx, job.paths, scope.loc, since, scope.project, cost, scope.f.debug, f.singleThread, parsed)
			if parsed != nil && err == nil {
				history, _, err = loadClaude(ctx, job.paths, scope.loc, time.Time{}, scope.project, nil, scope.f.debug, f.singleThread, parsed)
			}
		} else {
			entries, err = loadSource(ctx, job.name, job.paths, scope.loc, scope.f.debug, f.singleThread)
			if job.name == "codex" && daily {
				// A unified session report bounded by date reads Codex as
				// the other reports do.
				if whole && scope.since == "" && scope.until == "" {
					history = entries
				}
				entries = sourcesa.MergeCodexCopies(entries)
			}
		}
		fallbackSpeed := "standard"
		if job.name == "codex" {
			fallbackSpeed = calculator.DetectCodexSpeed(job.paths)
		}
		tag := func(entries []types.UsageEntry) {
			for j := range entries {
				if job.name == "codex" && entries[j].Speed == "" {
					entries[j].Speed = fallbackSpeed
				}
				if entries[j].Agent == "" {
					entries[j].Agent = job.name
				}
				if job.label != "" {
					entries[j].Agent = job.label
				}
				if job.user != "" {
					if entries[j].Raw == nil {
						entries[j].Raw = map[string]any{}
					}
					entries[j].Raw["system_user"] = job.user
				}
			}
		}
		tag(entries)
		tag(history)
		if history == nil {
			history = entries
		}
		results[i] = result{entries, history, detected, err}
	}
	for i := range jobs {
		wg.Add(1)
		if f.singleThread {
			work(i)
		} else {
			go work(i)
		}
	}
	wg.Wait()
	seen := map[string]bool{}
	detect := func(agent string) {
		if agent != "" && !seen[agent] {
			seen[agent] = true
			detected = append(detected, agent)
		}
	}
	for i, res := range results {
		if res.err != nil {
			// As upstream, the first source that fails fails the report;
			// --all-users, which upstream lacks, skips a user's failing
			// source instead.
			if !f.allUsers || errors.Is(res.err, context.Canceled) {
				return nil, nil, nil, res.err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s %s: %v\n", jobs[i].user, jobs[i].name, res.err)
		}
		if bounded {
			boundedEntries = append(boundedEntries, res.entries...)
		}
		if whole {
			wholeEntries = append(wholeEntries, res.whole...)
		}
		for _, entry := range res.entries {
			detect(entry.Agent)
		}
		if res.detected {
			detect(jobs[i].name)
		}
	}
	return boundedEntries, wholeEntries, detected, cmd.Context().Err()
}

// sourcePaths returns the directories name is read from under home, or with
// env the ones its environment variables name. Only a set CLAUDE_CONFIG_DIR
// can fail; see claudeConfigDirs.
func sourcePaths(name, home string, env bool) ([]string, error) {
	if name == "claude" {
		if env {
			if dirs, ok := os.LookupEnv("CLAUDE_CONFIG_DIR"); ok {
				return claudeConfigDirs(dirs, home)
			}
		}
		xdg := filepath.Join(home, ".config")
		if env && os.Getenv("XDG_CONFIG_HOME") != "" {
			xdg = os.Getenv("XDG_CONFIG_HOME")
		}
		return []string{filepath.Join(xdg, "claude"), filepath.Join(home, ".claude")}, nil
	}
	for _, n := range sourcesa.Names() {
		if n == name {
			if env {
				return sourcesa.DefaultPaths(name, home), nil
			}
			return sourcesa.DefaultPathsForHome(name, home), nil
		}
	}
	if env {
		return sourcesb.DefaultPaths(name, home), nil
	}
	return sourcesb.DefaultPathsForHome(name, home), nil
}

// claudeConfigDirs returns the Claude config directories dirs, a
// CLAUDE_CONFIG_DIR value, names as upstream reads it: entries that hold a
// projects/ directory or are one, skipping the rest. Naming none, even when
// set but empty, is an error.
func claudeConfigDirs(dirs, home string) ([]string, error) {
	isDir := func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.IsDir()
	}
	var valid []string
	for _, dir := range splitPaths(dirs, home) {
		dir = filepath.Clean(dir)
		if filepath.Base(dir) == "projects" && isDir(dir) {
			dir = filepath.Dir(dir)
		}
		if isDir(filepath.Join(dir, "projects")) {
			valid = append(valid, dir)
		}
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("no valid Claude data directories found in CLAUDE_CONFIG_DIR; expected each path to be a Claude config directory containing projects/, or the projects/ directory itself: %s", dirs)
	}
	return valid, nil
}

// dailyDedupeCost prices a copy of a Claude entry as the report will, for
// the daily dedupe to keep the costlier of two copies with equal usage.
func dailyDedupeCost(calc *calculator.Calculator) func(*types.UsageEntry) float64 {
	return func(e *types.UsageEntry) float64 {
		priced := *e
		priced.Agent = "claude"
		priced.Raw = maps.Clone(e.Raw)
		calc.CalculateCost(&priced)
		return priced.Cost
	}
}

func loadSource(ctx context.Context, name string, paths []string, loc *time.Location, debug, singleThread bool) ([]types.UsageEntry, error) {
	if name == "claude" {
		entries, _, err := loadClaude(ctx, paths, loc, time.Time{}, "", nil, debug, singleThread, nil)
		return entries, err
	}
	for _, n := range sourcesa.Names() {
		if n == name {
			return sourcesa.Load(ctx, name, paths, loc)
		}
	}
	return sourcesb.Load(ctx, name, paths, loc)
}

func splitPaths(s, home string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == "~" {
			p = home
		} else if strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, p[2:])
		}
		out = append(out, p)
	}
	return out
}
func pathsOverlap(a, b string) bool {
	canonical := func(p string) string {
		p, _ = filepath.Abs(p)
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			p = resolved
		}
		return filepath.Clean(p)
	}
	a, b = canonical(a), canonical(b)
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

// loadClaude loads Claude usage from paths in a single load across every
// root, which deduplicates repeated request/message IDs. A project reads only
// that project's files, so they are deduplicated among themselves. A nonzero
// since skips the sessions loader.SplitFilesBeforeSince rules out, so entries
// dated before since may be missing and callers must discard them. A non-nil
// dailyCost reads and dedupes as upstream's daily loader (see
// Loader.SetDailyLoader). Loads sharing a non-nil parsed read each file
// once between them; they must share loc and debug.
// detected reports whether any file, read or skipped, holds usage, as an
// unbounded load would see it.
func loadClaude(ctx context.Context, paths []string, loc *time.Location, since time.Time, project string, dailyCost func(*types.UsageEntry) float64, debug, singleThread bool, parsed *loader.ParsedFiles) (entries []types.UsageEntry, detected bool, err error) {
	files, err := claudeFiles(ctx, paths)
	if err != nil {
		return nil, false, err
	}
	if project != "" {
		files = loader.ProjectFiles(files, project)
	}
	kept, pruned := loader.SplitFilesBeforeSince(files, since, time.Now())
	if debug && len(pruned) > 0 {
		fmt.Fprintf(os.Stderr, "Debug: Skipped %d of %d JSONL files from sessions last written over a day before %s\n", len(pruned), len(files), since.Format("2006-01-02"))
	}
	l := loader.New()
	l.SetTimezone(loc)
	l.SetDebug(debug)
	l.SetDailyLoader(dailyCost)
	l.SetParsedFiles(parsed)
	if !singleThread {
		l.SetMaxWorkers(runtime.GOMAXPROCS(0))
	}
	entries = []types.UsageEntry{}
	if len(kept) > 0 {
		// LoadParallel fails when no file it reads holds usage and one
		// cannot be read; like upstream, such a file is skipped.
		if entries, err = l.LoadParallel(ctx, kept); err != nil {
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			entries = []types.UsageEntry{}
		}
	}
	detected = len(entries) > 0
	// Skipped sessions still hold usage; the first file with an entry
	// settles it.
	for _, file := range pruned {
		if detected {
			break
		}
		found, _ := l.LoadParallel(ctx, []string{file})
		detected = len(found) > 0
	}
	return entries, detected, nil
}

// claudeFiles lists the JSONL transcripts under paths, sorted, reading each
// root's projects directory when it has one.
func claudeFiles(ctx context.Context, paths []string) ([]string, error) {
	files := []string{}
	seen := map[string]bool{}
	for _, root := range paths {
		root = filepath.Clean(root)
		if info, err := os.Stat(filepath.Join(root, "projects")); err == nil && info.IsDir() {
			root = filepath.Join(root, "projects")
		}
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := dirwalk.Walk(root, func(path string, d os.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Like upstream, a directory that cannot be read is skipped.
			if err != nil {
				return nil
			}
			// Like upstream, read only regular files: a symlink below root
			// is not followed.
			if d.Type().IsRegular() && strings.HasSuffix(path, ".jsonl") {
				abs, _ := filepath.Abs(path)
				if !seen[abs] {
					files = append(files, abs)
					seen[abs] = true
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}
