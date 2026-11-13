#!/usr/bin/env bash
#
# bench-ab-env.sh — the machine a bench A/B ran on, written beside its samples:
# a number is read with the box that produced it. The suite, the two commits
# and the overlay come from the environment (SUITE_NAME, BASE_SHA, HEAD_SHA,
# OVERLAY); the rest is read from the runner — CPUs, lscpu, the clock source
# (time.Now is half of a cache hit), the frequency governor, memory, the disk
# the benchmarks write to and its inodes (a scale backlog is a million files),
# the kernel and the go command. A probe that is absent says so. Exit 0.

set -uo pipefail

line() { printf '%-14s %s\n' "$1:" "$2"; }
read_or() { if [ -r "$1" ]; then tr -d '\n' <"$1"; else printf '%s' "$2"; fi; }

line suite "${SUITE_NAME:-?}"
line base "${BASE_SHA:-?}"
line head "${HEAD_SHA:-?}"
line overlay "${OVERLAY:-?}"
line date "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
line runner "${RUNNER_NAME:-local} (${RUNNER_OS:-$(uname -s)}, ${RUNNER_ARCH:-$(uname -m)})"
line kernel "$(uname -srm)"
line cpus "$(nproc 2>/dev/null || echo '?')"
line clocksource "$(read_or /sys/devices/system/clocksource/clocksource0/current_clocksource 'not readable')"
line governor "$(read_or /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor 'not exposed')"
line memory "$(awk '/^MemTotal:/ { print $2 " kB" }' /proc/meminfo 2>/dev/null || echo '?')"
line go "$(${GO:-go} version 2>/dev/null || echo 'not found')"
echo
echo "## lscpu"
lscpu 2>/dev/null || echo "lscpu: not available"
echo
echo "## disk (df -h, df -i)"
df -h . 2>/dev/null || true
df -i . 2>/dev/null || true
exit 0
