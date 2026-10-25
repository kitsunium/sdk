#!/usr/bin/env bash
# scripts/ci/net-veth.sh — the veth pair L3 runs over (plan P8; the bench is
# internal/service/net/server/load_l3_linux_internal_test.go).
#
# `up` creates a network namespace and a veth pair whose far end sits in it,
# so the sender's datagrams cross a real device instead of the loopback the
# kernel short-cuts between two local addresses. The addresses are the
# benchmarking range of RFC 2544 (198.18.0.0/15): never routed, never
# anybody's infrastructure. `down` removes the namespace, which removes the
# pair, and succeeds when there is nothing to remove. Needs root.
#
# It prints, on `up`, the environment the bench reads: the receiver's and the
# sender's addresses and the prefix that runs the receiver in the namespace.
#
# Usage: net-veth.sh up|down
# Environment: IP names the ip command (the suite stubs it).
# Exit 0, 1 when ip fails on `up`, 64 on bad usage.

set -euo pipefail

ns=netgate-l3
near=ng-a
far=ng-b
near_addr=198.18.0.1
far_addr=198.18.0.2
ip_cmd="${IP:-ip}"

case "${1:-}" in
  up)
    "$ip_cmd" netns add "$ns"
    "$ip_cmd" link add "$near" type veth peer name "$far"
    "$ip_cmd" link set "$far" netns "$ns"
    "$ip_cmd" addr add "$near_addr/30" dev "$near"
    "$ip_cmd" link set "$near" up
    "$ip_cmd" -n "$ns" addr add "$far_addr/30" dev "$far"
    "$ip_cmd" -n "$ns" link set "$far" up
    "$ip_cmd" -n "$ns" link set lo up
    echo "KITSUNIUM_NETGATE_L3_PEER=$far_addr"
    echo "KITSUNIUM_NETGATE_L3_LOCAL=$near_addr"
    echo "KITSUNIUM_NETGATE_L3_EXEC=sudo $ip_cmd netns exec $ns"
    ;;
  down)
    "$ip_cmd" netns del "$ns" 2>/dev/null || true
    ;;
  *)
    echo "usage: $0 up|down" >&2
    exit 64
    ;;
esac
