package main

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/RedwindA/ccusage_go/internal/commands"
)

var version = "dev"

func main() {
	// A run is short and allocation-heavy; trading some peak memory for
	// half as many collections is faster. An explicit GOGC still wins.
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(200)
	}
	ctx := context.Background()

	rootCmd := commands.NewRootCommand(version)

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
