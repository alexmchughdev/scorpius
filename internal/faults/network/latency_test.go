package network_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
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

type fakeTC struct {
	mu sync.Mutex

	exists  map[string]bool
	existsE error

	showOut map[string][]byte
	showErr error

	addErr error
	delErr error

	addCalls []addCall
	delCalls []string
}

type addCall struct {
	iface string
	args  []string
}

func newFakeTC() *fakeTC {
	return &fakeTC{
		exists:  map[string]bool{},
		showOut: map[string][]byte{},
	}
}

func (f *fakeTC) InterfaceExists(iface string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exists[iface], f.existsE
}

func (f *fakeTC) Show(ctx context.Context, iface string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.showErr != nil {
		return nil, f.showErr
	}
	out, ok := f.showOut[iface]
	if !ok {
		return []byte("qdisc noqueue 0: root refcnt 2\n"), nil
	}
	return out, nil
}

func (f *fakeTC) AddRoot(ctx context.Context, iface string, qdiscArgs ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return f.addErr
	}
	f.addCalls = append(f.addCalls, addCall{iface: iface, args: append([]string(nil), qdiscArgs...)})
	rendered := "qdisc " + strings.Join(qdiscArgs, " ") + " 8001: root\n"
	f.showOut[iface] = []byte(rendered)
	return nil
}

func (f *fakeTC) DelRoot(ctx context.Context, iface string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.delErr != nil {
		return f.delErr
	}
	f.delCalls = append(f.delCalls, iface)
	f.showOut[iface] = []byte("qdisc noqueue 0: root refcnt 2\n")
	return nil
}

func (f *fakeTC) snapshot() ([]addCall, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]addCall(nil), f.addCalls...), append([]string(nil), f.delCalls...)
}

func silentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func validSpec() faults.FaultSpec {
	return faults.FaultSpec{
		Kind:     network.FaultName,
		Target:   faults.Target{Interface: "lo"},
		Duration: 10 * time.Second,
		Parameters: map[string]any{
			"delay_ms":  100,
			"jitter_ms": 20,
		},
	}
}

