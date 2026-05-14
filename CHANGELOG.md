# Changelog

All notable changes to scorpius. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## v0.1.0 - 2026-05-14

Initial release. Establishes the safety contract that every fault in v1.0
will satisfy and ships the first fault built on top of it.

### Added

- Single static `scorpius` binary for `linux/amd64` and `linux/arm64`,
  produced with `CGO_ENABLED=0` and `-ldflags=-s -w`.
- `Fault` and `Reversion` interfaces with the shared `FaultSpec`, `Target`,
  and `Plan` types in `internal/faults`. Every fault must Validate, Apply,
  DryRun, Revert, and Verify.
- Dead-man's switch watchdog in `internal/watchdog`. Default 30s threshold;
  zero or negative thresholds collapse to the default so misconfiguration
  cannot disable the safety net. Reversions run with a fresh
  `context.Background()` bounded by 30s so a parent shutdown cannot
  interrupt them.
- Structured JSON audit log in `internal/audit`. One event per line.
  Timestamps RFC3339Nano UTC. Four canonical event types: `fault_applied`,
  `fault_reverted`, `abort_triggered`, `verification_failed`.
- Network latency fault in `internal/faults/network` using `tc qdisc add ...
  netem`. Apply validates interface existence, refuses to overwrite any
  user-installed qdisc, registers with the watchdog, and runs a heartbeat
  loop until Revert. Revert is idempotent; Verify checks no leftover netem
  qdisc remains.
- Cobra-based CLI: `scorpius inject network-latency`, `scorpius preflight`,
  `scorpius status`. Persistent flags `--output {text,json,yaml}`, `-v/-vv`,
  `--config`. `--dry-run` renders the exact tc commands without touching the
  kernel.
- ADR-0001 (Go as implementation language) and ADR-0002 (no Kubernetes
  integration in v1.0) in `docs/adr/`.
- GitHub Actions CI: vet, staticcheck (pinned 2024.1.1), `go test -race
  -count=1`, and matrix builds for both target architectures with artifact
  upload.
- GitHub Actions release workflow: on `v*` tag push, builds both arches,
  generates `checksums.txt`, signs via cosign keyless OIDC, and publishes a
  GitHub release with binaries plus signature and certificate.
- `scripts/local-validate.sh` exercises the happy path against the loopback
  interface with `ping` before, during, and after the inject.

### Tested

- Unit tests for faults, watchdog, audit, network latency, and CLI all pass
  under `go test -race -count=1 ./...`.
- Integration tests gated behind `//go:build integration` cover Apply +
  Revert against a real dummy interface; they require root and skip
  cleanly otherwise.
- Dry-run, preflight, status, and CLI flag validation were exercised
  end-to-end against the built binary on `linux/amd64`. The live inject
  against `lo` is intended to be run as `sudo
  ./scripts/local-validate.sh` post-release.

### Known limitations

- One-shot only: scorpius runs as a process per inject. Daemon mode and
  `scorpius status` returning real state come in a later phase.
- Configuration is via flags only. Environment-variable and config-file
  precedence is wired in a later phase.
- Audit log rotation is the caller's responsibility (logrotate, journald).
