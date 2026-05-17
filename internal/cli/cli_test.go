package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/alexmchughdev/scorpius/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecuteHelpExitsZero(t *testing.T) {
	out, _ := capture(t, func() {
		code := cli.Execute([]string{"--help"})
		assert.Zero(t, code)
	})
	assert.Contains(t, out, "scorpius injects controlled failures")
}

func TestExecuteUnknownCommandExitsNonZero(t *testing.T) {
	_, errOut := capture(t, func() {
		code := cli.Execute([]string{"definitely-not-a-command"})
		assert.NotZero(t, code)
	})
	assert.Contains(t, errOut, "error:")
}

func TestStatusJSONOutput(t *testing.T) {
	stdout, _ := capture(t, func() {
		code := cli.Execute([]string{"status", "--output", "json"})
		require.Zero(t, code)
	})

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &got))

	active, ok := got["active"].([]any)
	require.True(t, ok)
	assert.Empty(t, active)
	assert.Contains(t, got["note"], "v0.1.0")
}

func TestStatusTextOutput(t *testing.T) {
	stdout, _ := capture(t, func() {
		code := cli.Execute([]string{"status"})
		require.Zero(t, code)
	})
	assert.Contains(t, stdout, "active: []")
	assert.Contains(t, stdout, "v0.1.0")
}

func TestInjectNetworkLatencyDryRun(t *testing.T) {
	stdout, _ := capture(t, func() {
		code := cli.Execute([]string{
			"inject", "network-latency",
			"--interface", "lo",
			"--delay", "100ms",
			"--duration", "10s",
			"--dry-run",
		})
		require.Zero(t, code)
	})

	assert.Contains(t, stdout, "network_latency")
	assert.Contains(t, stdout, "tc qdisc add dev lo root netem delay 100ms")
	assert.Contains(t, stdout, "tc qdisc del dev lo root")
}

func TestInjectNetworkLatencyDryRunJSON(t *testing.T) {
	stdout, _ := capture(t, func() {
		code := cli.Execute([]string{
			"inject", "network-latency",
			"--interface", "lo",
			"--delay", "100ms",
			"--duration", "10s",
			"--dry-run",
			"--output", "json",
		})
		require.Zero(t, code)
	})

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &got))
	assert.Equal(t, "network_latency", got["kind"])
}

func TestInjectNetworkLatencyRejectsMissingFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing interface",
			args: []string{"inject", "network-latency", "--delay", "100ms", "--duration", "1s", "--dry-run"},
			want: "--interface is required",
		},
		{
			name: "missing delay",
			args: []string{"inject", "network-latency", "--interface", "lo", "--duration", "1s", "--dry-run"},
			want: "--delay must be positive",
		},
		{
			name: "duration exceeds max",
			args: []string{
				"inject", "network-latency",
				"--interface", "lo",
				"--delay", "100ms",
				"--duration", "2h",
				"--max-duration", "1h",
				"--dry-run",
			},
			want: "exceeds --max-duration",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, errOut := capture(t, func() {
				code := cli.Execute(c.args)
				assert.NotZero(t, code)
			})
			assert.Contains(t, errOut, c.want)
		})
	}
}

func TestInjectNetworkPacketLossDryRun(t *testing.T) {
	stdout, _ := capture(t, func() {
		code := cli.Execute([]string{
			"inject", "network-packet-loss",
			"--interface", "lo",
			"--loss", "5",
			"--correlation", "25",
			"--duration", "10s",
			"--dry-run",
		})
		require.Zero(t, code)
	})

	assert.Contains(t, stdout, "network_packet_loss")
	assert.Contains(t, stdout, "tc qdisc add dev lo root netem loss 5% 25%")
	assert.Contains(t, stdout, "tc qdisc del dev lo root")
}

func TestInjectNetworkPacketLossDryRunWithoutCorrelation(t *testing.T) {
	stdout, _ := capture(t, func() {
		code := cli.Execute([]string{
			"inject", "network-packet-loss",
			"--interface", "lo",
			"--loss", "0.5",
			"--duration", "10s",
			"--dry-run",
		})
		require.Zero(t, code)
	})

	assert.Contains(t, stdout, "tc qdisc add dev lo root netem loss 0.5%")
}

func TestInjectNetworkPacketLossDryRunJSON(t *testing.T) {
	stdout, _ := capture(t, func() {
		code := cli.Execute([]string{
			"inject", "network-packet-loss",
			"--interface", "lo",
			"--loss", "5",
			"--duration", "10s",
			"--dry-run",
			"--output", "json",
		})
		require.Zero(t, code)
	})

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &got))
	assert.Equal(t, "network_packet_loss", got["kind"])
}

func TestInjectNetworkPacketLossRejectsBadFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing interface",
			args: []string{"inject", "network-packet-loss", "--loss", "5", "--duration", "1s", "--dry-run"},
			want: "--interface is required",
		},
		{
			name: "missing loss",
			args: []string{"inject", "network-packet-loss", "--interface", "lo", "--duration", "1s", "--dry-run"},
			want: "--loss must be positive",
		},
		{
			name: "loss over 100",
			args: []string{"inject", "network-packet-loss", "--interface", "lo", "--loss", "150", "--duration", "1s", "--dry-run"},
			want: "--loss must be <= 100",
		},
		{
			name: "negative correlation",
			args: []string{"inject", "network-packet-loss", "--interface", "lo", "--loss", "5", "--correlation", "-1", "--duration", "1s", "--dry-run"},
			want: "--correlation must be non-negative",
		},
		{
			name: "duration exceeds max",
			args: []string{
				"inject", "network-packet-loss",
				"--interface", "lo",
				"--loss", "5",
				"--duration", "2h",
				"--max-duration", "1h",
				"--dry-run",
			},
			want: "exceeds --max-duration",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, errOut := capture(t, func() {
				code := cli.Execute(c.args)
				assert.NotZero(t, code)
			})
			assert.Contains(t, errOut, c.want)
		})
	}
}

func TestInjectNetworkPartitionDryRun(t *testing.T) {
	stdout, _ := capture(t, func() {
		code := cli.Execute([]string{
			"inject", "network-partition",
			"--address", "10.0.0.5",
			"--protocol", "tcp",
			"--port", "5432",
			"--duration", "10s",
			"--dry-run",
		})
		require.Zero(t, code)
	})

	assert.Contains(t, stdout, "network_partition")
	assert.Contains(t, stdout, "SCORPIUS_PART_")
	assert.Contains(t, stdout, "-d 10.0.0.5 -p tcp --dport 5432 -j DROP")
	assert.Contains(t, stdout, "-s 10.0.0.5 -p tcp --sport 5432 -j DROP")
	assert.Contains(t, stdout, "-I OUTPUT 1 -j SCORPIUS_PART_")
	assert.Contains(t, stdout, "-I INPUT 1 -j SCORPIUS_PART_")
}

func TestInjectNetworkPartitionDryRunCIDR(t *testing.T) {
	stdout, _ := capture(t, func() {
		code := cli.Execute([]string{
			"inject", "network-partition",
			"--address", "10.0.0.0/24",
			"--direction", "out",
			"--duration", "10s",
			"--dry-run",
		})
		require.Zero(t, code)
	})

	assert.Contains(t, stdout, "-d 10.0.0.0/24 -j DROP")
	assert.Contains(t, stdout, "-I OUTPUT 1 -j SCORPIUS_PART_")
	assert.NotContains(t, stdout, "-I INPUT 1 -j SCORPIUS_PART_")
}

func TestInjectNetworkPartitionDryRunJSON(t *testing.T) {
	stdout, _ := capture(t, func() {
		code := cli.Execute([]string{
			"inject", "network-partition",
			"--address", "10.0.0.5",
			"--duration", "10s",
			"--dry-run",
			"--output", "json",
		})
		require.Zero(t, code)
	})

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &got))
	assert.Equal(t, "network_partition", got["kind"])
}

func TestInjectNetworkPartitionRejectsBadFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing address",
			args: []string{"inject", "network-partition", "--duration", "1s", "--dry-run"},
			want: "--address is required",
		},
		{
			name: "bad address",
			args: []string{"inject", "network-partition", "--address", "not-an-ip", "--duration", "1s", "--dry-run"},
			want: "invalid IP",
		},
		{
			name: "ipv6 address",
			args: []string{"inject", "network-partition", "--address", "2001:db8::1", "--duration", "1s", "--dry-run"},
			want: "IPv4",
		},
		{
			name: "port without protocol",
			args: []string{"inject", "network-partition", "--address", "10.0.0.5", "--port", "80", "--duration", "1s", "--dry-run"},
			want: "port requires protocol",
		},
		{
			name: "bad direction",
			args: []string{"inject", "network-partition", "--address", "10.0.0.5", "--direction", "sideways", "--duration", "1s", "--dry-run"},
			want: "direction",
		},
		{
			name: "duration exceeds max",
			args: []string{
				"inject", "network-partition",
				"--address", "10.0.0.5",
				"--duration", "2h",
				"--max-duration", "1h",
				"--dry-run",
			},
			want: "exceeds --max-duration",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, errOut := capture(t, func() {
				code := cli.Execute(c.args)
				assert.NotZero(t, code)
			})
			assert.Contains(t, errOut, c.want)
		})
	}
}

// capture redirects os.Stdout and os.Stderr through pipes for the duration
// of fn and returns whatever was written to each.
func capture(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()

	origStdout, origStderr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	require.NoError(t, err)
	rErr, wErr, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout, os.Stderr = wOut, wErr
	defer func() {
		os.Stdout, os.Stderr = origStdout, origStderr
	}()

	var outBuf, errBuf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&outBuf, rOut)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(&errBuf, rErr)
		done <- struct{}{}
	}()

	fn()

	_ = wOut.Close()
	_ = wErr.Close()
	<-done
	<-done

	return outBuf.String(), errBuf.String()
}
