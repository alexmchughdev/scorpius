package faults

import "context"

// Fault is the contract every fault implementation satisfies.
type Fault interface {
	// Name returns the canonical fault name, e.g. "network_latency".
	Name() string

	// Validate checks the spec is well-formed and safe before applying.
	Validate(spec FaultSpec) error

	// Apply injects the fault. A Reversion is returned even on partial
	// failure so cleanup is possible.
	Apply(ctx context.Context, spec FaultSpec) (Reversion, error)

	// DryRun returns the plan without applying it.
	DryRun(spec FaultSpec) (Plan, error)
}

// Reversion undoes a previously applied fault.
type Reversion interface {
	// Revert undoes the fault. Idempotent.
	Revert(ctx context.Context) error

	// Verify reports leftover state after a revert. Non-nil means revert
	// did not return the system to its pre-fault condition.
	Verify(ctx context.Context) error
}
