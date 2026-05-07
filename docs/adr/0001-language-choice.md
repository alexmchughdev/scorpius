# ADR-0001: Go as the implementation language

Status: Accepted
Date: 2026-05-14

## Context

scorpius is a Linux chaos engineering toolkit. The agent runs as root on
target hosts and on a mix of architectures (x86_64 servers, aarch64 edge
boxes). It needs to ship as a single static binary, build quickly enough
for hobby-time iteration, integrate with kernel facilities (tc, cgroups,
iptables, signals, eventually eBPF), and be approachable to a wider
contributor pool than systems specialists alone.

Three languages were considered: Rust, Go, and Python.

- Rust offers strong safety guarantees and the best eBPF ergonomics via
  Aya. It has a steeper learning curve, slower compile times, and a
  smaller pool of contributors willing to engage with a portfolio project.
  The chaos-tooling space has only a couple of Rust precedents.
- Go offers static binaries with no runtime, fast compiles, mature
  CLI/testing tooling, a workable eBPF library in cilium/ebpf, and the
  largest precedent in the chaos and systems tooling space (Chaos Mesh,
  Pumba, Kubernetes operators, Docker, containerd, runc).
- Python is unsuitable for the deploy story (interpreter, system
  libraries) and would require either packaging gymnastics or a switch
  later. Dismissed early.

## Decision

scorpius is implemented in Go. `CGO_ENABLED=0 go build` is the canonical
build for the agent. The optional eBPF path uses cilium/ebpf and is
gated behind a build tag; that is the only place cgo is permitted.

## Consequences

Positive:
- Single static binary per architecture, deployed by copying a file.
- Fast iteration: a clean build is seconds, not minutes.
- Large idiomatic body of work to learn from for systems concerns
  (signals, cgroups, processes, network namespaces).
- Tooling: `gofmt`, `go vet`, `staticcheck`, race detector, built-in
  testing all available with no setup.

Negative:
- No compile-time guarantees about concurrent access; the watchdog and
  audit log need careful design and tests instead.
- eBPF support in Go is workable but not as expressive as Aya in Rust.
  Acceptable because eBPF is selectively applied, not pervasive.
- Error handling is verbose. Mitigated by wrapping with `%w` and a
  consistent logging discipline.

The decision is settled for v1.0. Rust may be revisited if the project
moves beyond v1.0 and the eBPF surface grows materially.
