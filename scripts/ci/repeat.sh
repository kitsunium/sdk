#!/usr/bin/env bash
# scripts/ci/repeat.sh — run one test (or a few) many times, on the runner.
#
# The lane behind repeat.yml: a flaky test is closed on a measurement — N passes
# under -race on the runner it flaked on — and not on a rerun that happened to
# be green. Everything arrives through the environment (the workflow hands the
# dispatch inputs over as env:, never interpolated into a command), and every
# value is validated here, before any command is built from it.
#
#   REPEAT_MODULE      module directory of the census, default internal
#   REPEAT_PACKAGE     package pattern inside the module, default ./...
#   REPEAT_RUN         test names, [A-Za-z0-9_] separated by |, run as ^(…)$
#   REPEAT_COUNT       passes, 1..2000, default 100
#   REPEAT_BATCH       passes per go test invocation, 1..REPEAT_COUNT, default 100
#   REPEAT_RACE        true (default) or false
#   REPEAT_FAILFAST    true or false (default): stop at the first failing batch
#   REPEAT_GOMAXPROCS  optional, 1..256
#   REPEAT_DEADLINE    total minutes, 1..330, default 120: no batch starts after it
#   REPEAT_TEST_TIMEOUT  go test -timeout of one batch, default 30m
#   REPEAT_OUT         output directory, default repeat-out
#
# Writes REPEAT_OUT/raw.txt (the whole output), summary.md (also appended to
# $GITHUB_STEP_SUMMARY when set) and result.env. Exit 0 only when every pass
# ran and none failed: a run cut short by the deadline fails too, since the
# measurement it was asked for was not made.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
module="${REPEAT_MODULE:-internal}"
package="${REPEAT_PACKAGE:-./...}"
run="${REPEAT_RUN:-}"
count="${REPEAT_COUNT:-100}"
batch="${REPEAT_BATCH:-100}"
race="${REPEAT_RACE:-true}"
failfast="${REPEAT_FAILFAST:-false}"
maxprocs="${REPEAT_GOMAXPROCS:-}"
deadline_min="${REPEAT_DEADLINE:-120}"
test_timeout="${REPEAT_TEST_TIMEOUT:-30m}"
out="${REPEAT_OUT:-repeat-out}"

die() {
	printf 'repeat: %s\n' "$*" >&2
	exit 64
}

# Validation: an allow-list per input, never a deny-list.
[[ "$module" =~ ^[A-Za-z0-9_./-]+$ ]] || die "module: characters outside [A-Za-z0-9_./-]"
[[ "$module" != /* && ! "$module" =~ (^|/)\.\.(/|$) ]] || die "module: must be a relative path with no .."
[[ "$package" =~ ^[A-Za-z0-9_./-]+$ ]] || die "package: characters outside [A-Za-z0-9_./-]"
[[ ! "$package" =~ (^|/)\.\.(/|$) ]] || die "package: no .. path element"
[[ "$package" == ./* || "$package" == . ]] || die "package: must start with ./"
[ -n "$run" ] || die "run: a test name pattern is required"
[[ "$run" =~ ^[A-Za-z0-9_|]+$ ]] || die "run: only [A-Za-z0-9_] names joined by | are accepted"
[[ "$run" != \|* && "$run" != *\| && "$run" != *"||"* ]] || die "run: empty alternative"
[[ "$count" =~ ^[0-9]{1,4}$ ]] && [ "$count" -ge 1 ] && [ "$count" -le 2000 ] || die "count: 1..2000"
[[ "$batch" =~ ^[0-9]{1,4}$ ]] && [ "$batch" -ge 1 ] || die "batch: a positive integer"
[ "$batch" -le "$count" ] || batch="$count"
[[ "$race" = true || "$race" = false ]] || die "race: true or false"
[[ "$failfast" = true || "$failfast" = false ]] || die "failfast: true or false"
if [ -n "$maxprocs" ]; then
	[[ "$maxprocs" =~ ^[0-9]{1,3}$ ]] && [ "$maxprocs" -ge 1 ] && [ "$maxprocs" -le 256 ] || die "gomaxprocs: 1..256"
fi
[[ "$deadline_min" =~ ^[0-9]{1,3}$ ]] && [ "$deadline_min" -ge 1 ] && [ "$deadline_min" -le 330 ] || die "deadline: 1..330 minutes"
[[ "$test_timeout" =~ ^[0-9]{1,3}[smh]$ ]] || die "test timeout: like 30m"

# The module must be one the census names: a path that is not a module would
# run go test in whatever go.work or parent module it falls under.
if ! bash "$root/scripts/ci/go-modules.sh" | grep -Fxq -- "$module"; then
	die "module: $module is not a module of the census (scripts/ci/go-modules.sh)"
fi

mkdir -p "$out"
out="$(cd "$out" && pwd)"
raw="$out/raw.txt"
: >"$raw"

cpu_model() {
	case "$(uname -s)" in
	Linux) grep -m1 'model name' /proc/cpuinfo 2>/dev/null | sed 's/^[^:]*: *//' || true ;;
	Darwin) sysctl -n machdep.cpu.brand_string 2>/dev/null || true ;;
	*) printf '%s' "${PROCESSOR_IDENTIFIER:-unknown}" ;;
	esac
}
cpu_count() {
	case "$(uname -s)" in
	Linux) nproc 2>/dev/null || true ;;
	Darwin) sysctl -n hw.ncpu 2>/dev/null || true ;;
	*) printf '%s' "${NUMBER_OF_PROCESSORS:-unknown}" ;;
	esac
}
runner_cpu="$(cpu_model) x $(cpu_count)"

