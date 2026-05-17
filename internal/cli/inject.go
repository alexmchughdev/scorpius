package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/alexmchughdev/scorpius/internal/audit"
	"github.com/alexmchughdev/scorpius/internal/faults"
	"github.com/alexmchughdev/scorpius/internal/faults/network"
	"github.com/alexmchughdev/scorpius/internal/watchdog"
)

const (
	defaultDuration    = time.Minute
	defaultMaxDuration = time.Hour
	revertGrace        = 30 * time.Second
)

func newInjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inject",
		Short: "Apply a fault to the local host",
	}
	cmd.AddCommand(
		newInjectNetworkLatencyCmd(),
		newInjectNetworkPacketLossCmd(),
		newInjectNetworkPartitionCmd(),
	)
	return cmd
}

type injectFlags struct {
	iface     string
	delay     time.Duration
	jitter    time.Duration
	duration  time.Duration
	maxDur    time.Duration
	operator  string
	ticket    string
	dryRun    bool
	auditPath string
	wdThresh  time.Duration
	hbInt     time.Duration
}

func newInjectNetworkLatencyCmd() *cobra.Command {
	f := &injectFlags{}

	cmd := &cobra.Command{
		Use:   "network-latency",
		Short: "Add link-layer latency to an interface using tc netem",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInjectNetworkLatency(cmd, f)
		},
	}

	cmd.Flags().StringVar(&f.iface, "interface", "", "network interface (required)")
	cmd.Flags().DurationVar(&f.delay, "delay", 0, "latency to add, e.g. 100ms (required)")
	cmd.Flags().DurationVar(&f.jitter, "jitter", 0, "jitter to add (optional)")
	cmd.Flags().DurationVar(&f.duration, "duration", defaultDuration, "how long the fault stays active")
	cmd.Flags().DurationVar(&f.maxDur, "max-duration", defaultMaxDuration, "upper bound on duration")
	cmd.Flags().StringVar(&f.operator, "operator", "", "operator identity for audit log")
	cmd.Flags().StringVar(&f.ticket, "ticket", "", "ticket reference for audit log")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "print the plan and exit; do not apply")
	cmd.Flags().StringVar(&f.auditPath, "audit-log", "", "path to audit log (default stdout)")
	cmd.Flags().DurationVar(&f.wdThresh, "watchdog-threshold", watchdog.DefaultThreshold, "watchdog heartbeat threshold")
	cmd.Flags().DurationVar(&f.hbInt, "heartbeat-interval", 5*time.Second, "fault heartbeat interval to the watchdog")

	return cmd
}

