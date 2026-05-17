package network_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alexmchughdev/scorpius/internal/faults"
	"github.com/alexmchughdev/scorpius/internal/faults/network"
	"github.com/alexmchughdev/scorpius/internal/watchdog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validLossSpec() faults.FaultSpec {
	return faults.FaultSpec{
		Kind:     network.FaultNamePacketLoss,
		Target:   faults.Target{Interface: "lo"},
		Duration: 10 * time.Second,
		Parameters: map[string]any{
			"loss_percent":        5.0,
			"correlation_percent": 25.0,
		},
	}
}

func TestPacketLossValidateRejectsBadSpecs(t *testing.T) {
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
			name: "missing loss",
			mut:  func(s *faults.FaultSpec) { delete(s.Parameters, "loss_percent") },
			want: "loss_percent is required",
		},
		{
			name: "zero loss",
			mut:  func(s *faults.FaultSpec) { s.Parameters["loss_percent"] = 0.0 },
			want: "loss_percent must be positive",
		},
		{
			name: "negative loss",
			mut:  func(s *faults.FaultSpec) { s.Parameters["loss_percent"] = -1.0 },
			want: "loss_percent must be positive",
		},
		{
			name: "loss over 100",
			mut:  func(s *faults.FaultSpec) { s.Parameters["loss_percent"] = 101.0 },
			want: "loss_percent must be <= 100",
		},
		{
			name: "negative correlation",
			mut:  func(s *faults.FaultSpec) { s.Parameters["correlation_percent"] = -1.0 },
			want: "correlation_percent must be non-negative",
		},
		{
			name: "correlation over 100",
			mut:  func(s *faults.FaultSpec) { s.Parameters["correlation_percent"] = 101.0 },
			want: "correlation_percent must be <= 100",
		},
		{
			name: "non-numeric loss",
			mut:  func(s *faults.FaultSpec) { s.Parameters["loss_percent"] = "5" },
			want: "loss_percent",
		},
		{
			name: "wrong kind",
			mut:  func(s *faults.FaultSpec) { s.Kind = "network_latency" },
			want: "does not match",
		},
	}

	pl := network.NewPacketLoss(newFakeTC(), nil)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := validLossSpec()
			c.mut(&spec)
			err := pl.Validate(spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

func TestPacketLossValidateAcceptsValidSpec(t *testing.T) {
	pl := network.NewPacketLoss(newFakeTC(), nil)
	require.NoError(t, pl.Validate(validLossSpec()))
}

func TestPacketLossValidateAllowsEmptyKind(t *testing.T) {
	pl := network.NewPacketLoss(newFakeTC(), nil)
	spec := validLossSpec()
	spec.Kind = ""
	require.NoError(t, pl.Validate(spec))
}

func TestPacketLossApplyFailsIfInterfaceMissing(t *testing.T) {
	tc := newFakeTC()
	pl := network.NewPacketLoss(tc, nil)

	rev, err := pl.Apply(context.Background(), validLossSpec())
	require.Error(t, err)
	assert.Nil(t, rev)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestPacketLossApplyFailsIfQdiscAlreadyPresent(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	tc.showOut["lo"] = []byte("qdisc htb 1: root refcnt 2\n")
	pl := network.NewPacketLoss(tc, nil)

	rev, err := pl.Apply(context.Background(), validLossSpec())
	require.Error(t, err)
	assert.Nil(t, rev)
	assert.Contains(t, err.Error(), "already has a qdisc")
	assert.Contains(t, err.Error(), "htb")
}

func TestPacketLossApplyAddsNetemQdisc(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	pl := network.NewPacketLoss(tc, nil)

	rev, err := pl.Apply(context.Background(), validLossSpec())
	require.NoError(t, err)
	require.NotNil(t, rev)

	adds, _ := tc.snapshot()
	require.Len(t, adds, 1)
	assert.Equal(t, "lo", adds[0].iface)
	assert.Equal(t, []string{"netem", "loss", "5%", "25%"}, adds[0].args)
}

func TestPacketLossApplyAddsNetemWithoutCorrelation(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	pl := network.NewPacketLoss(tc, nil)

	spec := validLossSpec()
	delete(spec.Parameters, "correlation_percent")

	_, err := pl.Apply(context.Background(), spec)
	require.NoError(t, err)

	adds, _ := tc.snapshot()
	require.Len(t, adds, 1)
	assert.Equal(t, []string{"netem", "loss", "5%"}, adds[0].args)
}

func TestPacketLossApplyAcceptsIntegerPercent(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	pl := network.NewPacketLoss(tc, nil)

	spec := validLossSpec()
	spec.Parameters["loss_percent"] = 10
	delete(spec.Parameters, "correlation_percent")

	_, err := pl.Apply(context.Background(), spec)
	require.NoError(t, err)

	adds, _ := tc.snapshot()
	require.Len(t, adds, 1)
	assert.Equal(t, []string{"netem", "loss", "10%"}, adds[0].args)
}

func TestPacketLossApplyAddErrorPropagates(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	tc.addErr = errors.New("tc add failed")
	pl := network.NewPacketLoss(tc, nil)

	rev, err := pl.Apply(context.Background(), validLossSpec())
	require.Error(t, err)
	assert.Nil(t, rev)
	assert.ErrorIs(t, err, tc.addErr)
}

func TestPacketLossApplyRegistersWithWatchdog(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	wd := watchdog.New(time.Second, watchdog.WithLogger(silentLogger()))
	pl := network.NewPacketLoss(tc, wd, network.WithPacketLossHeartbeatInterval(10*time.Millisecond))

	rev, err := pl.Apply(context.Background(), validLossSpec())
	require.NoError(t, err)
	require.NotNil(t, rev)
	defer rev.Revert(context.Background())

	assert.Equal(t, 1, wd.Active(), "fault must be registered with the watchdog")
}

func TestPacketLossRevertRemovesQdiscAndDeregisters(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	wd := watchdog.New(time.Second, watchdog.WithLogger(silentLogger()))
	pl := network.NewPacketLoss(tc, wd, network.WithPacketLossHeartbeatInterval(10*time.Millisecond))

	rev, err := pl.Apply(context.Background(), validLossSpec())
	require.NoError(t, err)
	require.NoError(t, rev.Revert(context.Background()))

	_, dels := tc.snapshot()
	require.Len(t, dels, 1)
	assert.Equal(t, "lo", dels[0])
	assert.Equal(t, 0, wd.Active(), "fault must be deregistered after Revert")
}

func TestPacketLossRevertIsIdempotent(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	pl := network.NewPacketLoss(tc, nil)

	rev, err := pl.Apply(context.Background(), validLossSpec())
	require.NoError(t, err)

	require.NoError(t, rev.Revert(context.Background()))
	require.NoError(t, rev.Revert(context.Background()))
	require.NoError(t, rev.Revert(context.Background()))

	_, dels := tc.snapshot()
	assert.Len(t, dels, 1, "tc del must run only once")
}

func TestPacketLossRevertSurfacesDelError(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	pl := network.NewPacketLoss(tc, nil)

	rev, err := pl.Apply(context.Background(), validLossSpec())
	require.NoError(t, err)

	tc.delErr = errors.New("tc del failed")
	err = rev.Revert(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, tc.delErr)
}

func TestPacketLossVerifyDetectsLeftoverNetem(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	pl := network.NewPacketLoss(tc, nil)

	rev, err := pl.Apply(context.Background(), validLossSpec())
	require.NoError(t, err)

	tc.delErr = errors.New("tc del failed")
	_ = rev.Revert(context.Background())

	err = rev.Verify(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leftover netem")
}

func TestPacketLossVerifyPassesAfterCleanRevert(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true
	pl := network.NewPacketLoss(tc, nil)

	rev, err := pl.Apply(context.Background(), validLossSpec())
	require.NoError(t, err)
	require.NoError(t, rev.Revert(context.Background()))
	require.NoError(t, rev.Verify(context.Background()))
}

func TestPacketLossDryRunRendersCommands(t *testing.T) {
	tc := newFakeTC()
	pl := network.NewPacketLoss(tc, nil)

	plan, err := pl.DryRun(validLossSpec())
	require.NoError(t, err)

	assert.Equal(t, network.FaultNamePacketLoss, plan.Kind)
	require.Len(t, plan.Commands, 3)
	assert.Equal(t, "tc qdisc show dev lo", plan.Commands[0])
	assert.Equal(t, "tc qdisc add dev lo root netem loss 5% 25%", plan.Commands[1])
	assert.Equal(t, "tc qdisc del dev lo root", plan.Commands[2])

	adds, dels := tc.snapshot()
	assert.Empty(t, adds, "DryRun must not touch the kernel")
	assert.Empty(t, dels, "DryRun must not touch the kernel")
}

func TestPacketLossDryRunFractionalPercent(t *testing.T) {
	tc := newFakeTC()
	pl := network.NewPacketLoss(tc, nil)

	spec := validLossSpec()
	spec.Parameters["loss_percent"] = 0.5
	delete(spec.Parameters, "correlation_percent")

	plan, err := pl.DryRun(spec)
	require.NoError(t, err)

	require.True(t, strings.Contains(plan.Commands[1], "netem loss 0.5%"),
		"fractional percent should render without truncation; got %q", plan.Commands[1])
}

func TestPacketLossHeartbeatLoopKeepsWatchdogQuiet(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true

	wd := watchdog.New(100*time.Millisecond, watchdog.WithLogger(silentLogger()))
	pl := network.NewPacketLoss(tc, wd, network.WithPacketLossHeartbeatInterval(10*time.Millisecond))

	rev, err := pl.Apply(context.Background(), validLossSpec())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { wd.Run(ctx); close(done) }()

	time.Sleep(300 * time.Millisecond)

	_, dels := tc.snapshot()
	assert.Empty(t, dels, "heartbeat loop must keep the watchdog from reverting")

	require.NoError(t, rev.Revert(context.Background()))
	cancel()
	<-done
}

func TestPacketLossHeartbeatStopsAfterRevert(t *testing.T) {
	tc := newFakeTC()
	tc.exists["lo"] = true

	wd := watchdog.New(50*time.Millisecond, watchdog.WithLogger(silentLogger()))
	pl := network.NewPacketLoss(tc, wd, network.WithPacketLossHeartbeatInterval(5*time.Millisecond))

	rev, err := pl.Apply(context.Background(), validLossSpec())
	require.NoError(t, err)

	require.NoError(t, rev.Revert(context.Background()))

	wd.CheckOnce()
	assert.Equal(t, 0, wd.Active())
}
