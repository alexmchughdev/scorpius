//go:build integration

package network_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/alexmchughdev/scorpius/internal/faults"
	"github.com/alexmchughdev/scorpius/internal/faults/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegration_PacketLossApplyAndRevert(t *testing.T) {
	skipUnlessRoot(t)
	mustHave(t, "tc", "ip")

	iface := createDummy(t)

	tc, err := network.NewExecTC()
	require.NoError(t, err)

	pl := network.NewPacketLoss(tc, nil)

	spec := faults.FaultSpec{
		Kind:     network.FaultNamePacketLoss,
		Target:   faults.Target{Interface: iface},
		Duration: 10 * time.Second,
		Parameters: map[string]any{
			"loss_percent":        5.0,
			"correlation_percent": 25.0,
		},
	}

	rev, err := pl.Apply(context.Background(), spec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rev.Revert(context.Background()) })

	show := tcShow(t, iface)
	assert.Contains(t, show, "netem", "netem qdisc must be present after Apply")
	assert.Contains(t, strings.ToLower(show), "loss",
		"netem qdisc must show loss after Apply; got: %s", show)

	require.NoError(t, rev.Revert(context.Background()))

	out := tcShow(t, iface)
	assert.NotContains(t, out, "netem",
		"netem qdisc must be gone after Revert; got: %s", out)

	require.NoError(t, rev.Verify(context.Background()))
}

func TestIntegration_PacketLossRefusesWithExistingQdisc(t *testing.T) {
	skipUnlessRoot(t)
	mustHave(t, "tc", "ip")

	iface := createDummy(t)

	require.NoError(t, exec.Command("tc", "qdisc", "add", "dev", iface, "root", "pfifo", "limit", "10").Run())
	t.Cleanup(func() { _ = exec.Command("tc", "qdisc", "del", "dev", iface, "root").Run() })

	tc, err := network.NewExecTC()
	require.NoError(t, err)
	pl := network.NewPacketLoss(tc, nil)

	spec := faults.FaultSpec{
		Kind:     network.FaultNamePacketLoss,
		Target:   faults.Target{Interface: iface},
		Duration: 10 * time.Second,
		Parameters: map[string]any{
			"loss_percent": 5.0,
		},
	}

	rev, err := pl.Apply(context.Background(), spec)
	require.Error(t, err)
	assert.Nil(t, rev)
	assert.Contains(t, err.Error(), "already has a qdisc")
}
