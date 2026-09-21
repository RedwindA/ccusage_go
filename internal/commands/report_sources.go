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

func loadReportEntries(cmd *cobra.Command, agent string, f *reportFlags, loc *time.Location, cfg config.Config) ([]types.UsageEntry, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if f.dataPath != "" && !f.allUsers {
		for _, path := range splitPaths(f.dataPath, home) {
			if _, err := os.Stat(path); err != nil {
				return nil, fmt.Errorf("--data-path: %w", err)
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
			return nil, fmt.Errorf("--all-users requires a unified report")
		}
		if runtime.GOOS != "linux" || os.Geteuid() != 0 {
			return nil, fmt.Errorf("--all-users requires Linux root")
		}
		users, err := systemUsers()
		if err != nil {
			return nil, err
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "Warning: --all-users ignores source path environment variables, explicit paths and pi.stores")
		for _, u := range users {
			for _, name := range names {
				jobs = append(jobs, sourceJob{name: name, user: u.Name, paths: sourcePaths(name, u.Home, false)})
			}
		}
	} else {
		for _, name := range names {
			paths := sourcePaths(name, home, true)
			if f.dataPath != "" && (agent != "" || name == "claude") {
				paths = splitPaths(f.dataPath, home)
			}
			if name == "pi" && f.piPath != "" {
				paths = splitPaths(f.piPath, home)
			}
			if name == "openclaw" && f.openclawPath != "" {
				paths = splitPaths(f.openclawPath, home)
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
			if f.piPath != "" {
				occupied = splitPaths(f.piPath, home)
			}
			for _, store := range cfg.PiStores {
				paths := splitPaths(store.Path, home)
				for _, p := range paths {
					for _, q := range occupied {
						if pathsOverlap(p, q) {
							return nil, fmt.Errorf("pi store %q overlaps another pi store: %s", store.Name, p)
						}
					}
					occupied = append(occupied, p)
				}
				jobs = append(jobs, sourceJob{name: "pi", label: store.Name, paths: paths})
			}
		}
	}
	type result struct {
		entries []types.UsageEntry
		err     error
	}
	results := make([]result, len(jobs))
	var wg sync.WaitGroup
	work := func(i int) {
		defer wg.Done()
		job := jobs[i]
		var entries []types.UsageEntry
		var err error
		if job.name == "copilot" && f.until != "" {
			bound, e := time.ParseInLocation("2006-01-02", f.until, loc)
			if e != nil {
				err = e
			} else {
				entries, err = sourcesb.LoadUntil(cmd.Context(), job.name, job.paths, loc, bound.AddDate(0, 0, 1))
			}
		} else if job.name == "pi" {
			entries, err = sourcesa.LoadWithMode(cmd.Context(), job.name, job.paths, loc, f.mode)
		} else {
			entries, err = loadSource(cmd.Context(), job.name, job.paths, loc, f.debug)
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
		results[i] = result{entries, err}
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
	for i, res := range results {
		if res.err != nil {
			if errors.Is(res.err, context.Canceled) {
				return nil, res.err
			}
			if agent != "" {
				return nil, res.err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s %s: %v\n", jobs[i].user, jobs[i].name, res.err)
		}
		all = append(all, res.entries...)
	}
	return all, cmd.Context().Err()
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

func loadSource(ctx context.Context, name string, paths []string, loc *time.Location, debug bool) ([]types.UsageEntry, error) {
	if name == "claude" {
		l := loader.New()
		l.SetTimezone(loc)
		l.SetDebug(debug)
		// A single load across every root deduplicates repeated request/message IDs.
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
		if len(files) == 0 {
			return []types.UsageEntry{}, nil
		}
		return l.LoadParallel(ctx, files)
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
