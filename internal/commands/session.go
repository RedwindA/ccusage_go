package commands

import "github.com/spf13/cobra"

// NewSessionCommand reports usage across detected sources.
func NewSessionCommand() *cobra.Command { return NewReportCommand("session", "") }
