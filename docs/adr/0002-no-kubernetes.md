# ADR-0002: No Kubernetes integration in v1.0

Status: Accepted
Date: 2026-05-14

## Context

The dominant open-source chaos engineering tools (Chaos Mesh,
LitmusChaos, kube-monkey) are Kubernetes-native. They deploy as
operators, target pods via label selectors, and exercise faults through
CRDs. That is excellent if your production environment is Kubernetes.

scorpius is aimed at the surface area those tools do not cover well:
bare metal, virtual machines, and edge hosts where the workload runs as
a systemd service or a plain container without an orchestrator. That
includes on-prem Linux, single-node appliances, Raspberry Pi class edge
devices, and small VPS deployments. Building scorpius for Kubernetes
first would put it in direct competition with mature incumbents and
deliver no marginal value to its target users.

Two paths were considered:

- Build a generic agent and add an optional Kubernetes deployment
  (DaemonSet, CRD-driven experiments, operator).
- Build a generic agent only. No Kubernetes-specific code in v1.0.

## Decision

No Kubernetes integration in v1.0. That means no DaemonSet manifests, no
Helm chart, no CRDs, no controller-runtime, no kube client-go in
`go.mod`. The agent runs as a systemd unit or a plain container. The
optional controller (v0.5.0) speaks mTLS HTTP, not the Kubernetes API.

Users who want to run scorpius in Kubernetes can build their own
DaemonSet around the existing binary. That is not a supported deployment
in v1.0 documentation.

## Consequences

Positive:
- Smaller dependency surface. `go.mod` stays compact and reviewable.
- No coupling of the fault model to Pod or Container abstractions.
  Targets are Linux processes, network interfaces, and cgroups, full
  stop.
- Clear positioning against existing tools: scorpius is for hosts, not
  clusters.
- Easier to reason about safety. The blast radius is the host the agent
  runs on, not a scheduler-allocated subset of pods.

Negative:
- Users running everything in Kubernetes will not adopt scorpius.
  Acceptable: that audience is well served elsewhere.
- No ready-made distribution mechanism comparable to Helm. Mitigated by
  systemd units and container images.
- If a future user demand for Kubernetes integration appears, it will be
  added as an out-of-tree project or a v2.0 ADR, not retrofitted into
  v1.0.

This decision is final for v1.0. Revisit only with a superseding ADR.
