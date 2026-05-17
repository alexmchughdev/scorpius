package network

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexmchughdev/scorpius/internal/faults"
	"github.com/alexmchughdev/scorpius/internal/watchdog"
)

// FaultNamePartition is the canonical name reported by Partition.Name and
// used in audit events.
const FaultNamePartition = "network_partition"

// chainPrefix is the prefix of every scorpius-managed iptables chain. The
// chain name carries an 8-hex-character suffix so concurrent partitions on
// the same host do not collide. See ADR-0003.
const chainPrefix = "SCORPIUS_PART_"

// Direction selects which traffic direction(s) the partition blocks.
type Direction string

const (
	DirectionOut  Direction = "out"
	DirectionIn   Direction = "in"
	DirectionBoth Direction = "both"
)

// Partition drops traffic to or from a target address using iptables. Each
// instance owns a dedicated user chain so revert never touches operator
// rules.
type Partition struct {
	ipt               IPTables
	wd                *watchdog.Watchdog
	heartbeatInterval time.Duration
	suffixFunc        func() (string, error)
}

// PartitionOption configures a Partition at construction.
type PartitionOption func(*Partition)

// WithPartitionHeartbeatInterval overrides the default 5s heartbeat cadence.
func WithPartitionHeartbeatInterval(d time.Duration) PartitionOption {
	return func(p *Partition) { p.heartbeatInterval = d }
}

// WithPartitionSuffixFunc overrides the chain-suffix generator. Used in
// tests to get deterministic chain names.
func WithPartitionSuffixFunc(f func() (string, error)) PartitionOption {
	return func(p *Partition) { p.suffixFunc = f }
}

