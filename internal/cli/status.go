package cli

import (
	"github.com/spf13/cobra"
)

type statusReport struct {
	Active []string `json:"active" yaml:"active"`
	Note   string   `json:"note,omitempty" yaml:"note,omitempty"`
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report currently active faults",
		Long: "Status reads from a running scorpius daemon. v0.1.0 runs scorpius as a one-shot per `inject` invocation, " +
			"so status always reports no active faults; the command exists for forward compatibility.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return render(cmd.OutOrStdout(), statusReport{
				Active: []string{},
				Note:   "v0.1.0 runs as a one-shot; no shared agent state to inspect",
			})
		},
	}
}
