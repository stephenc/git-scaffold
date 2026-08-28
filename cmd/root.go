// Package cmd holds the command-line interface of git-scaffold.
package cmd

import (
	"errors"
	"fmt"
	"os"

	"io"

	"github.com/spf13/cobra"

	"github.com/stephenc/git-scaffold/internal/updatecheck"
	"github.com/stephenc/git-scaffold/internal/version"
)

var rootCmd = &cobra.Command{
	Use:   "git-scaffold",
	Short: "Maintain a selected set of files from an upstream Git repository",
	Long: `git-scaffold maintains a selected set of files in a Git repository
from an upstream Git repository. It is not a one-shot project generator:
the relationship with the source repository persists after initial creation.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	// The update-check (§56) starts in the background when any command —
	// version included — begins, and its notice prints after command output.
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		updateNotice = updatecheck.Start(version.String())
	},
}

// updateNotice waits for a pending update-check and prints its one-line
// notice, if any. A no-op until PersistentPreRun runs (help and flag-parse
// errors never check).
var updateNotice = func(io.Writer) {}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version of git-scaffold",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "git-scaffold %s\n", version.String())
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

// Execute runs the root command and exits with a non-zero status on error.
func Execute() {
	err := rootCmd.Execute()
	// The notice prints after command output, on stderr only, and never
	// affects the exit status (§56). Printing here, not in a PostRun hook,
	// covers failed commands too.
	updateNotice(os.Stderr)
	if code, _ := finish(err, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

// finish maps the root command's result to a process exit status, printing
// the error line to errw for reportable errors. It returns the status (0 for
// success) and whether a line was printed.
func finish(err error, errw io.Writer) (code int, printed bool) {
	if err == nil {
		return 0, false
	}
	// An ExitError without a wrapped error carries only a status (e.g.
	// `check` reporting differences already printed to stdout).
	var ee *ExitError
	if !(errors.As(err, &ee) && ee.Err == nil) {
		fmt.Fprintf(errw, "\033[1m❌ error:\033[0m %v\n", err)
		printed = true
	}
	return exitCode(err), printed
}
