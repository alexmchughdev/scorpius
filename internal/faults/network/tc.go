package network

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// TC abstracts the kernel-side tc commands the network faults need. The
// production implementation in NewExecTC shells out to /sbin/tc; tests
// inject a fake.
type TC interface {
	InterfaceExists(iface string) (bool, error)
	Show(ctx context.Context, iface string) ([]byte, error)
	AddRoot(ctx context.Context, iface string, qdiscArgs ...string) error
	DelRoot(ctx context.Context, iface string) error
}

// execTC is the production TC implementation.
type execTC struct {
	tcPath string
}

// NewExecTC resolves the tc binary on PATH. Returns an error if tc cannot be
// found; the caller should treat that as a preflight failure.
func NewExecTC() (TC, error) {
	path, err := exec.LookPath("tc")
	if err != nil {
		return nil, fmt.Errorf("tc not found on PATH: %w", err)
	}
	return &execTC{tcPath: path}, nil
}

func (t *execTC) InterfaceExists(iface string) (bool, error) {
	if iface == "" {
		return false, errors.New("interface name is empty")
	}
	_, err := os.Stat("/sys/class/net/" + iface)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat /sys/class/net/%s: %w", iface, err)
}

func (t *execTC) Show(ctx context.Context, iface string) ([]byte, error) {
	return t.run(ctx, "qdisc", "show", "dev", iface)
}

func (t *execTC) AddRoot(ctx context.Context, iface string, qdiscArgs ...string) error {
	args := append([]string{"qdisc", "add", "dev", iface, "root"}, qdiscArgs...)
	_, err := t.run(ctx, args...)
	return err
}

func (t *execTC) DelRoot(ctx context.Context, iface string) error {
	_, err := t.run(ctx, "qdisc", "del", "dev", iface, "root")
	return err
}

func (t *execTC) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, t.tcPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("tc %s: %w: %s", joinArgs(args), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}

func joinArgs(args []string) string {
	var b bytes.Buffer
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(a)
	}
	return b.String()
}
