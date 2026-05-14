#!/usr/bin/env bash
#
# Stage 8 live validation harness.
#
# Runs the v0.1.0 happy path against the loopback interface: inject 100ms of
# latency for 10s, ping in the background to confirm latency increases, wait
# for the bounded duration to expire, ping again to confirm reversion.
#
# Requires:
#   - root (CAP_NET_ADMIN is enough; sudo is the easy way)
#   - bin/scorpius built (run `make build` first)
#   - tc, ip, ping on PATH
#
# The script tears down any leftover scorpius-managed qdisc on lo before it
# exits, even on error.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="${ROOT}/bin/scorpius"
AUDIT_LOG="$(mktemp -t scorpius-audit.XXXXXX.jsonl)"

cleanup() {
    tc qdisc del dev lo root 2>/dev/null || true
    echo
    echo "audit log preserved at: ${AUDIT_LOG}"
}
trap cleanup EXIT

if [[ ! -x "${BIN}" ]]; then
    echo "scorpius binary not found at ${BIN}; run 'make build' first" >&2
    exit 1
fi

if [[ "$(id -u)" -ne 0 ]]; then
    echo "this script must run as root (try: sudo $0)" >&2
    exit 1
fi

echo "== preflight =="
"${BIN}" preflight

echo
echo "== baseline ping (3 pings before inject) =="
ping -c 3 -i 0.2 localhost | tail -4

echo
echo "== inject 100ms +20ms jitter on lo for 10s =="
"${BIN}" inject network-latency \
    --interface lo \
    --delay 100ms \
    --jitter 20ms \
    --duration 10s \
    --operator "$(logname 2>/dev/null || whoami)" \
    --ticket "v0.1.0-validate" \
    --audit-log "${AUDIT_LOG}" &

inject_pid=$!

# Wait for the qdisc to land.
sleep 1

echo
echo "== loaded ping (3 pings during inject) =="
ping -c 3 -i 0.2 localhost | tail -4

echo
echo "== waiting for scorpius to revert =="
wait "${inject_pid}"

echo
echo "== post-revert ping (3 pings after) =="
ping -c 3 -i 0.2 localhost | tail -4

echo
echo "== audit log =="
cat "${AUDIT_LOG}"

echo
echo "== leftover qdisc check =="
tc qdisc show dev lo
