<p align="center">
  <img src="logo.png" alt="scorpius" width="220">
</p>

# scorpius

A chaos engineering toolkit for Linux services. Single Go binary, runs on bare
metal, VMs, and edge hosts. No Kubernetes required.

Status: v0.1.0 (first tagged release). See `CHANGELOG.md`.

## What it does

Injects controlled failures into running Linux processes and reverts them
safely. Supported faults in v1.0 will include network latency, packet loss,
network partition, DNS failure, CPU pressure, memory pressure, disk slowdown,
disk full, and process kill.

Every fault implementation includes a bounded maximum duration, a dead-man's
switch watchdog, abort conditions, a dry-run mode, and verified reversion.

## Install

### Build from source

Requires Go 1.22 or later.

```
git clone https://github.com/alexmchughdev/scorpius.git
cd scorpius
make build       # produces ./bin/scorpius (static, CGO_ENABLED=0)
```

### Release binaries

Each tagged release publishes signed `linux/amd64` and `linux/arm64` binaries
on the GitHub Releases page. Checksums are signed via cosign keyless OIDC;
verify with:

```
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp "https://github.com/alexmchughdev/scorpius/.*" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  checksums.txt
```

## Quickstart

Check the host has the kernel features and binaries scorpius needs:

```
scorpius preflight
```

Preview a fault without applying it:

```
scorpius inject network-latency \
    --interface lo --delay 100ms --jitter 20ms --duration 30s --dry-run
```

Apply real latency to the loopback interface for ten seconds (requires root
or `CAP_NET_ADMIN`):

```
sudo scorpius inject network-latency \
    --interface lo --delay 100ms --duration 10s \
    --operator alex --ticket INC-1234
```

The agent applies the fault, blocks for the bounded duration, then reverts.
Send `SIGINT` (Ctrl-C) or `SIGTERM` to revert early. If the agent crashes mid
experiment, the dead-man's switch reverts on its own.

Audit events stream as JSON lines to stdout (or to `--audit-log <path>`):

```json
{"timestamp":"...","type":"fault_applied","operator":"alex","ticket":"INC-1234","fault":"network_latency","interface":"lo","duration":"10s","parameters":{"delay_ms":100,"jitter_ms":0}}
{"timestamp":"...","type":"fault_reverted","operator":"alex","ticket":"INC-1234","fault":"network_latency","interface":"lo","reason":"duration_complete"}
```

## Status by phase

- v0.1.0 (released): network latency fault, watchdog, audit log,
  cross-architecture builds for `linux/amd64` and `linux/arm64`.
- v0.2.0 onward: more fault types, YAML experiments, optional controller,
  optional eBPF path. See `docs/adr/` for the design record.

## Documentation

- `docs/adr/`: Architecture Decision Records.
- `CONTRIBUTING.md`: development workflow.

## Licence

Apache 2.0. See `LICENSE`.
