package faults_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alexmchughdev/scorpius/internal/faults"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockReversion struct {
	reverts  int
	verifies int
	revErr   error
	verErr   error
}

func (m *mockReversion) Revert(ctx context.Context) error {
	m.reverts++
	return m.revErr
}

func (m *mockReversion) Verify(ctx context.Context) error {
	m.verifies++
	return m.verErr
}

type mockFault struct {
	validateErr error
	applyErr    error
	rev         *mockReversion
}

func (m *mockFault) Name() string { return "mock" }

func (m *mockFault) Validate(spec faults.FaultSpec) error {
	return m.validateErr
}

func (m *mockFault) Apply(ctx context.Context, spec faults.FaultSpec) (faults.Reversion, error) {
	if m.applyErr != nil {
		return nil, m.applyErr
	}
	m.rev = &mockReversion{}
	return m.rev, nil
}

func (m *mockFault) DryRun(spec faults.FaultSpec) (faults.Plan, error) {
	return faults.Plan{Kind: m.Name(), Description: "mock plan"}, nil
}

func TestMockFaultSatisfiesInterface(t *testing.T) {
	var f faults.Fault = &mockFault{}

	spec := faults.FaultSpec{
		Kind:     "mock",
		Duration: time.Second,
		Target:   faults.Target{Interface: "lo"},
	}

	require.NoError(t, f.Validate(spec))

	rev, err := f.Apply(context.Background(), spec)
	require.NoError(t, err)
	require.NotNil(t, rev)

	require.NoError(t, rev.Revert(context.Background()))
	require.NoError(t, rev.Verify(context.Background()))

	plan, err := f.DryRun(spec)
	require.NoError(t, err)
	assert.Equal(t, "mock", plan.Kind)
	assert.Equal(t, "mock plan", plan.Description)
}

func TestApplyReturnsErrorWithoutReversion(t *testing.T) {
	f := &mockFault{applyErr: errors.New("apply failed")}

	rev, err := f.Apply(context.Background(), faults.FaultSpec{})
	require.Error(t, err)
	assert.Nil(t, rev)
}

func TestPlanString(t *testing.T) {
	p := faults.Plan{
		Kind:        "network_latency",
		Description: "Add 100ms latency to eth0",
		Commands:    []string{"tc qdisc add dev eth0 root netem delay 100ms"},
		SideEffects: []string{"all egress traffic on eth0 is delayed"},
	}

	s := p.String()
	assert.Contains(t, s, "network_latency")
	assert.Contains(t, s, "tc qdisc add")
	assert.Contains(t, s, "side effects")
	assert.Contains(t, s, "delayed")
}

func TestPlanStringOmitsEmptySections(t *testing.T) {
	p := faults.Plan{Kind: "noop"}
	s := p.String()
	assert.Contains(t, s, "noop")
	assert.NotContains(t, s, "commands:")
	assert.NotContains(t, s, "side effects:")
}
