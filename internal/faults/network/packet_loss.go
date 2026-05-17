package network

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexmchughdev/scorpius/internal/faults"
	"github.com/alexmchughdev/scorpius/internal/watchdog"
)

// FaultNamePacketLoss is the canonical name reported by PacketLoss.Name and
// used in audit events.
const FaultNamePacketLoss = "network_packet_loss"

// PacketLoss injects link-layer packet loss via tc qdisc add ... netem loss.
type PacketLoss struct {
	tc                TC
	wd                *watchdog.Watchdog
	heartbeatInterval time.Duration
}

// PacketLossOption configures a PacketLoss at construction.
type PacketLossOption func(*PacketLoss)

// WithPacketLossHeartbeatInterval overrides the default 5s heartbeat cadence.
func WithPacketLossHeartbeatInterval(d time.Duration) PacketLossOption {
	return func(p *PacketLoss) { p.heartbeatInterval = d }
}

// NewPacketLoss constructs a PacketLoss. tc is required; wd may be nil (used
// in tests that don't exercise the watchdog).
func NewPacketLoss(tc TC, wd *watchdog.Watchdog, opts ...PacketLossOption) *PacketLoss {
	if tc == nil {
		panic("network.NewPacketLoss: TC is required")
	}
	p := &PacketLoss{
		tc:                tc,
		wd:                wd,
		heartbeatInterval: defaultHeartbeatInterval,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the canonical fault name.
func (p *PacketLoss) Name() string { return FaultNamePacketLoss }

type lossParams struct {
	Loss        float64
	Correlation float64
}

// Validate checks the spec is well-formed.
func (p *PacketLoss) Validate(spec faults.FaultSpec) error {
	if spec.Kind != "" && spec.Kind != FaultNamePacketLoss {
		return fmt.Errorf("spec.Kind %q does not match %q", spec.Kind, FaultNamePacketLoss)
	}
	if spec.Target.Interface == "" {
		return errors.New("target.interface is required")
	}
	if spec.Duration <= 0 {
		return errors.New("duration must be positive")
	}
	if _, err := parseLossParams(spec.Parameters); err != nil {
		return err
	}
	return nil
}

// Apply runs the safety checks, installs the netem loss qdisc, and (if a
// watchdog is configured) registers and starts the heartbeat loop.
func (p *PacketLoss) Apply(ctx context.Context, spec faults.FaultSpec) (faults.Reversion, error) {
	if err := p.Validate(spec); err != nil {
		return nil, err
	}
	params, _ := parseLossParams(spec.Parameters)
	iface := spec.Target.Interface

	exists, err := p.tc.InterfaceExists(iface)
	if err != nil {
		return nil, fmt.Errorf("check interface %s: %w", iface, err)
	}
	if !exists {
		return nil, fmt.Errorf("interface %s does not exist", iface)
	}

	show, err := p.tc.Show(ctx, iface)
	if err != nil {
		return nil, fmt.Errorf("show qdisc on %s: %w", iface, err)
	}
	if k, busy := nonDefaultQdisc(show); busy {
		return nil, fmt.Errorf("interface %s already has a qdisc (%s); refusing to overwrite", iface, k)
	}

	args := tcNetemLossArgs(params)
	if err := p.tc.AddRoot(ctx, iface, args...); err != nil {
		return nil, fmt.Errorf("add netem loss on %s: %w", iface, err)
	}

	rev := &packetLossReversion{
		tc:    p.tc,
		iface: iface,
	}

	if p.wd != nil {
		rev.wd = p.wd
		rev.wdID = p.wd.Register(FaultNamePacketLoss, rev)
		rev.stop = make(chan struct{})
		go rev.heartbeatLoop(p.heartbeatInterval)
	}

	return rev, nil
}

// DryRun renders the exact commands that would be executed.
func (p *PacketLoss) DryRun(spec faults.FaultSpec) (faults.Plan, error) {
	if err := p.Validate(spec); err != nil {
		return faults.Plan{}, err
	}
	params, _ := parseLossParams(spec.Parameters)
	iface := spec.Target.Interface

	args := strings.Join(tcNetemLossArgs(params), " ")
	return faults.Plan{
		Kind:        FaultNamePacketLoss,
		Description: fmt.Sprintf("add %s on %s for %s", args, iface, spec.Duration),
		Commands: []string{
			fmt.Sprintf("tc qdisc show dev %s", iface),
			fmt.Sprintf("tc qdisc add dev %s root %s", iface, args),
			fmt.Sprintf("tc qdisc del dev %s root", iface),
		},
		SideEffects: []string{
			fmt.Sprintf("all egress traffic on %s shaped by: %s", iface, args),
		},
	}, nil
}

type packetLossReversion struct {
	tc    TC
	iface string

	wd   *watchdog.Watchdog
	wdID watchdog.ID

	stopOnce sync.Once
	stop     chan struct{}

	mu       sync.Mutex
	reverted bool
}

func (r *packetLossReversion) heartbeatLoop(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.wd.Heartbeat(r.wdID)
		}
	}
}

func (r *packetLossReversion) stopHeartbeats() {
	r.stopOnce.Do(func() {
		if r.stop != nil {
			close(r.stop)
		}
	})
}

// Revert removes the netem qdisc. Idempotent.
func (r *packetLossReversion) Revert(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reverted {
		return nil
	}

	r.stopHeartbeats()
	if r.wd != nil {
		r.wd.Deregister(r.wdID)
	}

	if err := r.tc.DelRoot(ctx, r.iface); err != nil {
		return fmt.Errorf("delete netem on %s: %w", r.iface, err)
	}

	r.reverted = true
	return nil
}

// Verify checks no netem qdisc remains on the interface.
func (r *packetLossReversion) Verify(ctx context.Context) error {
	out, err := r.tc.Show(ctx, r.iface)
	if err != nil {
		return fmt.Errorf("show qdisc on %s: %w", r.iface, err)
	}
	if hasNetemQdisc(out) {
		return fmt.Errorf("leftover netem qdisc on %s: %s", r.iface, strings.TrimSpace(string(out)))
	}
	return nil
}

func tcNetemLossArgs(p lossParams) []string {
	args := []string{"netem", "loss", formatPercent(p.Loss)}
	if p.Correlation > 0 {
		args = append(args, formatPercent(p.Correlation))
	}
	return args
}

func formatPercent(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64) + "%"
}

func parseLossParams(in map[string]any) (lossParams, error) {
	var p lossParams
	raw, ok := in["loss_percent"]
	if !ok {
		return p, errors.New("parameters.loss_percent is required")
	}
	loss, err := toPercent(raw)
	if err != nil {
		return p, fmt.Errorf("loss_percent: %w", err)
	}
	if loss <= 0 {
		return p, errors.New("loss_percent must be positive")
	}
	if loss > 100 {
		return p, errors.New("loss_percent must be <= 100")
	}
	p.Loss = loss

	if raw, ok := in["correlation_percent"]; ok {
		corr, err := toPercent(raw)
		if err != nil {
			return p, fmt.Errorf("correlation_percent: %w", err)
		}
		if corr < 0 {
			return p, errors.New("correlation_percent must be non-negative")
		}
		if corr > 100 {
			return p, errors.New("correlation_percent must be <= 100")
		}
		p.Correlation = corr
	}
	return p, nil
}

func toPercent(v any) (float64, error) {
	switch n := v.(type) {
	case int:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case uint:
		return float64(n), nil
	case uint64:
		return float64(n), nil
	case float32:
		return float64(n), nil
	case float64:
		return n, nil
	default:
		return 0, fmt.Errorf("expected number, got %T", v)
	}
}
