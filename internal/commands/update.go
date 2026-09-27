package commands

import (
	"fmt"

	"github.com/RedwindA/ccusage_go/internal/selfupdate"
	"github.com/spf13/cobra"
)

// NewUpdateCommand replaces the running binary with a GitHub release.
func NewUpdateCommand(version string) *cobra.Command {
	var check, force bool
	cmd := &cobra.Command{
		Use:     "update [vVERSION]",
		Aliases: []string{"upgrade"},
		Short:   "Update ccusage_go to the latest GitHub release",
		Long: "Download the latest (or the given) release from " + selfupdate.RepoURL + ",\n" +
			"verify its SHA-256 checksum, and replace the running binary.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			u := selfupdate.New()
			target := ""
			if len(args) == 1 {
				target = args[0]
				if !selfupdate.ValidTag(target) {
					return fmt.Errorf("invalid version %q; use a tag such as v0.18.0", target)
				}
			} else {
				fmt.Fprintln(out, "Checking for the latest release...")
				latest, err := u.Latest(ctx)
				if err != nil {
					return err
				}
				target = latest
			}
			if target == version && !force {
				fmt.Fprintf(out, "ccusage_go %s is already up to date.\n", version)
				return nil
			}
			if check {
				fmt.Fprintf(out, "Update available: %s -> %s\nRun `ccusage_go update` to install it.\n", version, target)
				return nil
			}
			exe, err := selfupdate.Executable()
			if err != nil {
				return fmt.Errorf("locate the running binary: %w", err)
			}
			fmt.Fprintf(out, "Downloading ccusage_go %s (%s/%s)...\n", target, u.GOOS, u.GOARCH)
			binary, err := u.Download(ctx, target)
			if err != nil {
				return err
			}
			if err := selfupdate.Install(ctx, exe, binary, target); err != nil {
				return err
			}
			fmt.Fprintf(out, "Updated %s: %s -> %s\n", exe, version, target)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "Only report whether an update is available")
	cmd.Flags().BoolVar(&force, "force", false, "Reinstall even if already on the target version")
	return cmd
}
