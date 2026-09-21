package main

import (
	"context"
	"fmt"
	"os"

	"github.com/RedwindA/ccusage_go/internal/commands"
)

var version = "dev"

func main() {
	ctx := context.Background()

	rootCmd := commands.NewRootCommand(version)

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