func runInjectNetworkLatency(cmd *cobra.Command, f *injectFlags) error {
	if f.iface == "" {
		return errors.New("--interface is required")
	}
	if f.delay <= 0 {
		return errors.New("--delay must be positive")
	}
	if f.duration <= 0 {
		return errors.New("--duration must be positive")
	}
	if f.duration > f.maxDur {
		return fmt.Errorf("--duration %s exceeds --max-duration %s", f.duration, f.maxDur)
	}
	if f.jitter < 0 {
		return errors.New("--jitter must be non-negative")
	}

	spec := faults.FaultSpec{
		Kind:     network.FaultName,
		Target:   faults.Target{Interface: f.iface},
		Duration: f.duration,
		Parameters: map[string]any{
			"delay_ms":  f.delay.Milliseconds(),
			"jitter_ms": f.jitter.Milliseconds(),
		},
	}

	tc, err := network.NewExecTC()
	if err != nil {
		return fmt.Errorf("tc unavailable: %w (run `scorpius preflight`)", err)
	}

	logger := newLogger(cmd.ErrOrStderr())
	wd := watchdog.New(f.wdThresh, watchdog.WithLogger(logger))
	fault := network.New(tc, wd, network.WithHeartbeatInterval(f.hbInt))

	if f.dryRun {
		plan, err := fault.DryRun(spec)
		if err != nil {
			return fmt.Errorf("dry run: %w", err)
		}
		return renderStringer(cmd.OutOrStdout(), plan)
	}

	if err := fault.Validate(spec); err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	auditLog, closeAudit, err := openAudit(f.auditPath)
	if err != nil {
		return err
	}
	defer closeAudit()

	wdCtx, wdCancel := context.WithCancel(context.Background())
	defer wdCancel()
	wdDone := make(chan struct{})
	go func() {
		wd.Run(wdCtx)
		close(wdDone)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	applyCtx, applyCancel := context.WithTimeout(context.Background(), 10*time.Second)
	rev, err := fault.Apply(applyCtx, spec)
	applyCancel()
	if err != nil {
		_ = auditLog.Emit(audit.Event{
			Type:      audit.EventVerificationFailed,
			Operator:  f.operator,
			Ticket:    f.ticket,
			Fault:     spec.Kind,
			Interface: spec.Target.Interface,
			Error:     err.Error(),
			Message:   "fault apply failed",
		})
		wdCancel()
		<-wdDone
		return fmt.Errorf("apply: %w", err)
	}

	if err := auditLog.Emit(audit.Event{
		Type:       audit.EventFaultApplied,
		Operator:   f.operator,
		Ticket:     f.ticket,
		Fault:      spec.Kind,
		Interface:  spec.Target.Interface,
		Duration:   spec.Duration.String(),
		Parameters: spec.Parameters,
	}); err != nil {
		logger.Error("audit emit failed", "err", err)
	}

	reason := waitForCompletion(f.duration, sigCh)

	revertCtx, revertCancel := context.WithTimeout(context.Background(), revertGrace)
	defer revertCancel()

	if err := rev.Revert(revertCtx); err != nil {
		_ = auditLog.Emit(audit.Event{
			Type:      audit.EventVerificationFailed,
			Operator:  f.operator,
			Ticket:    f.ticket,
			Fault:     spec.Kind,
			Interface: spec.Target.Interface,
			Reason:    reason,
			Error:     err.Error(),
			Message:   "revert failed",
		})
		wdCancel()
		<-wdDone
		return fmt.Errorf("revert: %w", err)
	}

	if err := rev.Verify(revertCtx); err != nil {
		_ = auditLog.Emit(audit.Event{
			Type:      audit.EventVerificationFailed,
			Operator:  f.operator,
			Ticket:    f.ticket,
			Fault:     spec.Kind,
			Interface: spec.Target.Interface,
			Reason:    reason,
			Error:     err.Error(),
			Message:   "verify failed",
		})
		wdCancel()
		<-wdDone
		return fmt.Errorf("verify: %w", err)
	}

	_ = auditLog.Emit(audit.Event{
		Type:      audit.EventFaultReverted,
		Operator:  f.operator,
		Ticket:    f.ticket,
		Fault:     spec.Kind,
		Interface: spec.Target.Interface,
		Reason:    reason,
	})

	wdCancel()
	<-wdDone
	return nil
}

type injectLossFlags struct {
	iface     string
	lossPct   float64
	corrPct   float64
	duration  time.Duration
	maxDur    time.Duration
	operator  string
	ticket    string
	dryRun    bool
	auditPath string
	wdThresh  time.Duration
	hbInt     time.Duration
}

func newInjectNetworkPacketLossCmd() *cobra.Command {
	f := &injectLossFlags{}

	cmd := &cobra.Command{
		Use:   "network-packet-loss",
		Short: "Drop egress packets on an interface using tc netem loss",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInjectNetworkPacketLoss(cmd, f)
		},
	}

	cmd.Flags().StringVar(&f.iface, "interface", "", "network interface (required)")
	cmd.Flags().Float64Var(&f.lossPct, "loss", 0, "packet loss percent, e.g. 5 (required, 0 < x <= 100)")
	cmd.Flags().Float64Var(&f.corrPct, "correlation", 0, "loss correlation percent (optional, 0 <= x <= 100)")
	cmd.Flags().DurationVar(&f.duration, "duration", defaultDuration, "how long the fault stays active")
	cmd.Flags().DurationVar(&f.maxDur, "max-duration", defaultMaxDuration, "upper bound on duration")
	cmd.Flags().StringVar(&f.operator, "operator", "", "operator identity for audit log")
	cmd.Flags().StringVar(&f.ticket, "ticket", "", "ticket reference for audit log")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "print the plan and exit; do not apply")
	cmd.Flags().StringVar(&f.auditPath, "audit-log", "", "path to audit log (default stdout)")
	cmd.Flags().DurationVar(&f.wdThresh, "watchdog-threshold", watchdog.DefaultThreshold, "watchdog heartbeat threshold")
	cmd.Flags().DurationVar(&f.hbInt, "heartbeat-interval", 5*time.Second, "fault heartbeat interval to the watchdog")

	return cmd
}

