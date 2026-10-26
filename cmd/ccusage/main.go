package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/RedwindA/ccusage_go/internal/commands"
	"github.com/RedwindA/ccusage_go/internal/selfupdate"
)

var version = "dev"

func main() {
	// A run is short and allocation-heavy; trading some peak memory for
	// half as many collections is faster. An explicit GOGC still wins.
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(200)
	}
	// Like upstream, TZ sets the system timezone on every platform.
	if tz, ok := os.LookupEnv("TZ"); ok {
		if loc, ok := tzLocation(tz); ok {
			time.Local = loc
		}
	}
	// An update on Windows leaves the old binary beside the new one, since it
	// was still running; delete it now that it has exited.
	if runtime.GOOS == "windows" {
		if exe, err := selfupdate.Executable(); err == nil {
			selfupdate.RemoveStale(exe)
		}
	}
	ctx := context.Background()

	rootCmd := commands.NewRootCommand(version)

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
