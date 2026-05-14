package network

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/alexmchughdev/scorpius/internal/faults"
	"github.com/alexmchughdev/scorpius/internal/watchdog"
)

const (
	// FaultName is the canonical name reported by Latency.Name and used in
	// audit events.
	FaultName = "network_latency"

	defaultHeartbeatInterval = 5 * time.Second
)

// defaultQdiscs are kernel-installed qdiscs that the "already-has-a-qdisc"
// guard ignores. Anything else on the interface means another tool is in
// charge and we refuse to overwrite it.
var defaultQdiscs = map[string]struct{}{
	"noqueue":    {},
	"pfifo_fast": {},
	"mq":         {},
	"fq_codel":   {},
}

// Latency injects link-layer delay via tc qdisc add ... netem.
type Latency struct {
	tc                TC
	wd                *watchdog.Watchdog
	heartbeatInterval time.Duration
}

// Option configures a Latency at construction.
type Option func(*Latency)

// WithHeartbeatInterval overrides the default 5s heartbeat cadence.
func WithHeartbeatInterval(d time.Duration) Option {
	return func(l *Latency) { l.heartbeatInterval = d }
}

// New constructs a Latency. tc is required; wd may be nil (used in tests
// that don't exercise the watchdog).
func New(tc TC, wd *watchdog.Watchdog, opts ...Option) *Latency {
	if tc == nil {
		panic("network.New: TC is required")
	}
	l := &Latency{
		tc:                tc,
		wd:                wd,
		heartbeatInterval: defaultHeartbeatInterval,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Name returns the canonical fault name.
func (l *Latency) Name() string { return FaultName }

type params struct {
	Delay  time.Duration
	Jitter time.Duration
}

// Validate checks the spec is well-formed.
func (l *Latency) Validate(spec faults.FaultSpec) error {
	if spec.Kind != "" && spec.Kind != FaultName {
		return fmt.Errorf("spec.Kind %q does not match %q", spec.Kind, FaultName)
	}
	if spec.Target.Interface == "" {
		return errors.New("target.interface is required")
	}
	if spec.Duration <= 0 {
		return errors.New("duration must be positive")
	}
	if _, err := parseParams(spec.Parameters); err != nil {
		return err
	}
	return nil
}

// Apply runs the safety checks, installs the netem qdisc, and (if a
// watchdog is configured) registers and starts the heartbeat loop.
func (l *Latency) Apply(ctx context.Context, spec faults.FaultSpec) (faults.Reversion, error) {
	if err := l.Validate(spec); err != nil {
		return nil, err
	}
	p, _ := parseParams(spec.Parameters)
	iface := spec.Target.Interface

	exists, err := l.tc.InterfaceExists(iface)
	if err != nil {
		return nil, fmt.Errorf("check interface %s: %w", iface, err)
	}
	if !exists {
		return nil, fmt.Errorf("interface %s does not exist", iface)
	}

	show, err := l.tc.Show(ctx, iface)
	if err != nil {
		return nil, fmt.Errorf("show qdisc on %s: %w", iface, err)
	}
	if k, busy := nonDefaultQdisc(show); busy {
		return nil, fmt.Errorf("interface %s already has a qdisc (%s); refusing to overwrite", iface, k)
	}

	args := tcNetemArgs(p)
	if err := l.tc.AddRoot(ctx, iface, args...); err != nil {
		return nil, fmt.Errorf("add netem on %s: %w", iface, err)
	}

	rev := &latencyReversion{
		tc:    l.tc,
		iface: iface,
	}

	if l.wd != nil {
		rev.wd = l.wd
		rev.wdID = l.wd.Register(FaultName, rev)
		rev.stop = make(chan struct{})
		go rev.heartbeatLoop(l.heartbeatInterval)
	}

	return rev, nil
}

// DryRun renders the exact commands that would be executed.
func (l *Latency) DryRun(spec faults.FaultSpec) (faults.Plan, error) {
	if err := l.Validate(spec); err != nil {
		return faults.Plan{}, err
	}
	p, _ := parseParams(spec.Parameters)
	iface := spec.Target.Interface

	args := strings.Join(tcNetemArgs(p), " ")
	return faults.Plan{
		Kind:        FaultName,
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

type latencyReversion struct {
	tc    TC
	iface string

	wd   *watchdog.Watchdog
	wdID watchdog.ID

	stopOnce sync.Once
	stop     chan struct{}

	mu       sync.Mutex
	reverted bool
}

func (r *latencyReversion) heartbeatLoop(interval time.Duration) {
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

func (r *latencyReversion) stopHeartbeats() {
	r.stopOnce.Do(func() {
		if r.stop != nil {
			close(r.stop)
		}
	})
}

// Revert removes the netem qdisc. Idempotent: once successful, subsequent
// calls are a no-op.
func (r *latencyReversion) Revert(ctx context.Context) error {
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
func (r *latencyReversion) Verify(ctx context.Context) error {
	out, err := r.tc.Show(ctx, r.iface)
	if err != nil {
		return fmt.Errorf("show qdisc on %s: %w", r.iface, err)
	}
	if hasNetemQdisc(out) {
		return fmt.Errorf("leftover netem qdisc on %s: %s", r.iface, strings.TrimSpace(string(out)))
	}
	return nil
}

// nonDefaultQdisc returns the kind of any user-installed qdisc found in tc's
// `qdisc show` output. Kernel defaults (noqueue, pfifo_fast, mq, fq_codel)
// are ignored.
func nonDefaultQdisc(showOutput []byte) (string, bool) {
	for _, line := range strings.Split(string(showOutput), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "qdisc" {
			continue
		}
		kind := fields[1]
		if _, isDefault := defaultQdiscs[kind]; !isDefault {
			return kind, true
		}
	}
	return "", false
}

func hasNetemQdisc(showOutput []byte) bool {
	for _, line := range strings.Split(string(showOutput), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "qdisc" {
			continue
		}
		if fields[1] == "netem" {
			return true
		}
	}
	return false
}

func tcNetemArgs(p params) []string {
	args := []string{"netem", "delay", fmt.Sprintf("%dms", p.Delay.Milliseconds())}
	if p.Jitter > 0 {
		args = append(args, fmt.Sprintf("%dms", p.Jitter.Milliseconds()))
	}
	return args
}

func parseParams(in map[string]any) (params, error) {
	var p params
	raw, ok := in["delay_ms"]
	if !ok {
		return p, errors.New("parameters.delay_ms is required")
	}
	delay, err := toMillis(raw)
	if err != nil {
		return p, fmt.Errorf("delay_ms: %w", err)
	}
	if delay <= 0 {
		return p, errors.New("delay_ms must be positive")
	}
	p.Delay = time.Duration(delay) * time.Millisecond

	if raw, ok := in["jitter_ms"]; ok {
		jitter, err := toMillis(raw)
		if err != nil {
			return p, fmt.Errorf("jitter_ms: %w", err)
		}
		if jitter < 0 {
			return p, errors.New("jitter_ms must be non-negative")
		}
		p.Jitter = time.Duration(jitter) * time.Millisecond
	}
	return p, nil
}

func toMillis(v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case uint:
		return int64(n), nil
	case uint64:
		return int64(n), nil
	case float64:
		if n != float64(int64(n)) {
			return 0, fmt.Errorf("expected integer, got %v", n)
		}
		return int64(n), nil
	default:
		return 0, fmt.Errorf("expected integer, got %T", v)
	}
}
