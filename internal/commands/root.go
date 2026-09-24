package commands

import "github.com/spf13/cobra"

func NewRootCommand(version string) *cobra.Command {
	root := NewReportCommand("daily", "")
	root.Use = "ccusage_go"
	root.Short = "Analyze coding agent token usage and costs"
	root.Version = version
	root.SilenceUsage = true
	root.SilenceErrors = true
	for _, kind := range []string{"daily", "weekly", "monthly", "session"} {
		root.AddCommand(NewReportCommand(kind, ""))
	}
	root.AddCommand(NewBlocksCommand(), NewMonitorCommand(), NewStatuslineCommand())
	for _, name := range SourceNames() {
		agent := NewReportCommand("daily", name)
		agent.Use = name
		agent.Short = "Analyze " + name + " usage"
		for _, kind := range []string{"daily", "weekly", "monthly", "session"} {
			agent.AddCommand(NewReportCommand(kind, name))
		}
		if name == "claude" || name == "codex" || name == "droid" {
			agent.AddCommand(NewReportCommand("model", name), NewReportCommand("workspace", name))
		}
		if name == "claude" {
			agent.AddCommand(NewBlocksCommand(), NewStatuslineCommand())
		}
		root.AddCommand(agent)
	}
	var setVersion func(*cobra.Command)
	setVersion = func(cmd *cobra.Command) {
		cmd.Version = version
		for _, child := range cmd.Commands() {
			setVersion(child)
		}
	}
	setVersion(root)
	return root
}