// NewPartition constructs a Partition. ipt is required; wd may be nil.
func NewPartition(ipt IPTables, wd *watchdog.Watchdog, opts ...PartitionOption) *Partition {
	if ipt == nil {
		panic("network.NewPartition: IPTables is required")
	}
	p := &Partition{
		ipt:               ipt,
		wd:                wd,
		heartbeatInterval: defaultHeartbeatInterval,
		suffixFunc:        randomChainSuffix,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the canonical fault name.
func (p *Partition) Name() string { return FaultNamePartition }

type partitionParams struct {
	Direction Direction
	Port      int
	Protocol  string
}

// Validate checks the spec is well-formed.
func (p *Partition) Validate(spec faults.FaultSpec) error {
	if spec.Kind != "" && spec.Kind != FaultNamePartition {
		return fmt.Errorf("spec.Kind %q does not match %q", spec.Kind, FaultNamePartition)
	}
	if spec.Target.Address == "" {
		return errors.New("target.address is required")
	}
	if err := validateIPv4Address(spec.Target.Address); err != nil {
		return fmt.Errorf("target.address: %w", err)
	}
	if spec.Duration <= 0 {
		return errors.New("duration must be positive")
	}
	if _, err := parsePartitionParams(spec.Parameters); err != nil {
		return err
	}
	return nil
}

// Apply creates the dedicated chain, populates DROP rules in it, and
// inserts JUMP rules into the default chains.
func (p *Partition) Apply(ctx context.Context, spec faults.FaultSpec) (faults.Reversion, error) {
	if err := p.Validate(spec); err != nil {
		return nil, err
	}
	params, _ := parsePartitionParams(spec.Parameters)

	suffix, err := p.suffixFunc()
	if err != nil {
		return nil, fmt.Errorf("generate chain suffix: %w", err)
	}
	chain := chainPrefix + suffix

	rules := buildDropRules(spec.Target.Address, params)
	jumps := jumpDirections(params.Direction)

	if err := p.ipt.NewChain(ctx, chain); err != nil {
		return nil, fmt.Errorf("create chain %s: %w", chain, err)
	}

	for _, r := range rules {
		if err := p.ipt.AppendRule(ctx, chain, r...); err != nil {
			// Tear down what we created so we never leak partial state.
			_ = p.ipt.FlushChain(ctx, chain)
			_ = p.ipt.DeleteChain(ctx, chain)
			return nil, fmt.Errorf("append rule to %s: %w", chain, err)
		}
	}

	installed := make([]string, 0, len(jumps))
	for _, j := range jumps {
		if err := p.ipt.InsertRule(ctx, j, "-j", chain); err != nil {
			// Roll back jumps we already installed before tearing down.
			for _, done := range installed {
				_ = p.ipt.DeleteRule(ctx, done, "-j", chain)
			}
			_ = p.ipt.FlushChain(ctx, chain)
			_ = p.ipt.DeleteChain(ctx, chain)
			return nil, fmt.Errorf("install jump %s -> %s: %w", j, chain, err)
		}
		installed = append(installed, j)
	}

	rev := &partitionReversion{
		ipt:        p.ipt,
		chain:      chain,
		jumpChains: append([]string(nil), installed...),
	}

	if p.wd != nil {
		rev.wd = p.wd
		rev.wdID = p.wd.Register(FaultNamePartition, rev)
		rev.stop = make(chan struct{})
		go rev.heartbeatLoop(p.heartbeatInterval)
	}

	return rev, nil
}

// DryRun renders the exact iptables commands that would be executed.
func (p *Partition) DryRun(spec faults.FaultSpec) (faults.Plan, error) {
	if err := p.Validate(spec); err != nil {
		return faults.Plan{}, err
	}
	params, _ := parsePartitionParams(spec.Parameters)

	// DryRun deliberately uses a fixed-suffix placeholder so the rendered
	// commands are stable across invocations. The real Apply generates a
	// random suffix.
	chain := chainPrefix + "DRYRUNXX"

	cmds := make([]string, 0, 3+len(buildDropRules(spec.Target.Address, params)))
	cmds = append(cmds, fmt.Sprintf("iptables -w %s -N %s", xtablesLockWait, chain))
	for _, r := range buildDropRules(spec.Target.Address, params) {
		cmds = append(cmds, fmt.Sprintf("iptables -w %s -A %s %s",
			xtablesLockWait, chain, strings.Join(r, " ")))
	}
	for _, j := range jumpDirections(params.Direction) {
		cmds = append(cmds, fmt.Sprintf("iptables -w %s -I %s 1 -j %s",
			xtablesLockWait, j, chain))
	}
	cmds = append(cmds,
		fmt.Sprintf("# revert: delete the jumps, then -F %s, then -X %s", chain, chain),
	)

	sideEffect := fmt.Sprintf("DROP traffic %s %s",
		describeDirection(params.Direction), describeTarget(spec.Target.Address, params))

	return faults.Plan{
		Kind: FaultNamePartition,
		Description: fmt.Sprintf("partition %s for %s",
			describeTarget(spec.Target.Address, params), spec.Duration),
		Commands:    cmds,
		SideEffects: []string{sideEffect},
	}, nil
}

type partitionReversion struct {
	ipt        IPTables
	chain      string
	jumpChains []string

	wd   *watchdog.Watchdog
	wdID watchdog.ID

	stopOnce sync.Once
	stop     chan struct{}

	mu       sync.Mutex
	reverted bool
}

func (r *partitionReversion) heartbeatLoop(interval time.Duration) {
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

func (r *partitionReversion) stopHeartbeats() {
	r.stopOnce.Do(func() {
		if r.stop != nil {
			close(r.stop)
		}
	})
}

// Revert deletes the JUMP rules, flushes and deletes the chain. Idempotent.
func (r *partitionReversion) Revert(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reverted {
		return nil
	}

	r.stopHeartbeats()
	if r.wd != nil {
		r.wd.Deregister(r.wdID)
	}

	for _, j := range r.jumpChains {
		if err := r.ipt.DeleteRule(ctx, j, "-j", r.chain); err != nil {
			return fmt.Errorf("delete jump %s -> %s: %w", j, r.chain, err)
		}
	}
	if err := r.ipt.FlushChain(ctx, r.chain); err != nil {
		return fmt.Errorf("flush chain %s: %w", r.chain, err)
	}
	if err := r.ipt.DeleteChain(ctx, r.chain); err != nil {
		return fmt.Errorf("delete chain %s: %w", r.chain, err)
	}

	r.reverted = true
	return nil
}

// Verify confirms the chain is gone.
func (r *partitionReversion) Verify(ctx context.Context) error {
	exists, err := r.ipt.ChainExists(ctx, r.chain)
	if err != nil {
		return fmt.Errorf("check chain %s: %w", r.chain, err)
	}
	if exists {
		return fmt.Errorf("leftover chain %s on host", r.chain)
	}
	return nil
}

func buildDropRules(address string, p partitionParams) [][]string {
	var rules [][]string
	switch p.Direction {
	case DirectionOut, DirectionBoth, "":
		rules = append(rules, outboundDropRule(address, p))
	}
	switch p.Direction {
	case DirectionIn, DirectionBoth, "":
		rules = append(rules, inboundDropRule(address, p))
	}
	return rules
}

func outboundDropRule(address string, p partitionParams) []string {
	args := []string{"-d", address}
	if p.Protocol != "" {
		args = append(args, "-p", p.Protocol)
		if p.Port != 0 {
			args = append(args, "--dport", strconv.Itoa(p.Port))
		}
	}
	return append(args, "-j", "DROP")
}

func inboundDropRule(address string, p partitionParams) []string {
	args := []string{"-s", address}
	if p.Protocol != "" {
		args = append(args, "-p", p.Protocol)
		if p.Port != 0 {
			args = append(args, "--sport", strconv.Itoa(p.Port))
		}
	}
	return append(args, "-j", "DROP")
}

func jumpDirections(d Direction) []string {
	switch d {
	case DirectionOut:
		return []string{"OUTPUT"}
	case DirectionIn:
		return []string{"INPUT"}
	case DirectionBoth, "":
		return []string{"OUTPUT", "INPUT"}
	}
	return nil
}

func describeDirection(d Direction) string {
	switch d {
	case DirectionOut:
		return "egress"
	case DirectionIn:
		return "ingress"
	default:
		return "both directions"
	}
}

func describeTarget(address string, p partitionParams) string {
	if p.Protocol != "" && p.Port != 0 {
		return fmt.Sprintf("%s %s:%d", p.Protocol, address, p.Port)
	}
	if p.Protocol != "" {
		return fmt.Sprintf("%s to/from %s", p.Protocol, address)
	}
	return address
}

func parsePartitionParams(in map[string]any) (partitionParams, error) {
	var p partitionParams
	p.Direction = DirectionBoth

	if raw, ok := in["direction"]; ok {
		s, ok := raw.(string)
		if !ok {
			return p, fmt.Errorf("direction: expected string, got %T", raw)
		}
		switch Direction(s) {
		case DirectionIn, DirectionOut, DirectionBoth:
			p.Direction = Direction(s)
		default:
			return p, fmt.Errorf("direction: must be one of in, out, both; got %q", s)
		}
	}

	if raw, ok := in["protocol"]; ok {
		s, ok := raw.(string)
		if !ok {
			return p, fmt.Errorf("protocol: expected string, got %T", raw)
		}
		switch s {
		case "tcp", "udp", "icmp":
			p.Protocol = s
		case "":
			// empty == unset
		default:
			return p, fmt.Errorf("protocol: must be tcp, udp, or icmp; got %q", s)
		}
	}

	if raw, ok := in["port"]; ok {
		port, err := toPortInt(raw)
		if err != nil {
			return p, fmt.Errorf("port: %w", err)
		}
		if port < 1 || port > 65535 {
			return p, errors.New("port must be in 1..65535")
		}
		if p.Protocol == "" || p.Protocol == "icmp" {
			return p, errors.New("port requires protocol tcp or udp")
		}
		p.Port = port
	}

	return p, nil
}

func toPortInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int32:
		return int(n), nil
	case int64:
		return int(n), nil
	case uint:
		return int(n), nil
	case uint64:
		return int(n), nil
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("expected integer, got %v", n)
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("expected integer, got %T", v)
	}
}

func validateIPv4Address(s string) error {
	if strings.Contains(s, "/") {
		prefix, err := netip.ParsePrefix(s)
		if err != nil {
			return fmt.Errorf("invalid CIDR: %w", err)
		}
		if !prefix.Addr().Is4() {
			return errors.New("only IPv4 supported in v0.2.0")
		}
		return nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return fmt.Errorf("invalid IP: %w", err)
	}
	if !addr.Is4() {
		return errors.New("only IPv4 supported in v0.2.0")
	}
	return nil
}

func randomChainSuffix() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(b[:])), nil
}
