<p align="center">
  <img src="logo.png" alt="scorpius" width="220">
</p>

# scorpius

A chaos engineering toolkit for Linux services. Single Go binary, runs on bare
metal, VMs, and edge hosts. No Kubernetes required.

Status: v0.1.0 in development. See `CHANGELOG.md` once tagged.

## What it does

Injects controlled failures into running Linux processes and reverts them
safely. Supported faults in v1.0 will include network latency, packet loss,
network partition, DNS failure, CPU pressure, memory pressure, disk slowdown,
disk full, and process kill.

Every fault implementation includes a bounded maximum duration, a dead-man's
switch watchdog, abort conditions, a dry-run mode, and verified reversion.

## Status by phase

- v0.1.0 (in progress): network latency fault, watchdog, audit log,
  cross-architecture builds for `linux/amd64` and `linux/arm64`.
- v0.2.0 onward: more fault types, YAML experiments, optional controller,
  optional eBPF path. See `docs/adr/` for the design record.

## Documentation

- `docs/adr/`: Architecture Decision Records.
- `CONTRIBUTING.md`: development workflow.

## Licence

Apache 2.0. See `LICENSE`.
