#!/usr/bin/env bash
# run.sh - balanced-pair comparisons for the C reference bench.
#
#   run.sh micro   HMAC (evp vs midstate) and parser (ours vs picohttpparser);
#                  runs entirely on this host, no load generator.
#   run.sh echo    epoll vs io_uring echo; needs the load generator on ANOTHER
#                  host (GEN_SSH, SUT_ADDR below).
#   run.sh all     both.
#
# Each comparison is PAIRS pairs. In a pair both variants run once; the order
# alternates (A,B then B,A, ...) so a drift in the machine does not favour one
# side. Per comparison: the raw rows (results/<stamp>/<name>.tsv) and a
# bootstrap 95% interval of the geometric mean of B/A (pairstat).
#
# Run it ON the host under test, build first (`make`). Numbers are only a measurement
# when that host is otherwise idle; SMOKE=1 shrinks everything and stamps the
# output "SMOKE", which is for checking the harness works, never for a figure.
#
# Environment:
#   PAIRS=6  HMAC_N=2000000  PARSE_N=2000000
#   PORT=9000 CONNS=256 MSG=64 SECS=20 WARM=4      echo shape
#   SUT_ADDR   address of THIS host as the generator sees it (echo only; required, no default)
#   GEN_SSH    command that opens a shell on the generator (echo only; required,
#              no default), e.g. "ssh -o BatchMode=yes <user>@<gen-host>"
#   GEN_DIR=cref-gen   directory on the generator that receives echo_client
#   SMOKE=1
set -euo pipefail

cd "$(dirname "$0")"
BUILD=build
MODE="${1:-}"
case "$MODE" in micro | echo | all) ;; *)
	echo "usage: run.sh micro|echo|all" >&2
	exit 2
	;;
esac

PAIRS="${PAIRS:-6}"
HMAC_N="${HMAC_N:-2000000}"
PARSE_N="${PARSE_N:-2000000}"
PORT="${PORT:-9000}"
CONNS="${CONNS:-256}"
MSG="${MSG:-64}"
SECS="${SECS:-20}"
WARM="${WARM:-4}"
GEN_DIR="${GEN_DIR:-cref-gen}"
SMOKE="${SMOKE:-0}"
if [ "$SMOKE" = 1 ]; then
	PAIRS=2 HMAC_N=200000 PARSE_N=200000 SECS=3 WARM=1 CONNS=32
	echo "*** SMOKE: harness check only, these are not measurements ***"
fi
if [ "$PAIRS" -lt 2 ]; then
	echo "PAIRS must be at least 2" >&2
	exit 2
fi

for b in hmac_bench parse_bench pairstat echo_epoll echo_uring echo_client; do
	[ -x "$BUILD/$b" ] || {
		echo "run.sh: $BUILD/$b missing, run make first" >&2
		exit 1
	}
done

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
OUT="results/$STAMP"
mkdir -p "$OUT"
./envinfo.sh >"$OUT/env.txt"
if [ "$SMOKE" = 1 ]; then echo "SMOKE=1" >>"$OUT/env.txt"; fi
cat "$OUT/env.txt"
echo "results: $OUT"

# kv LINE KEY: the value of KEY in a `k=v k=v ...` line.
kv() { tr ' ' '\n' <<<"$1" | awk -F= -v k="$2" '$1 == k { print $2; exit }'; }

# pair_order I: "A B" for an even pair, "B A" for an odd one.
pair_order() { if [ $(($1 % 2)) -eq 0 ]; then echo "A B"; else echo "B A"; fi; }

# compare NAME RUN_A RUN_B METRIC...: run the two commands PAIRS times in
# balanced order, each printing one k=v line, and collect every METRIC from the
# same pair of runs. One TSV and one pairstat summary per metric.
compare() {
	local name="$1" run_a="$2" run_b="$3" i first m line_a line_b
	local metrics=("${@:4}")
	for m in "${metrics[@]}"; do
		printf 'pair\torder\ta_%s\tb_%s\n' "$m" "$m" >"$OUT/$name.$m.tsv"
	done
	for ((i = 0; i < PAIRS; i++)); do
		first="$(pair_order "$i" | cut -d' ' -f1)"
		if [ "$first" = A ]; then
			line_a="$($run_a)" || exit 1
			line_b="$($run_b)" || exit 1
		else
			line_b="$($run_b)" || exit 1
			line_a="$($run_a)" || exit 1
		fi
		for m in "${metrics[@]}"; do
			if [ -z "$(kv "$line_a" "$m")" ] || [ -z "$(kv "$line_b" "$m")" ]; then
				echo "run.sh: pair $i has no value for $m (A: '$line_a' B: '$line_b')" >&2
				exit 1
			fi
			printf '%d\t%s\t%s\t%s\n' "$i" "$([ "$first" = A ] && echo AB || echo BA)" \
				"$(kv "$line_a" "$m")" "$(kv "$line_b" "$m")" >>"$OUT/$name.$m.tsv"
		done
		echo "  pair $i ($first first): ${metrics[0]} A=$(kv "$line_a" "${metrics[0]}") B=$(kv "$line_b" "${metrics[0]}")"
	done
	for m in "${metrics[@]}"; do
		{
			echo "== $name: $m, B over A =="
			tail -n +2 "$OUT/$name.$m.tsv" | cut -f3,4 | "$BUILD/pairstat" ||
				echo "pairstat: not computable for $m (a zero, missing or non-positive value)"
		} | tee -a "$OUT/summary.txt"
	done
}

