# ADR-0003: iptables backend and per-fault chain isolation

Status: Accepted
Date: 2026-05-17

## Context

scorpius needs a network filtering primitive for the `network_partition`
fault (v0.2.0) and the `network_dns_failure` fault that follows it.
Unlike the latency and packet loss faults, which shape an entire
interface via `tc netem`, partition is target-scoped: drop traffic
to or from a specific address, optionally constrained by port and
protocol. `tc` cannot express that without elaborate filter+class
machinery; the idiomatic Linux tool is iptables (or nftables).

Two backend choices were considered:

- **iptables (legacy CLI).** Universal on modern Linux, including
  Alpine (which Alex tests on). On most current distros the `iptables`
  binary is `iptables-nft` under the hood, using the same legacy syntax
  but committing through the nftables backend. That covers both worlds
  with one command surface.
- **nftables (`nft` CLI / netlink directly).** The strategic future,
  but the agent would need to either shell out to `nft` (which is
  not always installed by default) or use a netlink library
  (additional dependency, additional cgo on some paths). The added
  complexity does not buy anything users can observe in v1.0.

The second axis is **how the agent's state stays isolated from
operator-installed rules**. Three approaches were considered:

- Append DROP rules directly to the default `INPUT`/`OUTPUT` chains.
  Simplest. But revert requires either reading every rule (race-prone)
  or stashing a "we put it at position N" index that becomes wrong if
  any other tool touches iptables during the experiment.
- Tag rules with `-m comment --comment "scorpius:<id>"`. Identifies our
  rules unambiguously, but revert still has to scan and `-D` each one.
  Brittle under concurrent edits.
- **Create a per-fault chain** (`SCORPIUS_PART_<id>`), populate it
  with DROP rules, then `-I` jump rules into the default chains.
  Revert deletes the jumps, flushes the chain, deletes the chain.
  Never reads or modifies user rules.

## Decision

scorpius shells out to the `iptables` binary on PATH for network
filtering faults in v0.2.0. The agent invokes iptables with `-w 5`
to wait politely for the xtables lock instead of failing fast under
contention. IPv4 only in v0.2.0; `ip6tables` support is deferred to
hardening (Phase 6 / v1.0.0). Each fault instance owns a dedicated
chain named `SCORPIUS_PART_<8 hex chars>`, where the suffix is
generated from `crypto/rand` at Apply time.

The chain lifecycle is:

1. **Apply.** Generate id, create chain, append DROP rules into it,
   then insert one JUMP rule per direction (`-I OUTPUT 1 -j
   SCORPIUS_PART_<id>` and/or `-I INPUT 1 -j SCORPIUS_PART_<id>`).
2. **Revert.** Delete each JUMP from the default chains, flush the
   chain, delete the chain. Idempotent: a missing chain is treated
   as already-reverted.
3. **Verify.** Confirm both the JUMP rules and the chain are gone.

This is the same isolation pattern used by Docker's `DOCKER` chain
and `iptables-restore --noflush` workflows. It is the cleanest way
to coexist with whatever else is touching iptables on the host.

## Consequences

Positive:

- The agent never reads or mutates a user rule. The blast radius is
  the chain it created and the jump rules pointing at it.
- Revert is unconditional: delete jumps, flush chain, delete chain.
  No "find the rule at position N" race.
- Verify is meaningful: did our chain disappear, yes/no.
- Naming is unique per instance, so multiple concurrent partitions
  do not collide.

Negative:

- Slightly more `iptables` invocations per Apply (chain create + N
  rules + jump inserts) than appending rules directly. Acceptable;
  Apply is not a hot path.
- Users inspecting iptables output during an experiment see an extra
  chain. Documented in the operator guide.
- Per-fault chains are not visible across agent restarts. If the
  agent crashes, the watchdog has already registered to revert; if
  the watchdog itself dies, the chain leaks. The leftover state is
  named, scoped, and easy to identify and clean up by an operator
  running `iptables -L | grep SCORPIUS_`.

## Future revisits

- IPv6 (`ip6tables`) parity will be added before v1.0.0.
- A v2.0 might switch to native nftables via netlink for finer
  control and faster batched operations. The chain-per-fault
  pattern translates directly to nftables tables/chains.
- A startup-time sweep that removes any leftover `SCORPIUS_*`
  chains is a reasonable hardening item; deferred because v0.2.0
  has no agent-as-daemon mode yet.
