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

// These tests touch real iptables state. They require root, the `iptables`
// binary on PATH, and the IPv4 filter table being writeable. The target is
// the TEST-NET-1 range (192.0.2.0/24, RFC 5737) so the rules cannot
// possibly affect anything real on the host.

func iptablesAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("iptables"); err != nil {
		t.Skipf("integration test requires iptables on PATH")
	}
}

func iptablesListChain(t *testing.T, chain string) (string, bool) {
	t.Helper()
	out, err := exec.Command("iptables", "-w", "5", "-S", chain).CombinedOutput()
	if err != nil {
		return string(out), false
	}
	return string(out), true
}

func TestIntegration_PartitionApplyAndRevert(t *testing.T) {
	skipUnlessRoot(t)
	iptablesAvailable(t)

	ipt, err := network.NewExecIPTables()
	require.NoError(t, err)

	p := network.NewPartition(ipt, nil,
		network.WithPartitionSuffixFunc(func() (string, error) { return "INTGTEST", nil }),
	)

	spec := faults.FaultSpec{
		Kind:     network.FaultNamePartition,
		Target:   faults.Target{Address: "192.0.2.42"},
		Duration: 10 * time.Second,
		Parameters: map[string]any{
			"protocol":  "tcp",
			"port":      5432,
			"direction": "both",
		},
	}

	rev, err := p.Apply(context.Background(), spec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rev.Revert(context.Background()) })

	body, ok := iptablesListChain(t, "SCORPIUS_PART_INTGTEST")
	require.True(t, ok, "chain must exist after Apply; got: %s", body)
	assert.Contains(t, body, "192.0.2.42")
	assert.Contains(t, body, "DROP")

	output, _ := exec.Command("iptables", "-w", "5", "-S", "OUTPUT").CombinedOutput()
	assert.Contains(t, strings.ToLower(string(output)), strings.ToLower("SCORPIUS_PART_INTGTEST"))
	input, _ := exec.Command("iptables", "-w", "5", "-S", "INPUT").CombinedOutput()
	assert.Contains(t, strings.ToLower(string(input)), strings.ToLower("SCORPIUS_PART_INTGTEST"))

	require.NoError(t, rev.Revert(context.Background()))
	require.NoError(t, rev.Verify(context.Background()))

	_, exists := iptablesListChain(t, "SCORPIUS_PART_INTGTEST")
	assert.False(t, exists, "chain must be gone after Revert")
}

func TestIntegration_PartitionRevertIsIdempotent(t *testing.T) {
	skipUnlessRoot(t)
	iptablesAvailable(t)

	ipt, err := network.NewExecIPTables()
	require.NoError(t, err)

	p := network.NewPartition(ipt, nil,
		network.WithPartitionSuffixFunc(func() (string, error) { return "IDEM0001", nil }),
	)

	spec := faults.FaultSpec{
		Kind:       network.FaultNamePartition,
		Target:     faults.Target{Address: "192.0.2.99"},
		Duration:   10 * time.Second,
		Parameters: map[string]any{},
	}

	rev, err := p.Apply(context.Background(), spec)
	require.NoError(t, err)

	require.NoError(t, rev.Revert(context.Background()))
	require.NoError(t, rev.Revert(context.Background()))
	require.NoError(t, rev.Verify(context.Background()))
}