flags=(-count="$batch" -run "^(${run})\$" -timeout "$test_timeout")
[ "$race" = true ] && flags+=(-race)
[ "$failfast" = true ] && flags+=(-failfast)
[ -n "$maxprocs" ] && export GOMAXPROCS="$maxprocs"

cd "$root/$module"
export GOWORK=off
printf 'repeat: %s %s run=%s count=%s batch=%s race=%s go=%s\n' \
	"$module" "$package" "$run" "$count" "$batch" "$race" "$(go version)" | tee -a "$raw"

start="$(date +%s)"
done_passes=0
failed_batches=0
failures=0
cut=""
while [ "$done_passes" -lt "$count" ]; do
	if [ $(($(date +%s) - start)) -ge $((deadline_min * 60)) ]; then
		cut="deadline of ${deadline_min} min reached"
		break
	fi
	n=$((count - done_passes))
	[ "$n" -gt "$batch" ] && n="$batch"
	batch_flags=("${flags[@]}")
	batch_flags[0]="-count=$n"
	before="$(wc -l <"$raw")"
	printf '::group::batch %s..%s\n' "$((done_passes + 1))" "$((done_passes + n))"
	set +e
	go test "${batch_flags[@]}" "$package" 2>&1 | tee -a "$raw"
	rc="${PIPESTATUS[0]}"
	set -e
	printf '::endgroup::\n'
	done_passes=$((done_passes + n))
	# A failing test prints one unindented "--- FAIL:" line per failed pass. A
	# batch that fails with none (a panic, a timeout, a build error) still counts
	# as one failure: the measurement is a failure either way.
	f="$(tail -n +"$((before + 1))" "$raw" | grep -c '^--- FAIL: ' || true)"
	if [ "$rc" -ne 0 ]; then
		failed_batches=$((failed_batches + 1))
		[ "$f" -gt 0 ] || f=1
		failures=$((failures + f))
		if [ "$failfast" = true ]; then
			cut="stopped at the first failing batch (failfast)"
			break
		fi
	fi
done
elapsed=$(($(date +%s) - start))

first_trace=""
if [ "$failures" -gt 0 ]; then
	ln="$(grep -n -m1 -E '^(--- FAIL: |panic: |WARNING: DATA RACE|fatal error: |FAIL\s)' "$raw" | cut -d: -f1 || true)"
	if [ -n "$ln" ]; then
		from=$((ln > 30 ? ln - 30 : 1))
		first_trace="$(sed -n "${from},$((ln + 60))p" "$raw")"
	fi
fi

verdict=PASS
[ "$failures" -eq 0 ] || verdict=FAIL
# A pattern that names no test passes vacuously: a typo must not read as a
# measurement.
if [ "$verdict" = PASS ] && grep -q '\[no tests to run\]' "$raw"; then
	verdict=FAIL
	failures=1
	cut="the -run pattern matched no test in some package"
fi
[ -z "$cut" ] || [ "$failures" -gt 0 ] || verdict=INCOMPLETE
{
	printf '## repeat: %s\n\n' "$verdict"
	printf '| | |\n|---|---|\n'
	printf '| module / package | `%s` `%s` |\n' "$module" "$package"
	printf '| -run | `^(%s)$` |\n' "${run//|/\\|}"
	printf '| passes asked / run | %s / %s |\n' "$count" "$done_passes"
	printf '| failures | %s (in %s batch(es) of %s) |\n' "$failures" "$failed_batches" "$batch"
	printf '| race | %s |\n' "$race"
	printf '| GOMAXPROCS | %s |\n' "${maxprocs:-runner default}"
	printf '| runner CPU | %s |\n' "$runner_cpu"
	printf '| elapsed | %s s |\n' "$elapsed"
	[ -z "$cut" ] || printf '| cut short | %s |\n' "$cut"
	if [ -n "$first_trace" ]; then
		printf '\nFirst failure trace:\n\n```\n%s\n```\n' "$first_trace"
	fi
} >"$out/summary.md"
printf 'verdict=%s\npasses=%s\nfailures=%s\n' "$verdict" "$done_passes" "$failures" >"$out/result.env"
cat "$out/summary.md"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
	cat "$out/summary.md" >>"$GITHUB_STEP_SUMMARY"
fi
[ "$verdict" = PASS ]
