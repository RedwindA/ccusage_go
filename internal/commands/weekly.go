package commands

import "github.com/spf13/cobra"

// NewWeeklyCommand reports usage across detected sources.
func NewWeeklyCommand() *cobra.Command { return NewReportCommand("weekly", "") }
