package network

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// IPTables abstracts the kernel-side iptables commands the partition and
// DNS-failure faults need. The production implementation in NewExecIPTables
// shells out to the system `iptables` binary; tests inject a fake.
//
// Every operation runs against the default `filter` table. IPv6 is out of
// scope for v0.2.0 (see ADR-0003).
type IPTables interface {
	// ChainExists reports whether the named user chain exists in the filter
	// table.
	ChainExists(ctx context.Context, name string) (bool, error)

	// NewChain creates a new user chain in the filter table.
	NewChain(ctx context.Context, name string) error

	// AppendRule appends a rule (the `-A <chain> <args>` form) to a chain.
	AppendRule(ctx context.Context, chain string, args ...string) error

	// InsertRule inserts a rule at position 1 of a built-in chain (the
	// `-I <chain> 1 <args>` form), so it runs before any rule already
	// installed by the operator or another tool.
	InsertRule(ctx context.Context, chain string, args ...string) error

	// DeleteRule deletes a rule by spec (the `-D <chain> <args>` form). It
	// returns nil when the rule does not exist, so revert is idempotent.
	DeleteRule(ctx context.Context, chain string, args ...string) error

	// FlushChain removes every rule from a chain.
	FlushChain(ctx context.Context, name string) error

	// DeleteChain deletes a user chain. The chain must be empty first.
	DeleteChain(ctx context.Context, name string) error
}

// xtablesLockWait is the value passed to `iptables -w`. Five seconds is
// generous for an interactive operator workflow and short enough that a
// genuinely stuck lock surfaces quickly.
const xtablesLockWait = "5"

type execIPTables struct {
	path string
}

// NewExecIPTables resolves the iptables binary on PATH. Returns an error
// if iptables cannot be found; the caller should treat that as a preflight
// failure.
func NewExecIPTables() (IPTables, error) {
	path, err := exec.LookPath("iptables")
	if err != nil {
		return nil, fmt.Errorf("iptables not found on PATH: %w", err)
	}
	return &execIPTables{path: path}, nil
}

func (t *execIPTables) ChainExists(ctx context.Context, name string) (bool, error) {
	if name == "" {
		return false, errors.New("chain name is empty")
	}
	// `-S <chain>` returns 0 if the chain exists and a non-zero exit with a
	// "No chain/target/match by that name" message otherwise.
	_, stderr, err := t.run(ctx, "-S", name)
	if err == nil {
		return true, nil
	}
	if strings.Contains(strings.ToLower(stderr), "no chain") {
		return false, nil
	}
	return false, fmt.Errorf("iptables -S %s: %w: %s", name, err, stderr)
}

func (t *execIPTables) NewChain(ctx context.Context, name string) error {
	_, _, err := t.run(ctx, "-N", name)
	return err
}

func (t *execIPTables) AppendRule(ctx context.Context, chain string, args ...string) error {
	all := append([]string{"-A", chain}, args...)
	_, _, err := t.run(ctx, all...)
	return err
}

func (t *execIPTables) InsertRule(ctx context.Context, chain string, args ...string) error {
	all := append([]string{"-I", chain, "1"}, args...)
	_, _, err := t.run(ctx, all...)
	return err
}

func (t *execIPTables) DeleteRule(ctx context.Context, chain string, args ...string) error {
	all := append([]string{"-D", chain}, args...)
	_, stderr, err := t.run(ctx, all...)
	if err == nil {
		return nil
	}
	// iptables prints "No chain/target/match by that name" if the rule or
	// chain is already gone; treat that as success so revert is idempotent.
	low := strings.ToLower(stderr)
	if strings.Contains(low, "does a matching rule exist") ||
		strings.Contains(low, "no chain/target/match by that name") ||
		strings.Contains(low, "bad rule") {
		return nil
	}
	return err
}

func (t *execIPTables) FlushChain(ctx context.Context, name string) error {
	_, _, err := t.run(ctx, "-F", name)
	return err
}

func (t *execIPTables) DeleteChain(ctx context.Context, name string) error {
	_, _, err := t.run(ctx, "-X", name)
	return err
}

func (t *execIPTables) run(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	full := append([]string{"-w", xtablesLockWait}, args...)
	cmd := exec.CommandContext(ctx, t.path, full...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if runErr := cmd.Run(); runErr != nil {
		return out.String(), errb.String(), fmt.Errorf("iptables %s: %w: %s",
			strings.Join(full, " "), runErr, bytes.TrimSpace(errb.Bytes()))
	}
	return out.String(), errb.String(), nil
}
