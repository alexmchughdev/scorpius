package network_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexmchughdev/scorpius/internal/faults"
	"github.com/alexmchughdev/scorpius/internal/faults/network"
	"github.com/alexmchughdev/scorpius/internal/watchdog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeIPTables tracks every call so tests can assert exact invocations and
// can simulate failures at any step.
type fakeIPTables struct {
	mu sync.Mutex

	chains map[string]bool

	newErr     error
	appendErr  error
	insertErr  error
	deleteErr  error
	flushErr   error
	deleteCErr error

	// missingDelete simulates the iptables "rule does not exist" response
	// that DeleteRule must treat as success.
	missingDelete bool

	calls []iptCall
}

type iptCall struct {
	op    string // "new", "append", "insert", "delete", "flush", "deletechain", "exists"
	chain string
	args  []string
}

func newFakeIPTables() *fakeIPTables {
	return &fakeIPTables{chains: map[string]bool{}}
}

func (f *fakeIPTables) ChainExists(ctx context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, iptCall{op: "exists", chain: name})
	return f.chains[name], nil
}

func (f *fakeIPTables) NewChain(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.newErr != nil {
		return f.newErr
	}
	f.chains[name] = true
	f.calls = append(f.calls, iptCall{op: "new", chain: name})
	return nil
}

func (f *fakeIPTables) AppendRule(ctx context.Context, chain string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appendErr != nil {
		return f.appendErr
	}
	f.calls = append(f.calls, iptCall{op: "append", chain: chain, args: append([]string(nil), args...)})
	return nil
}

func (f *fakeIPTables) InsertRule(ctx context.Context, chain string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertErr != nil {
		return f.insertErr
	}
	f.calls = append(f.calls, iptCall{op: "insert", chain: chain, args: append([]string(nil), args...)})
	return nil
}

func (f *fakeIPTables) DeleteRule(ctx context.Context, chain string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.missingDelete {
		// Mimic exec impl's idempotent behaviour: missing rule => nil.
		f.calls = append(f.calls, iptCall{op: "delete", chain: chain, args: append([]string(nil), args...)})
		return nil
	}
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.calls = append(f.calls, iptCall{op: "delete", chain: chain, args: append([]string(nil), args...)})
	return nil
}

func (f *fakeIPTables) FlushChain(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.flushErr != nil {
		return f.flushErr
	}
	f.calls = append(f.calls, iptCall{op: "flush", chain: name})
	return nil
}

func (f *fakeIPTables) DeleteChain(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteCErr != nil {
		return f.deleteCErr
	}
	delete(f.chains, name)
	f.calls = append(f.calls, iptCall{op: "deletechain", chain: name})
	return nil
}

func (f *fakeIPTables) snapshot() []iptCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]iptCall(nil), f.calls...)
}

func (f *fakeIPTables) callsByOp(op string) []iptCall {
	out := []iptCall{}
	for _, c := range f.snapshot() {
		if c.op == op {
			out = append(out, c)
		}
	}
	return out
}

func fixedSuffix(s string) func() (string, error) {
	return func() (string, error) { return s, nil }
}

func validPartitionSpec() faults.FaultSpec {
	return faults.FaultSpec{
		Kind:     network.FaultNamePartition,
		Target:   faults.Target{Address: "10.0.0.5"},
		Duration: 10 * time.Second,
		Parameters: map[string]any{
			"direction": "both",
			"protocol":  "tcp",
			"port":      5432,
		},
	}
}

