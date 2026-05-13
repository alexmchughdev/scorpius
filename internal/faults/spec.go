package faults

import (
	"fmt"
	"strings"
	"time"
)

// FaultSpec is the parsed configuration for one fault application.
type FaultSpec struct {
	Kind     string
	Target   Target
	Duration time.Duration

	// Parameters carries fault-specific tuning. Implementations
	// type-assert the values they expect.
	Parameters map[string]any
}

// Target is the addressable kernel resource a fault acts on.
type Target struct {
	Interface string
	Process   string
	Address   string
}

// Plan is the DryRun output: commands and predicted side effects.
type Plan struct {
	Kind        string   `json:"kind"`
	Description string   `json:"description"`
	Commands    []string `json:"commands,omitempty"`
	SideEffects []string `json:"side_effects,omitempty"`
}

// String renders the plan for human review.
func (p Plan) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "kind: %s\n", p.Kind)
	if p.Description != "" {
		fmt.Fprintf(&b, "description: %s\n", p.Description)
	}
	if len(p.Commands) > 0 {
		b.WriteString("commands:\n")
		for _, c := range p.Commands {
			fmt.Fprintf(&b, "  - %s\n", c)
		}
	}
	if len(p.SideEffects) > 0 {
		b.WriteString("side effects:\n")
		for _, s := range p.SideEffects {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
	}
	return b.String()
}
