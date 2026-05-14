# Security policy

## Supported versions

| Version | Supported |
| ------- | --------- |
| 0.1.x   | yes       |
| < 0.1.0 | no        |

## Reporting a vulnerability

Report vulnerabilities privately via GitHub's Security Advisories:

https://github.com/alexmchughdev/scorpius/security/advisories/new

Please do not file public issues for security reports. Initial response within
seven days; fixed-issue disclosure timing negotiated with the reporter.

If GitHub advisories are not an option, email the project owner via the
address on the GitHub profile linked from this repository.

## Threat model

scorpius is a privileged agent. It runs as root (or with `CAP_NET_ADMIN`) on
the target host, executes `tc`, and modifies kernel networking state. The
threat model assumes:

- The host operator is trusted. scorpius is not a multi-tenant tool.
- The build pipeline (GitHub Actions) is trusted. Provenance is established
  by signed release artefacts (cosign keyless via Sigstore OIDC).
- Inputs to the agent come from a trusted source (operator CLI or, in
  future, controller mTLS channel).

The following are out of scope for v0.1 and should not be assumed:

- Tamper-evident audit logs. Audit events are appended JSON-lines, not
  cryptographically chained or signed. An attacker with write access to
  the audit file can edit or delete prior entries. Ship audit output to
  an append-only sink (journald, remote syslog, append-only S3) for
  forensic integrity.
- Operator authentication. The `--operator` flag is recorded verbatim. It
  is a label, not a credential. v0.5 introduces mTLS-authenticated
  controller-driven experiments where operator identity is bound to the
  client certificate.
- Resistance to a compromised `$PATH`. scorpius resolves `tc` via
  `exec.LookPath`, which honours `$PATH`. A privileged invocation with
  attacker-controlled PATH executes the attacker's binary. Run scorpius
  from a known-good environment; a systemd unit with a fixed `Environment=`
  is the recommended deployment.
- Resistance to `SIGKILL`. The in-process dead-man's switch runs as a
  goroutine in the same process as the agent. A `SIGKILL` removes both,
  leaving any applied fault in the kernel until cleared manually. The
  watchdog protects only against the agent crashing in a way that leaves
  the process alive but the fault unattended. v0.2 considers an external
  sidecar watchdog.

## Defensive practices for operators

- Deploy scorpius as a systemd unit with `Environment=PATH=/usr/bin:/sbin`
  and `NoNewPrivileges=true`.
- Use `--max-duration` to cap how long any single experiment can stay
  active. The default is one hour.
- Pipe `--audit-log` to an append-only sink. The default (stdout) suits
  systemd-journald natively.
- Verify release artefacts with cosign keyless. See `README.md` for the
  verification command.
- Do not run scorpius on production hosts you do not own; the safety net
  is best-effort.

## Defensive practices in the codebase

- `exec.Command`/`exec.CommandContext` is used with argument lists, never
  with a shell. Operator-supplied strings (interface names, ticket text)
  cannot be parsed as shell commands.
- Audit log files are created `O_APPEND|O_CREATE|O_WRONLY` with mode
  `0o600`.
- The watchdog runs Revert with a fresh `context.Background()` bounded by
  a fixed timeout so a parent shutdown cannot abandon a revert mid-way.
- Reversions are mutex-guarded and idempotent.
- Concurrency is exercised under `go test -race -count=1` in CI.

## Supply chain

- Releases are tagged `v*` and published by `.github/workflows/release.yml`.
- Release binaries are built with `-trimpath`, `CGO_ENABLED=0`, and stripped.
- `checksums.txt` is signed by cosign keyless via the Sigstore OIDC flow.
  Verification details in `README.md`.
- A CycloneDX SBOM is generated per release and attached to the GitHub
  release.
- GitHub Actions are pinned by commit SHA. Dependabot updates them on a
  weekly cadence.
- `govulncheck` runs in CI; `CodeQL` provides semantic code scanning;
  `dependency-review` blocks PRs that introduce vulnerable dependencies.
