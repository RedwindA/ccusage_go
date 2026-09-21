package commands

import "github.com/spf13/cobra"

// NewDailyCommand reports usage across detected sources.
func NewDailyCommand() *cobra.Command { return NewReportCommand("daily", "") }
