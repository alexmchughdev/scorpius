package cli

import (
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

type preflightCheck struct {
	Name   string `json:"name" yaml:"name"`
	Status string `json:"status" yaml:"status"`
	Detail string `json:"detail,omitempty" yaml:"detail,omitempty"`
}

type preflightReport struct {
	Checks []preflightCheck `json:"checks" yaml:"checks"`
	OK     bool             `json:"ok" yaml:"ok"`
}

func newPreflightCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "preflight",
		Short: "Check the host has the binaries and kernel features scorpius needs",
		RunE: func(cmd *cobra.Command, args []string) error {
			report := runPreflight()
			if err := render(cmd.OutOrStdout(), report); err != nil {
				return err
			}
			if !report.OK {
				return errPreflightFailed
			}
			return nil
		},
	}
}

var errPreflightFailed = &preflightError{msg: "preflight checks failed"}

type preflightError struct{ msg string }

func (e *preflightError) Error() string { return e.msg }

func runPreflight() preflightReport {
	var report preflightReport
	report.OK = true

	for _, name := range []string{"tc", "ip"} {
		check := preflightCheck{Name: name}
		if path, err := exec.LookPath(name); err == nil {
			check.Status = "ok"
			check.Detail = path
		} else {
			check.Status = "missing"
			check.Detail = err.Error()
			report.OK = false
		}
		report.Checks = append(report.Checks, check)
	}

	sysNet := preflightCheck{Name: "/sys/class/net"}
	if _, err := os.Stat("/sys/class/net"); err == nil {
		sysNet.Status = "ok"
	} else {
		sysNet.Status = "missing"
		sysNet.Detail = err.Error()
		report.OK = false
	}
	report.Checks = append(report.Checks, sysNet)

	return report
}
