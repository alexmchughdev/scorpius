package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

const (
	OutputText = "text"
	OutputJSON = "json"
	OutputYAML = "yaml"
)

// Globals are populated from persistent flags on the root command and read
// by subcommands. Tests reset them via newRootCmd().
var (
	rootVerbosity int
	rootOutput    string
	rootConfig    string
)

func newRootCmd() *cobra.Command {
	rootVerbosity = 0
	rootOutput = OutputText
	rootConfig = ""

	cmd := &cobra.Command{
		Use:           "scorpius",
		Short:         "Linux chaos engineering toolkit",
		Long:          "scorpius injects controlled failures into Linux services and reverts them safely.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().CountVarP(&rootVerbosity, "verbose", "v", "verbosity: -v for INFO, -vv for DEBUG")
	cmd.PersistentFlags().StringVarP(&rootOutput, "output", "o", OutputText, "output format: text, json, or yaml")
	cmd.PersistentFlags().StringVar(&rootConfig, "config", "", "path to config file (not yet read)")

	cmd.AddCommand(
		newInjectCmd(),
		newPreflightCmd(),
		newStatusCmd(),
	)
	return cmd
}

// Execute runs scorpius with the given args (typically os.Args[1:]) and
// returns a Unix-style exit code. It writes errors to os.Stderr.
func Execute(args []string) int {
	cmd := newRootCmd()
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// logLevel maps -v counts to slog levels.
func logLevel() slog.Level {
	switch {
	case rootVerbosity >= 2:
		return slog.LevelDebug
	case rootVerbosity == 1:
		return slog.LevelInfo
	default:
		return slog.LevelWarn
	}
}

func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: logLevel()}))
}
