package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stephenc/git-scaffold/internal/engine"
)

func newInitCmd() *cobra.Command {
	var ref string
	var args []string
	var existing, force, textOnly bool
	c := &cobra.Command{
		Use:   "init <git-url>",
		Short: "Initialize this repository from an upstream scaffold",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, pos []string) error {
			values := map[string]string{}
			for _, kv := range args {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return fmt.Errorf("invalid --arg %q (expected name=value)", kv)
				}
				values[k] = v
			}
			return engine.Init(".", cmd.OutOrStdout(), pos[0], ref, values, existing, force, textOnly)
		},
	}
	c.Flags().StringVar(&ref, "ref", "", "source ref (branch, tag, or commit); defaults to the remote HEAD")
	c.Flags().StringArrayVar(&args, "arg", nil, "scaffold argument value as name=value (repeatable)")
	c.Flags().BoolVar(&existing, "existing", false, "adopt differing existing files as patch overrides")
	c.Flags().BoolVar(&force, "force", false, "overwrite differing existing files (with --existing: only binary files)")
	c.Flags().BoolVar(&textOnly, "text-patch", false, "with --existing: capture differences as text patches only; never json-patch")
	return c
}

func newRepatchCmd() *cobra.Command {
	var textOnly bool
	c := &cobra.Command{
		Use:   "repatch",
		Short: "Rewrite override patches from the current working-tree content of managed files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return engine.Repatch(".", cmd.OutOrStdout(), textOnly)
		},
	}
	c.Flags().BoolVar(&textOnly, "text-patch", false, "capture differences as text patches only; never json-patch")
	return c
}

func newCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Verify managed files match the locked materialization",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			clean, err := engine.Check(".", cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if !clean {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}
}

func newDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff",
		Short: "Show differences between the working tree and the locked materialization",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return engine.Diff(".", cmd.OutOrStdout())
		},
	}
}

func newApplyCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "apply",
		Short: "Make the working tree match the locked materialization",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return engine.Apply(".", cmd.OutOrStdout(), force)
		},
	}
	c.Flags().BoolVar(&force, "force", false, "discard local modifications to managed files")
	return c
}

func newUpdateCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "update",
		Short: "Update managed files to the currently resolved source ref",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return engine.Update(".", cmd.OutOrStdout(), force)
		},
	}
	c.Flags().BoolVar(&force, "force", false, "discard local modifications to managed files")
	return c
}

func newOutdatedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "outdated",
		Short: "Report whether the source ref has advanced past the lock",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			outdated, err := engine.Outdated(".", cmd.OutOrStdout())
			if err != nil {
				// §38: >1 signals an error, 1 is reserved for "update
				// available".
				return &ExitError{Code: 2, Err: err}
			}
			if outdated {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}
}

func newProposeCmd() *cobra.Command {
	var branch string
	c := &cobra.Command{
		Use:   "propose",
		Short: "Propose a scaffold update via a pushed branch and pull request",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return engine.Propose(".", cmd.OutOrStdout(), branch)
		},
	}
	c.Flags().StringVar(&branch, "branch", engine.DefaultProposalBranch, "proposal branch name")
	return c
}

func init() {
	rootCmd.AddCommand(
		newInitCmd(),
		newCheckCmd(),
		newDiffCmd(),
		newApplyCmd(),
		newUpdateCmd(),
		newOutdatedCmd(),
		newRepatchCmd(),
		newProposeCmd(),
	)
}
