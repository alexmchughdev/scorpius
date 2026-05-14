//go:build integration

package network_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/alexmchughdev/scorpius/internal/faults"
	"github.com/alexmchughdev/scorpius/internal/faults/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests touch real kernel state. They require root (CAP_NET_ADMIN),
// the `tc` and `ip` binaries on PATH, and the `dummy` kernel module loaded.
// They run inside the host network namespace against a freshly-created
// dummy interface that is torn down at test end. A namespace-isolated
// variant is a worthwhile follow-up but would require setns() from Go.

func skipUnlessRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("integration test requires root")
	}
}

func mustHave(t *testing.T, binaries ...string) {
	t.Helper()
	for _, b := range binaries {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("integration test requires %s on PATH", b)
		}
	}
}

func createDummy(t *testing.T) string {
	t.Helper()
	name := "scorpius-it-" + strings.ReplaceAll(t.Name(), "/", "-")
	if len(name) > 15 {
		// Linux interface names cap at 15 characters.
		name = name[:15]
	}

	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "%s: %s", strings.Join(args, " "), out)
	}

	_ = exec.Command("ip", "link", "delete", name).Run()
	run("ip", "link", "add", name, "type", "dummy")
	run("ip", "link", "set", name, "up")
	t.Cleanup(func() {
		_ = exec.Command("ip", "link", "delete", name).Run()
	})
	return name
}

func tcShow(t *testing.T, iface string) string {
	t.Helper()
	out, err := exec.Command("tc", "qdisc", "show", "dev", iface).CombinedOutput()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func TestIntegration_ApplyAndRevert(t *testing.T) {
	skipUnlessRoot(t)
	mustHave(t, "tc", "ip")

	iface := createDummy(t)

	tc, err := network.NewExecTC()
	require.NoError(t, err)

	l := network.New(tc, nil)

	spec := faults.FaultSpec{
		Kind:     network.FaultName,
		Target:   faults.Target{Interface: iface},
		Duration: 10 * time.Second,
		Parameters: map[string]any{
			"delay_ms":  50,
			"jitter_ms": 10,
		},
	}

	rev, err := l.Apply(context.Background(), spec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rev.Revert(context.Background()) })

	assert.Contains(t, tcShow(t, iface), "netem",
		"netem qdisc must be present after Apply")

	require.NoError(t, rev.Revert(context.Background()))

	out := tcShow(t, iface)
	assert.NotContains(t, out, "netem",
		"netem qdisc must be gone after Revert; got: %s", out)

	require.NoError(t, rev.Verify(context.Background()))
}

func TestIntegration_RefusesWithExistingQdisc(t *testing.T) {
	skipUnlessRoot(t)
	mustHave(t, "tc", "ip")

	iface := createDummy(t)

	// Pre-install an unrelated qdisc.
	require.NoError(t, exec.Command("tc", "qdisc", "add", "dev", iface, "root", "pfifo", "limit", "10").Run())
	t.Cleanup(func() { _ = exec.Command("tc", "qdisc", "del", "dev", iface, "root").Run() })

	tc, err := network.NewExecTC()
	require.NoError(t, err)
	l := network.New(tc, nil)

	spec := faults.FaultSpec{
		Kind:     network.FaultName,
		Target:   faults.Target{Interface: iface},
		Duration: 10 * time.Second,
		Parameters: map[string]any{
			"delay_ms": 50,
		},
	}

	rev, err := l.Apply(context.Background(), spec)
	require.Error(t, err)
	assert.Nil(t, rev)
	assert.Contains(t, err.Error(), "already has a qdisc")
}

func TestIntegration_RefusesUnknownInterface(t *testing.T) {
	skipUnlessRoot(t)
	mustHave(t, "tc", "ip")

	tc, err := network.NewExecTC()
	require.NoError(t, err)
	l := network.New(tc, nil)

	spec := faults.FaultSpec{
		Kind:     network.FaultName,
		Target:   faults.Target{Interface: "scorpius-it-nope"},
		Duration: 10 * time.Second,
		Parameters: map[string]any{
			"delay_ms": 50,
		},
	}

	rev, err := l.Apply(context.Background(), spec)
	require.Error(t, err)
	assert.Nil(t, rev)
	assert.Contains(t, err.Error(), "does not exist")
}