func runInjectNetworkPacketLoss(cmd *cobra.Command, f *injectLossFlags) error {
	if f.iface == "" {
		return errors.New("--interface is required")
	}
	if f.lossPct <= 0 {
		return errors.New("--loss must be positive")
	}
	if f.lossPct > 100 {
		return errors.New("--loss must be <= 100")
	}
	if f.corrPct < 0 {
		return errors.New("--correlation must be non-negative")
	}
	if f.corrPct > 100 {
		return errors.New("--correlation must be <= 100")
	}
	if f.duration <= 0 {
		return errors.New("--duration must be positive")
	}
	if f.duration > f.maxDur {
		return fmt.Errorf("--duration %s exceeds --max-duration %s", f.duration, f.maxDur)
	}

	params := map[string]any{
		"loss_percent": f.lossPct,
	}
	if f.corrPct > 0 {
		params["correlation_percent"] = f.corrPct
	}
	spec := faults.FaultSpec{
		Kind:       network.FaultNamePacketLoss,
		Target:     faults.Target{Interface: f.iface},
		Duration:   f.duration,
		Parameters: params,
	}

	tc, err := network.NewExecTC()
	if err != nil {
		return fmt.Errorf("tc unavailable: %w (run `scorpius preflight`)", err)
	}

	logger := newLogger(cmd.ErrOrStderr())
	wd := watchdog.New(f.wdThresh, watchdog.WithLogger(logger))
	fault := network.NewPacketLoss(tc, wd, network.WithPacketLossHeartbeatInterval(f.hbInt))

	if f.dryRun {
		plan, err := fault.DryRun(spec)
		if err != nil {
			return fmt.Errorf("dry run: %w", err)
		}
		return renderStringer(cmd.OutOrStdout(), plan)
	}

	if err := fault.Validate(spec); err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	auditLog, closeAudit, err := openAudit(f.auditPath)
	if err != nil {
		return err
	}
	defer closeAudit()

	wdCtx, wdCancel := context.WithCancel(context.Background())
	defer wdCancel()
	wdDone := make(chan struct{})
	go func() {
		wd.Run(wdCtx)
		close(wdDone)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	applyCtx, applyCancel := context.WithTimeout(context.Background(), 10*time.Second)
	rev, err := fault.Apply(applyCtx, spec)
	applyCancel()
	if err != nil {
		_ = auditLog.Emit(audit.Event{
			Type:      audit.EventVerificationFailed,
			Operator:  f.operator,
			Ticket:    f.ticket,
			Fault:     spec.Kind,
			Interface: spec.Target.Interface,
			Error:     err.Error(),
			Message:   "fault apply failed",
		})
		wdCancel()
		<-wdDone
		return fmt.Errorf("apply: %w", err)
	}

	if err := auditLog.Emit(audit.Event{
		Type:       audit.EventFaultApplied,
		Operator:   f.operator,
		Ticket:     f.ticket,
		Fault:      spec.Kind,
		Interface:  spec.Target.Interface,
		Duration:   spec.Duration.String(),
		Parameters: spec.Parameters,
	}); err != nil {
		logger.Error("audit emit failed", "err", err)
	}

	reason := waitForCompletion(f.duration, sigCh)

	revertCtx, revertCancel := context.WithTimeout(context.Background(), revertGrace)
	defer revertCancel()

	if err := rev.Revert(revertCtx); err != nil {
		_ = auditLog.Emit(audit.Event{
			Type:      audit.EventVerificationFailed,
			Operator:  f.operator,
			Ticket:    f.ticket,
			Fault:     spec.Kind,
			Interface: spec.Target.Interface,
			Reason:    reason,
			Error:     err.Error(),
			Message:   "revert failed",
		})
		wdCancel()
		<-wdDone
		return fmt.Errorf("revert: %w", err)
	}

	if err := rev.Verify(revertCtx); err != nil {
		_ = auditLog.Emit(audit.Event{
			Type:      audit.EventVerificationFailed,
			Operator:  f.operator,
			Ticket:    f.ticket,
			Fault:     spec.Kind,
			Interface: spec.Target.Interface,
			Reason:    reason,
			Error:     err.Error(),
			Message:   "verify failed",
		})
		wdCancel()
		<-wdDone
		return fmt.Errorf("verify: %w", err)
	}

	_ = auditLog.Emit(audit.Event{
		Type:      audit.EventFaultReverted,
		Operator:  f.operator,
		Ticket:    f.ticket,
		Fault:     spec.Kind,
		Interface: spec.Target.Interface,
		Reason:    reason,
	})

	wdCancel()
	<-wdDone
	return nil
}

type injectPartitionFlags struct {
	address   string
	port      int
	protocol  string
	direction string
	duration  time.Duration
	maxDur    time.Duration
	operator  string
	ticket    string
	dryRun    bool
	auditPath string
	wdThresh  time.Duration
	hbInt     time.Duration
}

func newInjectNetworkPartitionCmd() *cobra.Command {
	f := &injectPartitionFlags{}

	cmd := &cobra.Command{
		Use:   "network-partition",
		Short: "Drop traffic to or from a target address using iptables",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInjectNetworkPartition(cmd, f)
		},
	}

	cmd.Flags().StringVar(&f.address, "address", "", "IPv4 address or CIDR to partition (required)")
	cmd.Flags().IntVar(&f.port, "port", 0, "destination/source port (optional; requires --protocol)")
	cmd.Flags().StringVar(&f.protocol, "protocol", "", "protocol filter: tcp, udp, or icmp (optional)")
	cmd.Flags().StringVar(&f.direction, "direction", "both", "direction: in, out, or both")
	cmd.Flags().DurationVar(&f.duration, "duration", defaultDuration, "how long the fault stays active")
	cmd.Flags().DurationVar(&f.maxDur, "max-duration", defaultMaxDuration, "upper bound on duration")
	cmd.Flags().StringVar(&f.operator, "operator", "", "operator identity for audit log")
	cmd.Flags().StringVar(&f.ticket, "ticket", "", "ticket reference for audit log")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "print the plan and exit; do not apply")
	cmd.Flags().StringVar(&f.auditPath, "audit-log", "", "path to audit log (default stdout)")
	cmd.Flags().DurationVar(&f.wdThresh, "watchdog-threshold", watchdog.DefaultThreshold, "watchdog heartbeat threshold")
	cmd.Flags().DurationVar(&f.hbInt, "heartbeat-interval", 5*time.Second, "fault heartbeat interval to the watchdog")

	return cmd
}

