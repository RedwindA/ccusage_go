package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RedwindA/ccusage_go/internal/calculator"
	"github.com/RedwindA/ccusage_go/internal/config"
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

// loadReportEntries loads every source the report reads. A bounded report
// drops usage dated before each source's --since, which lets Claude skip the
// sessions last written before it. detected names the agents with usage,
// including Claude usage that only skipped files hold.
func loadReportEntries(cmd *cobra.Command, agent string, scopes sourceScopes, cfg config.Config, bounded bool) ([]types.UsageEntry, []string, error) {
	f := scopes[""].f
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}
	if f.dataPath != "" && !f.allUsers {
		for _, path := range splitPaths(f.dataPath, home) {
			if _, err := os.Stat(path); err != nil {
				return nil, nil, fmt.Errorf("--data-path: %w", err)
			}
		}
	}
	names := SourceNames()
	if agent != "" {
		names = []string{agent}
	}
	var jobs []sourceJob
	if f.allUsers {
		if agent != "" {
			return nil, nil, fmt.Errorf("--all-users requires a unified report")
		}
		if runtime.GOOS != "linux" || os.Geteuid() != 0 {
			return nil, nil, fmt.Errorf("--all-users requires Linux root")
		}
		users, err := systemUsers()
		if err != nil {
			return nil, nil, err
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "Warning: --all-users ignores source path environment variables, explicit paths and pi.stores")
		for _, u := range users {
			for _, name := range names {
				jobs = append(jobs, sourceJob{name: name, user: u.Name, paths: sourcePaths(name, u.Home, false)})
			}
		}
	} else {
		for _, name := range names {
			scope := scopes.of(name)
			paths := sourcePaths(name, home, true)
			if f.dataPath != "" && (agent != "" || name == "claude") {
				paths = splitPaths(f.dataPath, home)
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
			occupied := sourcePaths("pi", home, true)
			if piPath := scopes.of("pi").f.piPath; piPath != "" {
				occupied = splitPaths(piPath, home)
			}
			for _, store := range cfg.PiStores {
				paths := splitPaths(store.Path, home)
				for _, p := range paths {
					for _, q := range occupied {
						if pathsOverlap(p, q) {
							return nil, nil, fmt.Errorf("pi store %q overlaps another pi store: %s", store.Name, p)
						}
					}
					occupied = append(occupied, p)
				}
				jobs = append(jobs, sourceJob{name: "pi", label: store.Name, paths: paths})
			}
		}
	}
	type result struct {
		entries  []types.UsageEntry
		detected bool
		err      error
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
		var entries []types.UsageEntry
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
			if bounded && scope.since != "" {
				since, _ = time.ParseInLocation("2006-01-02", scope.since, scope.loc)
			}
			entries, detected, err = loadClaude(ctx, job.paths, scope.loc, since, scope.f.debug, f.singleThread)
		} else {
			entries, err = loadSource(ctx, job.name, job.paths, scope.loc, scope.f.debug, f.singleThread)
		}
		fallbackSpeed := "standard"
		if job.name == "codex" {
			fallbackSpeed = calculator.DetectCodexSpeed(job.paths)
		}
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
		results[i] = result{entries, detected, err}
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
	var all []types.UsageEntry
	var detected []string
	seen := map[string]bool{}
	detect := func(agent string) {
		if agent != "" && !seen[agent] {
			seen[agent] = true
			detected = append(detected, agent)
		}
	}
	for i, res := range results {
		if res.err != nil {
			if errors.Is(res.err, context.Canceled) {
				return nil, nil, res.err
			}
			if agent != "" {
				return nil, nil, res.err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s %s: %v\n", jobs[i].user, jobs[i].name, res.err)
		}
		all = append(all, res.entries...)
		for _, entry := range res.entries {
			detect(entry.Agent)
		}
		if res.detected {
			detect(jobs[i].name)
		}
	}
	return all, detected, cmd.Context().Err()
}

func sourcePaths(name, home string, env bool) []string {
	if name == "claude" {
		if env {
			if paths := os.Getenv("CLAUDE_CONFIG_DIR"); paths != "" {
				return splitPaths(paths, home)
			}
		}
		xdg := filepath.Join(home, ".config")
		if env && os.Getenv("XDG_CONFIG_HOME") != "" {
			xdg = os.Getenv("XDG_CONFIG_HOME")
		}
		return []string{filepath.Join(xdg, "claude"), filepath.Join(home, ".claude")}
	}
	for _, n := range sourcesa.Names() {
		if n == name {
			if env {
				return sourcesa.DefaultPaths(name, home)
			}
			return sourcesa.DefaultPathsForHome(name, home)
		}
	}
	if env {
		return sourcesb.DefaultPaths(name, home)
	}
	return sourcesb.DefaultPathsForHome(name, home)
}

func loadSource(ctx context.Context, name string, paths []string, loc *time.Location, debug, singleThread bool) ([]types.UsageEntry, error) {
	if name == "claude" {
		entries, _, err := loadClaude(ctx, paths, loc, time.Time{}, debug, singleThread)
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
// root, which deduplicates repeated request/message IDs. A nonzero since
// skips the sessions loader.SplitFilesBeforeSince rules out, so entries dated
// before since may be missing and callers must discard them. detected
// reports whether any file, read or skipped, holds usage, as an unbounded
// load would see it.
func loadClaude(ctx context.Context, paths []string, loc *time.Location, since time.Time, debug, singleThread bool) (entries []types.UsageEntry, detected bool, err error) {
	files, err := claudeFiles(ctx, paths)
	if err != nil {
		return nil, false, err
	}
	kept, pruned := loader.SplitFilesBeforeSince(files, since, time.Now())
	if debug && len(pruned) > 0 {
		fmt.Fprintf(os.Stderr, "Debug: Skipped %d of %d JSONL files from sessions last written over a day before %s\n", len(pruned), len(files), since.Format("2006-01-02"))
	}
	l := loader.New()
	l.SetTimezone(loc)
	l.SetDebug(debug)
	if !singleThread {
		l.SetMaxWorkers(runtime.GOMAXPROCS(0))
	}
	entries = []types.UsageEntry{}
	if len(kept) > 0 {
		if entries, err = l.LoadParallel(ctx, kept); err != nil {
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
	// An unbounded load fails only when no file holds usage, so kept files
	// that cannot be read are fatal only if no skipped file has any.
	if err != nil && !detected {
		return nil, false, err
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
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
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
