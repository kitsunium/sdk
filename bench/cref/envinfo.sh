#!/usr/bin/env bash
# envinfo.sh - the machine facts a number from this bench depends on. Printed as
# key=value lines; run.sh stores them next to the results.
set -u

flag() {
	if grep -m1 '^flags' /proc/cpuinfo | tr ' ' '\n' | grep -qx "$1"; then echo yes; else echo no; fi
}

echo "date_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "host=$(hostname)"
echo "kernel=$(uname -r)"
echo "cpu_model=$(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ //')"
echo "cpu_mhz=$(grep -m1 'cpu MHz' /proc/cpuinfo | cut -d: -f2- | sed 's/^ //')"
echo "vcpus=$(nproc)"
for f in sha_ni avx2 avx512f sse4_2 constant_tsc nonstop_tsc; do
	echo "cpu_flag_$f=$(flag "$f")"
done
echo "clocksource=$(cat /sys/devices/system/clocksource/clocksource0/current_clocksource 2>/dev/null || echo unknown)"
echo "clocksource_available=$(cat /sys/devices/system/clocksource/clocksource0/available_clocksource 2>/dev/null || echo unknown)"
echo "governor=$(cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor 2>/dev/null || echo none)"
echo "ulimit_nofile=$(ulimit -n)"
if command -v gcc >/dev/null 2>&1; then echo "gcc=$(gcc --version | head -1)"; else echo "gcc=none"; fi
echo "openssl=$(openssl version 2>/dev/null || echo none)"
echo "liburing=$(pkg-config --modversion liburing 2>/dev/null || dpkg-query -W -f='${Version}' liburing-dev 2>/dev/null || echo none)"
# The Go side compiles for a GOAMD64 level; record it when a toolchain is here.
if command -v go >/dev/null 2>&1; then
	echo "go_version=$(go env GOVERSION)"
	echo "go_amd64=$(go env GOAMD64)"
else
	echo "go_version=none"
	echo "go_amd64=unknown"
fi
# steal and softirq since boot: a rising steal during a run invalidates it.
awk '/^cpu /{print "proc_stat_cpu_boot=" $0}' /proc/stat
