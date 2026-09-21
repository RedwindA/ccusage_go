package commands

import "github.com/spf13/cobra"

// NewMonthlyCommand reports usage across detected sources.
func NewMonthlyCommand() *cobra.Command { return NewReportCommand("monthly", "") }