func TestValidateRejectsBadSpecs(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*faults.FaultSpec)
		want string
	}{
		{
			name: "missing interface",
			mut:  func(s *faults.FaultSpec) { s.Target.Interface = "" },
			want: "target.interface is required",
		},
		{
			name: "zero duration",
			mut:  func(s *faults.FaultSpec) { s.Duration = 0 },
			want: "duration must be positive",
		},
		{
			name: "missing delay",
			mut:  func(s *faults.FaultSpec) { delete(s.Parameters, "delay_ms") },
			want: "delay_ms is required",
		},
		{
			name: "negative delay",
			mut:  func(s *faults.FaultSpec) { s.Parameters["delay_ms"] = -10 },
			want: "delay_ms must be positive",
		},
		{
			name: "negative jitter",
			mut:  func(s *faults.FaultSpec) { s.Parameters["jitter_ms"] = -1 },
			want: "jitter_ms must be non-negative",
		},
		{
			name: "non-numeric delay",
			mut:  func(s *faults.FaultSpec) { s.Parameters["delay_ms"] = "100" },
			want: "delay_ms",
		},
		{
			name: "wrong kind",
			mut:  func(s *faults.FaultSpec) { s.Kind = "packet_loss" },
			want: "does not match",
		},
	}

	l := network.New(newFakeTC(), nil)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := validSpec()
			c.mut(&spec)
			err := l.Validate(spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

func TestValidateAcceptsValidSpec(t *testing.T) {
	l := network.New(newFakeTC(), nil)
	require.NoError(t, l.Validate(validSpec()))
}

func TestValidateAllowsEmptyKind(t *testing.T) {
	l := network.New(newFakeTC(), nil)
	spec := validSpec()
	spec.Kind = ""
	require.NoError(t, l.Validate(spec))
}

func TestApplyFailsIfInterfaceMissing(t *testing.T) {
	tc := newFakeTC()
	// lo not in exists map → InterfaceExists returns false.
	l := network.New(tc, nil)

	rev, err := l.Apply(context.Background(), validSpec())
	require.Error(t, err)
	assert.Nil(t, rev)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestApplyFailsIfQdiscAlreadyPresent(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	tc.showOut["lo"] = []byte("qdisc htb 1: root refcnt 2\n")
	l := network.New(tc, nil)

	rev, err := l.Apply(context.Background(), validSpec())
	require.Error(t, err)
	assert.Nil(t, rev)
	assert.Contains(t, err.Error(), "already has a qdisc")
	assert.Contains(t, err.Error(), "htb")
}

func TestApplyIgnoresDefaultQdiscs(t *testing.T) {
	cases := []string{
		"qdisc noqueue 0: root refcnt 2\n",
		"qdisc pfifo_fast 0: root refcnt 2 bands 3\n",
		"qdisc mq 0: root\n",
		"qdisc fq_codel 0: root refcnt 2\n",
		"",
	}

	for _, show := range cases {
		t.Run(strings.TrimSpace(show), func(t *testing.T) {
			tc := newFakeTC()
			tc.exists["lo"] = true
			tc.showOut["lo"] = []byte(show)
			l := network.New(tc, nil)

			rev, err := l.Apply(context.Background(), validSpec())
			require.NoError(t, err)
			require.NotNil(t, rev)
			require.NoError(t, rev.Revert(context.Background()))
		})
	}
}

func TestApplyAddsNetemQdisc(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	l := network.New(tc, nil)

	rev, err := l.Apply(context.Background(), validSpec())
	require.NoError(t, err)
	require.NotNil(t, rev)

	adds, _ := tc.snapshot()
	require.Len(t, adds, 1)
	assert.Equal(t, "lo", adds[0].iface)
	assert.Equal(t, []string{"netem", "delay", "100ms", "20ms"}, adds[0].args)
}

func TestApplyAddsNetemWithoutJitter(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	l := network.New(tc, nil)

	spec := validSpec()
	delete(spec.Parameters, "jitter_ms")

	_, err := l.Apply(context.Background(), spec)
	require.NoError(t, err)

	adds, _ := tc.snapshot()
	require.Len(t, adds, 1)
	assert.Equal(t, []string{"netem", "delay", "100ms"}, adds[0].args)
}

func TestApplyAddErrorPropagates(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	tc.addErr = errors.New("tc add failed")
	l := network.New(tc, nil)

	rev, err := l.Apply(context.Background(), validSpec())
	require.Error(t, err)
	assert.Nil(t, rev)
	assert.ErrorIs(t, err, tc.addErr)
}

func TestApplyRegistersWithWatchdog(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	wd := watchdog.New(time.Second, watchdog.WithLogger(silentLogger()))
	l := network.New(tc, wd, network.WithHeartbeatInterval(10*time.Millisecond))

	rev, err := l.Apply(context.Background(), validSpec())
	require.NoError(t, err)
	require.NotNil(t, rev)
	defer rev.Revert(context.Background())

	assert.Equal(t, 1, wd.Active(), "fault must be registered with the watchdog")
}

func TestRevertRemovesQdiscAndDeregisters(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	wd := watchdog.New(time.Second, watchdog.WithLogger(silentLogger()))
	l := network.New(tc, wd, network.WithHeartbeatInterval(10*time.Millisecond))

	rev, err := l.Apply(context.Background(), validSpec())
	require.NoError(t, err)
	require.NoError(t, rev.Revert(context.Background()))

	_, dels := tc.snapshot()
	require.Len(t, dels, 1)
	assert.Equal(t, "lo", dels[0])
	assert.Equal(t, 0, wd.Active(), "fault must be deregistered after Revert")
}

func TestRevertIsIdempotent(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	l := network.New(tc, nil)

	rev, err := l.Apply(context.Background(), validSpec())
	require.NoError(t, err)

	require.NoError(t, rev.Revert(context.Background()))
	require.NoError(t, rev.Revert(context.Background()))
	require.NoError(t, rev.Revert(context.Background()))

	_, dels := tc.snapshot()
	assert.Len(t, dels, 1, "tc del must run only once")
}

func TestRevertSurfacesDelError(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	l := network.New(tc, nil)

	rev, err := l.Apply(context.Background(), validSpec())
	require.NoError(t, err)

	tc.delErr = errors.New("tc del failed")
	err = rev.Revert(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, tc.delErr)
}

func TestVerifyDetectsLeftoverNetem(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	l := network.New(tc, nil)

	rev, err := l.Apply(context.Background(), validSpec())
	require.NoError(t, err)

	// Simulate revert failure: leftover netem qdisc.
	tc.delErr = errors.New("tc del failed")
	_ = rev.Revert(context.Background())

	err = rev.Verify(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leftover netem")
}

func TestVerifyPassesAfterCleanRevert(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	l := network.New(tc, nil)

	rev, err := l.Apply(context.Background(), validSpec())
	require.NoError(t, err)
	require.NoError(t, rev.Revert(context.Background()))
	require.NoError(t, rev.Verify(context.Background()))
}

func TestDryRunRendersCommands(t *testing.T) {
	tc := newFakeTC()
	l := network.New(tc, nil)

	plan, err := l.DryRun(validSpec())
	require.NoError(t, err)

	assert.Equal(t, network.FaultName, plan.Kind)
	require.Len(t, plan.Commands, 3)
	assert.Equal(t, "tc qdisc show dev lo", plan.Commands[0])
	assert.Equal(t, "tc qdisc add dev lo root netem delay 100ms 20ms", plan.Commands[1])
	assert.Equal(t, "tc qdisc del dev lo root", plan.Commands[2])

	adds, dels := tc.snapshot()
	assert.Empty(t, adds, "DryRun must not touch the kernel")
	assert.Empty(t, dels, "DryRun must not touch the kernel")
}

func TestHeartbeatLoopKeepsWatchdogQuiet(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true

	wd := watchdog.New(100*time.Millisecond, watchdog.WithLogger(silentLogger()))
	l := network.New(tc, wd, network.WithHeartbeatInterval(10*time.Millisecond))

	rev, err := l.Apply(context.Background(), validSpec())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { wd.Run(ctx); close(done) }()

	// Let the watchdog run for several thresholds; heartbeats should
	// keep it from triggering.
	time.Sleep(300 * time.Millisecond)

	_, dels := tc.snapshot()
	assert.Empty(t, dels, "heartbeat loop must keep the watchdog from reverting")

	require.NoError(t, rev.Revert(context.Background()))
	cancel()
	<-done
}

func TestHeartbeatStopsAfterRevert(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true

	wd := watchdog.New(50*time.Millisecond, watchdog.WithLogger(silentLogger()))
	l := network.New(tc, wd, network.WithHeartbeatInterval(5*time.Millisecond))

	rev, err := l.Apply(context.Background(), validSpec())
	require.NoError(t, err)

	require.NoError(t, rev.Revert(context.Background()))

	// After Revert, the heartbeat goroutine should have exited. Running
	// the watchdog now must not see the fault.
	wd.CheckOnce()
	assert.Equal(t, 0, wd.Active())
}