micro() {
	echo "== hmac: A=evp (EVP_MAC_CTX_dup) B=midstate =="
	"$BUILD/hmac_bench" selftest
	compare hmac "$BUILD/hmac_bench evp -n $HMAC_N" "$BUILD/hmac_bench mid -n $HMAC_N" \
		ns_per_op cpu_us_per_op utime_us_per_op stime_us_per_op
	echo "== hmac: A=reinit (EVP_MAC_init(ctx,NULL,..) reuse) B=midstate =="
	compare hmac_reinit "$BUILD/hmac_bench reinit -n $HMAC_N" "$BUILD/hmac_bench mid -n $HMAC_N" \
		ns_per_op cpu_us_per_op
	echo "== parse: A=ours (S5 scope) B=picohttpparser (informational) =="
	compare parse "$BUILD/parse_bench own -n $PARSE_N" "$BUILD/parse_bench pico -n $PARSE_N" \
		ns_per_op cpu_us_per_op
}

# read_stat: the aggregate cpu line of /proc/stat.
read_stat() { awk '/^cpu /{print; exit}' /proc/stat; }

# busy_pct BEFORE AFTER: non-idle share and softirq share over the window.
busy_pct() {
	awk -v b="$1" -v a="$2" 'BEGIN {
		n = split(b, x, " "); split(a, y, " ");
		for (i = 2; i <= 9; i++) { d[i] = y[i] - x[i]; t += d[i] }
		if (t == 0) t = 1; idle = d[5] + d[6]; printf "cpu_busy_pct=%.1f softirq_pct=%.1f steal_pct=%.1f", \
			100 * (t - idle) / t, 100 * d[8] / t, 100 * d[9] / t }'
}

echo_install_client() {
	: "${GEN_SSH:?GEN_SSH is required for the echo comparison: the command that opens a shell on the load-generator host}"
	: "${SUT_ADDR:?SUT_ADDR is required for the echo comparison: the address of this host as the load generator sees it}"
	# shellcheck disable=SC2086
	$GEN_SSH "mkdir -p $GEN_DIR && cat > $GEN_DIR/echo_client.new && chmod +x $GEN_DIR/echo_client.new && mv $GEN_DIR/echo_client.new $GEN_DIR/echo_client" <"$BUILD/echo_client"
}

# echo_run SERVER: one measured run; prints the server line merged with the
# client's rps and the host CPU split.
echo_run() {
	local server="$1" spid cpu0 cpu1 cl srv f0 rc=0 i reqs
	"$BUILD/$server" -p "$PORT" >"$OUT/$server.out" 2>&1 &
	spid=$!
	sleep 0.5
	kill -0 "$spid" 2>/dev/null || {
		cat "$OUT/$server.out" >&2
		return 1
	}
	f0="$OUT/.stat0"
	(
		sleep "$WARM"
		read_stat >"$f0"
		kill -USR1 "$spid" 2>/dev/null || true
	) &
	# shellcheck disable=SC2086
	cl="$($GEN_SSH "$GEN_DIR/echo_client -h $SUT_ADDR -p $PORT -c $CONNS -s $MSG -t $SECS -w $WARM")" || rc=$?
	cpu1="$(read_stat)"
	cpu0="$(cat "$f0" 2>/dev/null || true)"
	# The server wakes every 100 ms, so TERM is seen within a tick; if it is
	# not, KILL it after 5 s rather than hang the bench.
	kill -0 "$spid" 2>/dev/null || {
		echo "run.sh: $server died during the run" >&2
		return 1
	}
	kill -TERM "$spid"
	for ((i = 0; i < 50; i++)); do
		kill -0 "$spid" 2>/dev/null || break
		sleep 0.1
	done
	if kill -0 "$spid" 2>/dev/null; then
		echo "run.sh: $server ignored SIGTERM for 5 s, killing it" >&2
		kill -KILL "$spid"
		wait "$spid" 2>/dev/null || true
		return 1
	fi
	wait "$spid" || {
		echo "run.sh: $server exited non-zero" >&2
		return 1
	}
	if [ "$rc" -ne 0 ]; then
		echo "echo_client failed (exit $rc): $cl" >&2
		return 1
	fi
	srv="$(cat "$OUT/$server.out")"
	reqs="$(kv "$srv" requests)"
	if [ -z "$reqs" ] || [ "$reqs" -le 0 ] || [ -z "$cpu0" ]; then
		echo "run.sh: $server reported no requests in the window: '$srv'" >&2
		return 1
	fi
	echo "$srv $(busy_pct "$cpu0" "$cpu1") client_rps=$(kv "$cl" rps) client_mean_latency_us=$(kv "$cl" mean_latency_us) client_mismatches=$(kv "$cl" mismatches)"
	echo "$server $srv | $cl" >>"$OUT/echo-lines.txt"
}

echo_cmp() {
	echo "== echo: A=epoll B=io_uring, $CONNS conns x $MSG B, ${SECS}s after ${WARM}s warm-up =="
	echo_install_client
	compare echo "echo_run echo_epoll" "echo_run echo_uring" \
		cpu_us_per_req utime_us_per_req stime_us_per_req client_rps cpu_busy_pct softirq_pct
}

case "$MODE" in
micro) micro ;;
echo) echo_cmp ;;
all)
	micro
	echo_cmp
	;;
esac
echo "done: $OUT/summary.txt"