func runInjectNetworkPartition(cmd *cobra.Command, f *injectPartitionFlags) error {
	if f.address == "" {
		return errors.New("--address is required")
	}
	if f.duration <= 0 {
		return errors.New("--duration must be positive")
	}
	if f.duration > f.maxDur {
		return fmt.Errorf("--duration %s exceeds --max-duration %s", f.duration, f.maxDur)
	}

	params := map[string]any{
		"direction": f.direction,
	}
	if f.protocol != "" {
		params["protocol"] = f.protocol
	}
	if f.port != 0 {
		params["port"] = f.port
	}

	spec := faults.FaultSpec{
		Kind:       network.FaultNamePartition,
		Target:     faults.Target{Address: f.address},
		Duration:   f.duration,
		Parameters: params,
	}

	ipt, err := network.NewExecIPTables()
	if err != nil {
		return fmt.Errorf("iptables unavailable: %w (run `scorpius preflight`)", err)
	}

	logger := newLogger(cmd.ErrOrStderr())
	wd := watchdog.New(f.wdThresh, watchdog.WithLogger(logger))
	fault := network.NewPartition(ipt, wd, network.WithPartitionHeartbeatInterval(f.hbInt))

	if f.dryRun {
		plan, err := fault.DryRun(spec)
		if err != nil {
			return fmt.Errorf("dry run: %w", err)
		}
		return renderStringer(cmd.OutOrStdout(), plan)
	}

	if err := fault.Validate(spec); err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	auditLog, closeAudit, err := openAudit(f.auditPath)
	if err != nil {
		return err
	}
	defer closeAudit()

	wdCtx, wdCancel := context.WithCancel(context.Background())
	defer wdCancel()
	wdDone := make(chan struct{})
	go func() {
		wd.Run(wdCtx)
		close(wdDone)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	applyCtx, applyCancel := context.WithTimeout(context.Background(), 10*time.Second)
	rev, err := fault.Apply(applyCtx, spec)
	applyCancel()
	if err != nil {
		_ = auditLog.Emit(audit.Event{
			Type:     audit.EventVerificationFailed,
			Operator: f.operator,
			Ticket:   f.ticket,
			Fault:    spec.Kind,
			Error:    err.Error(),
			Message:  "fault apply failed",
		})
		wdCancel()
		<-wdDone
		return fmt.Errorf("apply: %w", err)
	}

	if err := auditLog.Emit(audit.Event{
		Type:       audit.EventFaultApplied,
		Operator:   f.operator,
		Ticket:     f.ticket,
		Fault:      spec.Kind,
		Duration:   spec.Duration.String(),
		Parameters: spec.Parameters,
	}); err != nil {
		logger.Error("audit emit failed", "err", err)
	}

	reason := waitForCompletion(f.duration, sigCh)

	revertCtx, revertCancel := context.WithTimeout(context.Background(), revertGrace)
	defer revertCancel()

	if err := rev.Revert(revertCtx); err != nil {
		_ = auditLog.Emit(audit.Event{
			Type:     audit.EventVerificationFailed,
			Operator: f.operator,
			Ticket:   f.ticket,
			Fault:    spec.Kind,
			Reason:   reason,
			Error:    err.Error(),
			Message:  "revert failed",
		})
		wdCancel()
		<-wdDone
		return fmt.Errorf("revert: %w", err)
	}

	if err := rev.Verify(revertCtx); err != nil {
		_ = auditLog.Emit(audit.Event{
			Type:     audit.EventVerificationFailed,
			Operator: f.operator,
			Ticket:   f.ticket,
			Fault:    spec.Kind,
			Reason:   reason,
			Error:    err.Error(),
			Message:  "verify failed",
		})
		wdCancel()
		<-wdDone
		return fmt.Errorf("verify: %w", err)
	}

	_ = auditLog.Emit(audit.Event{
		Type:     audit.EventFaultReverted,
		Operator: f.operator,
		Ticket:   f.ticket,
		Fault:    spec.Kind,
		Reason:   reason,
	})

	wdCancel()
	<-wdDone
	return nil
}

func waitForCompletion(duration time.Duration, sigCh <-chan os.Signal) string {
	select {
	case <-time.After(duration):
		return "duration_complete"
	case sig := <-sigCh:
		return "signal:" + sig.String()
	}
}

func openAudit(path string) (*audit.Logger, func(), error) {
	if path == "" || path == "-" {
		return audit.Default(), func() {}, nil
	}
	l, err := audit.OpenFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open audit log: %w", err)
	}
	return l, func() { _ = l.Close() }, nil
}