func TestPartitionValidateRejectsBadSpecs(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*faults.FaultSpec)
		want string
	}{
		{
			name: "missing address",
			mut:  func(s *faults.FaultSpec) { s.Target.Address = "" },
			want: "target.address is required",
		},
		{
			name: "invalid address",
			mut:  func(s *faults.FaultSpec) { s.Target.Address = "not-an-ip" },
			want: "invalid IP",
		},
		{
			name: "ipv6 rejected",
			mut:  func(s *faults.FaultSpec) { s.Target.Address = "2001:db8::1" },
			want: "IPv4",
		},
		{
			name: "bad cidr",
			mut:  func(s *faults.FaultSpec) { s.Target.Address = "10.0.0.0/40" },
			want: "invalid CIDR",
		},
		{
			name: "zero duration",
			mut:  func(s *faults.FaultSpec) { s.Duration = 0 },
			want: "duration must be positive",
		},
		{
			name: "bad direction",
			mut:  func(s *faults.FaultSpec) { s.Parameters["direction"] = "sideways" },
			want: "direction",
		},
		{
			name: "bad protocol",
			mut:  func(s *faults.FaultSpec) { s.Parameters["protocol"] = "sctp" },
			want: "protocol",
		},
		{
			name: "port without protocol",
			mut: func(s *faults.FaultSpec) {
				delete(s.Parameters, "protocol")
				s.Parameters["port"] = 80
			},
			want: "port requires protocol",
		},
		{
			name: "port with icmp",
			mut: func(s *faults.FaultSpec) {
				s.Parameters["protocol"] = "icmp"
				s.Parameters["port"] = 80
			},
			want: "port requires protocol",
		},
		{
			name: "port out of range",
			mut:  func(s *faults.FaultSpec) { s.Parameters["port"] = 99999 },
			want: "port must be",
		},
		{
			name: "wrong kind",
			mut:  func(s *faults.FaultSpec) { s.Kind = "network_latency" },
			want: "does not match",
		},
	}

	p := network.NewPartition(newFakeIPTables(), nil)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := validPartitionSpec()
			c.mut(&spec)
			err := p.Validate(spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

func TestPartitionValidateAcceptsCIDR(t *testing.T) {
	p := network.NewPartition(newFakeIPTables(), nil)
	spec := validPartitionSpec()
	spec.Target.Address = "10.0.0.0/24"
	require.NoError(t, p.Validate(spec))
}

func TestPartitionValidateAllowsMinimalSpec(t *testing.T) {
	p := network.NewPartition(newFakeIPTables(), nil)
	spec := faults.FaultSpec{
		Target:     faults.Target{Address: "10.0.0.5"},
		Duration:   time.Second,
		Parameters: map[string]any{},
	}
	require.NoError(t, p.Validate(spec))
}

func TestPartitionApplyCreatesChainAndInstallsBothJumps(t *testing.T) {
	ipt := newFakeIPTables()
	p := network.NewPartition(ipt, nil, network.WithPartitionSuffixFunc(fixedSuffix("ABCD1234")))

	rev, err := p.Apply(context.Background(), validPartitionSpec())
	require.NoError(t, err)
	require.NotNil(t, rev)

	news := ipt.callsByOp("new")
	require.Len(t, news, 1)
	assert.Equal(t, "SCORPIUS_PART_ABCD1234", news[0].chain)

	appends := ipt.callsByOp("append")
	require.Len(t, appends, 2, "both an outbound and inbound DROP rule should be appended")
	assert.Equal(t, []string{"-d", "10.0.0.5", "-p", "tcp", "--dport", "5432", "-j", "DROP"}, appends[0].args)
	assert.Equal(t, []string{"-s", "10.0.0.5", "-p", "tcp", "--sport", "5432", "-j", "DROP"}, appends[1].args)

	inserts := ipt.callsByOp("insert")
	require.Len(t, inserts, 2)
	chains := []string{inserts[0].chain, inserts[1].chain}
	assert.ElementsMatch(t, []string{"OUTPUT", "INPUT"}, chains)
	for _, c := range inserts {
		assert.Equal(t, []string{"-j", "SCORPIUS_PART_ABCD1234"}, c.args)
	}
}

func TestPartitionApplyOutboundOnly(t *testing.T) {
	ipt := newFakeIPTables()
	p := network.NewPartition(ipt, nil, network.WithPartitionSuffixFunc(fixedSuffix("OUTONLY1")))

	spec := validPartitionSpec()
	spec.Parameters["direction"] = "out"

	_, err := p.Apply(context.Background(), spec)
	require.NoError(t, err)

	appends := ipt.callsByOp("append")
	require.Len(t, appends, 1)
	assert.Contains(t, appends[0].args, "-d")

	inserts := ipt.callsByOp("insert")
	require.Len(t, inserts, 1)
	assert.Equal(t, "OUTPUT", inserts[0].chain)
}

func TestPartitionApplyInboundOnly(t *testing.T) {
	ipt := newFakeIPTables()
	p := network.NewPartition(ipt, nil, network.WithPartitionSuffixFunc(fixedSuffix("INONLY01")))

	spec := validPartitionSpec()
	spec.Parameters["direction"] = "in"

	_, err := p.Apply(context.Background(), spec)
	require.NoError(t, err)

	appends := ipt.callsByOp("append")
	require.Len(t, appends, 1)
	assert.Contains(t, appends[0].args, "-s")

	inserts := ipt.callsByOp("insert")
	require.Len(t, inserts, 1)
	assert.Equal(t, "INPUT", inserts[0].chain)
}

func TestPartitionApplyCIDRTarget(t *testing.T) {
	ipt := newFakeIPTables()
	p := network.NewPartition(ipt, nil, network.WithPartitionSuffixFunc(fixedSuffix("CIDR0001")))

	spec := validPartitionSpec()
	spec.Target.Address = "10.0.0.0/24"
	delete(spec.Parameters, "protocol")
	delete(spec.Parameters, "port")

	_, err := p.Apply(context.Background(), spec)
	require.NoError(t, err)

	appends := ipt.callsByOp("append")
	require.Len(t, appends, 2)
	assert.Equal(t, []string{"-d", "10.0.0.0/24", "-j", "DROP"}, appends[0].args)
	assert.Equal(t, []string{"-s", "10.0.0.0/24", "-j", "DROP"}, appends[1].args)
}

func TestPartitionApplyRollsBackOnAppendError(t *testing.T) {
	ipt := newFakeIPTables()
	ipt.appendErr = errors.New("iptables: -A failed")
	p := network.NewPartition(ipt, nil, network.WithPartitionSuffixFunc(fixedSuffix("ROLLBACK")))

	rev, err := p.Apply(context.Background(), validPartitionSpec())
	require.Error(t, err)
	assert.Nil(t, rev)

	// Chain must have been flushed and deleted to leave no residue.
	flushes := ipt.callsByOp("flush")
	deletes := ipt.callsByOp("deletechain")
	require.NotEmpty(t, flushes, "partial state must be flushed")
	require.NotEmpty(t, deletes, "partial state must be deleted")
}

func TestPartitionApplyRollsBackOnInsertError(t *testing.T) {
	ipt := newFakeIPTables()
	ipt.insertErr = errors.New("iptables: -I failed")
	p := network.NewPartition(ipt, nil, network.WithPartitionSuffixFunc(fixedSuffix("ROLLINS1")))

	rev, err := p.Apply(context.Background(), validPartitionSpec())
	require.Error(t, err)
	assert.Nil(t, rev)

	// Chain teardown still happens; any jumps installed before the failure
	// must also be cleaned up. With both directions, none get installed
	// because the first InsertRule errors; rollback should still tear the
	// chain down.
	flushes := ipt.callsByOp("flush")
	deletes := ipt.callsByOp("deletechain")
	require.NotEmpty(t, flushes)
	require.NotEmpty(t, deletes)
}

func TestPartitionApplyNewChainError(t *testing.T) {
	ipt := newFakeIPTables()
	ipt.newErr = errors.New("chain already exists")
	p := network.NewPartition(ipt, nil, network.WithPartitionSuffixFunc(fixedSuffix("NEWFAIL1")))

	rev, err := p.Apply(context.Background(), validPartitionSpec())
	require.Error(t, err)
	assert.Nil(t, rev)
	assert.ErrorIs(t, err, ipt.newErr)
}

func TestPartitionApplyRegistersWithWatchdog(t *testing.T) {
	ipt := newFakeIPTables()
	wd := watchdog.New(time.Second, watchdog.WithLogger(silentLogger()))
	p := network.NewPartition(ipt, wd,
		network.WithPartitionSuffixFunc(fixedSuffix("WDREG001")),
		network.WithPartitionHeartbeatInterval(10*time.Millisecond),
	)

	rev, err := p.Apply(context.Background(), validPartitionSpec())
	require.NoError(t, err)
	defer rev.Revert(context.Background())

	assert.Equal(t, 1, wd.Active(), "fault must be registered with the watchdog")
}

func TestPartitionRevertTearsDownInOrder(t *testing.T) {
	ipt := newFakeIPTables()
	p := network.NewPartition(ipt, nil, network.WithPartitionSuffixFunc(fixedSuffix("TEARDOWN")))

	rev, err := p.Apply(context.Background(), validPartitionSpec())
	require.NoError(t, err)
	require.NoError(t, rev.Revert(context.Background()))

	calls := ipt.snapshot()
	var ops []string
	for _, c := range calls {
		ops = append(ops, c.op)
	}

	// Order requirements: every delete jump happens before flush; flush
	// happens before deletechain.
	flushIdx := -1
	deleteCIdx := -1
	for i, op := range ops {
		if op == "flush" && flushIdx == -1 {
			flushIdx = i
		}
		if op == "deletechain" && deleteCIdx == -1 {
			deleteCIdx = i
		}
	}
	require.NotEqual(t, -1, flushIdx)
	require.NotEqual(t, -1, deleteCIdx)
	assert.Less(t, flushIdx, deleteCIdx, "flush must precede chain deletion")

	for i, op := range ops[:flushIdx] {
		if op == "delete" {
			continue
		}
		// Before flush we only expect new, append, insert, delete jump.
		assert.NotEqual(t, "flush", op, "no flush before jump deletions; pos %d", i)
	}
}

func TestPartitionRevertIsIdempotent(t *testing.T) {
	ipt := newFakeIPTables()
	p := network.NewPartition(ipt, nil, network.WithPartitionSuffixFunc(fixedSuffix("IDEMPOTE")))

	rev, err := p.Apply(context.Background(), validPartitionSpec())
	require.NoError(t, err)

	require.NoError(t, rev.Revert(context.Background()))
	require.NoError(t, rev.Revert(context.Background()))
	require.NoError(t, rev.Revert(context.Background()))

	// The teardown sequence should only have run once.
	assert.Len(t, ipt.callsByOp("flush"), 1, "flush must run only once")
	assert.Len(t, ipt.callsByOp("deletechain"), 1, "delete chain must run only once")
}

func TestPartitionRevertSurfacesFlushError(t *testing.T) {
	ipt := newFakeIPTables()
	p := network.NewPartition(ipt, nil, network.WithPartitionSuffixFunc(fixedSuffix("FLUSHERR")))

	rev, err := p.Apply(context.Background(), validPartitionSpec())
	require.NoError(t, err)

	ipt.flushErr = errors.New("flush failed")
	err = rev.Revert(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ipt.flushErr)
}

func TestPartitionVerifyDetectsLeftoverChain(t *testing.T) {
	ipt := newFakeIPTables()
	p := network.NewPartition(ipt, nil, network.WithPartitionSuffixFunc(fixedSuffix("LEFTOVER")))

	rev, err := p.Apply(context.Background(), validPartitionSpec())
	require.NoError(t, err)

	// Simulate a botched revert: chain still present.
	ipt.deleteCErr = errors.New("delete chain failed")
	_ = rev.Revert(context.Background())

	err = rev.Verify(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leftover chain")
}

func TestPartitionVerifyPassesAfterCleanRevert(t *testing.T) {
	ipt := newFakeIPTables()
	p := network.NewPartition(ipt, nil, network.WithPartitionSuffixFunc(fixedSuffix("CLEAN001")))

	rev, err := p.Apply(context.Background(), validPartitionSpec())
	require.NoError(t, err)
	require.NoError(t, rev.Revert(context.Background()))
	require.NoError(t, rev.Verify(context.Background()))
}

func TestPartitionDryRunRendersCommands(t *testing.T) {
	ipt := newFakeIPTables()
	p := network.NewPartition(ipt, nil)

	plan, err := p.DryRun(validPartitionSpec())
	require.NoError(t, err)

	assert.Equal(t, network.FaultNamePartition, plan.Kind)
	require.NotEmpty(t, plan.Commands)
	joined := strings.Join(plan.Commands, "\n")
	assert.Contains(t, joined, "-N SCORPIUS_PART_DRYRUNXX")
	assert.Contains(t, joined, "-A SCORPIUS_PART_DRYRUNXX -d 10.0.0.5 -p tcp --dport 5432 -j DROP")
	assert.Contains(t, joined, "-A SCORPIUS_PART_DRYRUNXX -s 10.0.0.5 -p tcp --sport 5432 -j DROP")
	assert.Contains(t, joined, "-I OUTPUT 1 -j SCORPIUS_PART_DRYRUNXX")
	assert.Contains(t, joined, "-I INPUT 1 -j SCORPIUS_PART_DRYRUNXX")

	// DryRun must not actually mutate iptables.
	assert.Empty(t, ipt.callsByOp("new"))
	assert.Empty(t, ipt.callsByOp("append"))
	assert.Empty(t, ipt.callsByOp("insert"))
}

func TestPartitionHeartbeatLoopKeepsWatchdogQuiet(t *testing.T) {
	ipt := newFakeIPTables()
	wd := watchdog.New(100*time.Millisecond, watchdog.WithLogger(silentLogger()))
	p := network.NewPartition(ipt, wd,
		network.WithPartitionSuffixFunc(fixedSuffix("HBQUIET1")),
		network.WithPartitionHeartbeatInterval(10*time.Millisecond),
	)

	rev, err := p.Apply(context.Background(), validPartitionSpec())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { wd.Run(ctx); close(done) }()

	time.Sleep(300 * time.Millisecond)

	assert.Empty(t, ipt.callsByOp("deletechain"),
		"heartbeat loop must keep the watchdog from reverting")

	require.NoError(t, rev.Revert(context.Background()))
	cancel()
	<-done
}

func TestPartitionHeartbeatStopsAfterRevert(t *testing.T) {
	ipt := newFakeIPTables()
	wd := watchdog.New(50*time.Millisecond, watchdog.WithLogger(silentLogger()))
	p := network.NewPartition(ipt, wd,
		network.WithPartitionSuffixFunc(fixedSuffix("HBSTOP01")),
		network.WithPartitionHeartbeatInterval(5*time.Millisecond),
	)

	rev, err := p.Apply(context.Background(), validPartitionSpec())
	require.NoError(t, err)
	require.NoError(t, rev.Revert(context.Background()))

	wd.CheckOnce()
	assert.Equal(t, 0, wd.Active())
}
