package commands

import (
	"strings"

	"github.com/spf13/cobra"
)

// completeValues registers fixed shell completions for a flag's value.
func completeValues(cmd *cobra.Command, name string, values ...string) {
	_ = cmd.RegisterFlagCompletionFunc(name, cobra.FixedCompletions(values, cobra.ShellCompDirectiveNoFileComp))
}

// completeList completes one item of a comma-separated flag value.
func completeList(cmd *cobra.Command, name string, values ...string) {
	_ = cmd.RegisterFlagCompletionFunc(name, func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		prefix := ""
		if i := strings.LastIndex(toComplete, ","); i >= 0 {
			prefix = toComplete[:i+1]
		}
		used := map[string]bool{}
		for _, v := range strings.Split(prefix, ",") {
			used[strings.TrimSpace(v)] = true
		}
		var out []string
		for _, v := range values {
			if !used[v] {
				out = append(out, prefix+v)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	})
}

func completeDir(cmd *cobra.Command, names ...string) {
	for _, name := range names {
		_ = cmd.MarkFlagDirname(name)
	}
}

func completeJSONFile(cmd *cobra.Command, name string) {
	_ = cmd.MarkFlagFilename(name, "json")
}
