#!/usr/bin/env bash
# gen-bench-md.sh — collect benchmark output for each package that ships a
# *_bench_test.go file and write a BENCH.md report next to it.
#
# Why per-package: bench numbers from `cpu Intel(R) Core(TM) i5-3210M` and
# from `cpu AMD EPYC 9354P` are not directly comparable. A self-contained
# report alongside the code lets reviewers see the machine baseline AND
# the numbers in one place, without spelunking through CI logs.
#
# Usage:
#   scripts/bench/gen-bench-md.sh                # discover and run every bench target
#   scripts/bench/gen-bench-md.sh PACKAGE...     # run only the given Bazel targets
#
# Each invocation rewrites the listed BENCH.md files with a fresh header.

set -euo pipefail

WORKSPACE="${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"
cd "$WORKSPACE"

BENCHTIME="${BENCH_TIME:-2s}"

# Collect machine fingerprint once — the same header is stamped on every
# report so reviewers see exactly which box produced the numbers.
collect_machine_info() {
    local cpu_model cpu_cores cpu_freq mem_total os_name kernel arch host go_ver bazel_ver git_sha git_branch ts

    cpu_model="$(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | sed 's/.*: //' || echo "unknown")"
    cpu_cores="$(nproc 2>/dev/null || echo "unknown")"
    cpu_freq="$(grep -m1 'cpu MHz' /proc/cpuinfo 2>/dev/null | sed 's/.*: //;s/$/ MHz/' || echo "unknown")"
    mem_total="$(grep -m1 'MemTotal:' /proc/meminfo 2>/dev/null | awk '{printf "%.1f GiB", $2/1024/1024}' || echo "unknown")"
    os_name="$(grep -m1 '^PRETTY_NAME=' /etc/os-release 2>/dev/null | cut -d'"' -f2 || echo "unknown")"
    kernel="$(uname -sr 2>/dev/null || echo "unknown")"
    arch="$(uname -m 2>/dev/null || echo "unknown")"
    host="$(hostname 2>/dev/null || echo "unknown")"
    go_ver="$(go version 2>/dev/null | awk '{print $3, $4}' || echo "unknown")"
    bazel_ver="$(bazel version 2>/dev/null | grep -m1 'Build label' | awk '{print $3}' || echo "unknown")"
    git_sha="$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")"
    git_branch="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")"
    ts="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"

    cat <<EOF
## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU                | $cpu_model |
| CPU cores          | $cpu_cores |
| CPU frequency      | $cpu_freq |
| RAM                | $mem_total |
| OS                 | $os_name |
| Kernel             | $kernel |
| Architecture       | $arch |
| Hostname           | $host |
| Go toolchain       | $go_ver |
| Bazel              | $bazel_ver |
| Git branch         | $git_branch |
| Git commit         | $git_sha |
| Generated (UTC)    | $ts |
| Bench wall-clock   | \`-test.benchtime=$BENCHTIME\` |

EOF
}

# Parse `go test` benchmark output into a Markdown table. The columns are
# the standard \`testing\` outputs: bench name, iterations, ns/op, B/op,
# allocs/op. Lines that are not Benchmark* are dropped.
format_bench_table() {
    local input_file="$1"
    awk '
        BEGIN {
            printed_header = 0
        }
        /^Benchmark/ {
            if (!printed_header) {
                print "| Benchmark | Iters | ns/op | B/op | allocs/op |"
                print "|---|---:|---:|---:|---:|"
                printed_header = 1
            }
            #: pull the bench name (col 1) and trim the -N suffix that
            #: encodes GOMAXPROCS — preserves it in the table for clarity.
            name = $1
            iters = $2
            #: locate the columns by their unit suffix so the parser
            #: survives extra ReportMetric values in any order.
            ns = ""; bops = ""; allocs = ""
            for (i = 3; i <= NF; i++) {
                if ($(i+1) == "ns/op")    { ns = $i }
                if ($(i+1) == "B/op")     { bops = $i }
                if ($(i+1) == "allocs/op"){ allocs = $i }
            }
            #: emit one row per bench; missing columns render as em-dash.
            printf "| `%s` | %s | %s | %s | %s |\n", \
                name, iters, (ns?ns:"—"), (bops?bops:"—"), (allocs?allocs:"—")
        }
    ' "$input_file"
}

# Locate the source directory holding the *_bench_test.go file for a given
# Bazel test target. We resolve via gazelle's natural mapping: the BUILD.bazel
# file lives at the package source root.
target_to_dir() {
    local target="$1"
    #: //pkg/v1/codec:codec_bench_test → pkg/v1/codec
    echo "${target#//}" | cut -d: -f1
}

# Discover every Bazel test target tagged 'benchmark' AND containing a
# *_bench_test.go source — those are the packages we report on.
discover_targets() {
    bazel query 'attr(tags, "benchmark", //...)' 2>/dev/null \
        | grep -E '_bench_test$' \
        || true
}

run_one_target() {
    local target="$1"
    local pkg_dir
    pkg_dir="$(target_to_dir "$target")"

    if [ ! -d "$pkg_dir" ]; then
        echo "skip: $target — source directory $pkg_dir not found" >&2
        return 0
    fi

    local bench_log="" target_short=""
    bench_log="$(mktemp)"
    # shellcheck disable=SC2064  # we want the trap to capture the current path
    trap "rm -f '$bench_log'" RETURN

    echo "→ bench: $target (output → $pkg_dir/BENCH.md)" >&2

    #: bazel writes the actual bench output to bazel-testlogs/<path>/test.log
    #: regardless of --test_output mode; the streamed flag only mirrors it to
    #: the controlling terminal. We read the file directly to dodge stdout/
    #: stderr capture races, and capture the bazel rc separately so a flaky
    #: infra exit does not silently drop the report.
    set +e
    bazel test "$target" \
        --test_arg=-test.bench=. \
        --test_arg=-test.benchmem \
        --test_arg=-test.run='^$' \
        --test_arg=-test.benchtime="$BENCHTIME" \
        --test_output=errors \
        >/dev/null 2>&1
    local bazel_rc=$?
    set -e

    if [ "$bazel_rc" -ne 0 ]; then
        echo "warning: bazel test $target exited with $bazel_rc — report will reflect partial output" >&2
    fi

    #: //pkg/v1/codec:codec_bench_test → bazel-testlogs/pkg/v1/codec/codec_bench_test/test.log
    local test_log
    test_log="bazel-testlogs/$(echo "$target" | sed 's|^//||;s|:|/|')/test.log"
    if [ -f "$test_log" ]; then
        cp "$test_log" "$bench_log"
    else
        echo "warning: bazel test log not found at $test_log — report will be empty" >&2
    fi

    target_short="${target#//}"

    {
        echo "<!-- generated by scripts/bench/gen-bench-md.sh — do not edit by hand -->"
        echo "# Benchmarks — \`$pkg_dir\`"
        echo ""
        echo "Bazel target: \`$target_short\` (tag \`manual,benchmark\` — excluded from \`bazel test //...\`)"
        echo ""
        collect_machine_info
        echo "## Results"
        echo ""
        format_bench_table "$bench_log"
        echo ""
        echo "## Reproduce"
        echo ""
        echo "\`\`\`shell"
        echo "make sdk-bench-md   # regenerate every BENCH.md across the SDK"
        echo "# or the lower-level form:"
        echo "bazel test $target \\"
        echo "    --test_arg=-test.bench=. \\"
        echo "    --test_arg=-test.benchmem \\"
        echo "    --test_arg=-test.run='^\$' \\"
        echo "    --test_arg=-test.benchtime=$BENCHTIME"
        echo "\`\`\`"
    } > "$pkg_dir/BENCH.md"

}

main() {
    local -a targets
    if [ $# -gt 0 ]; then
        targets=("$@")
    else
        mapfile -t targets < <(discover_targets)
    fi

    if [ "${#targets[@]}" -eq 0 ]; then
        echo "no benchmark targets discovered — nothing to do" >&2
        return 0
    fi

    for target in "${targets[@]}"; do
        run_one_target "$target"
    done

    echo "BENCH.md written for ${#targets[@]} package(s)." >&2
}

main "$@"
