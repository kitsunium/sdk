#!/usr/bin/env bash
# scripts/ci/consensus-fuzz.sh — the long fuzz lane of ADR 0152 (D15).
#
# Runs every fuzz target of the consensus domain for a bounded time each:
# the frame, snapshot-metadata, admin and token decoders of wire/, and the
# log record decoder, segment scan and tail classification of the engine,
# raft's step function under the simulator's invariants (FuzzStep, #924),
# which runs for its own time, FUZZ_STEP_TIME (10m), whatever the others
# run for, and the same three targets of kernel/fs/seglog, the segmented log
# extracted from the engine's (#820). A crash, a hang or a broken property fails the run; go writes the failing input under the
# package's testdata/fuzz/<Target>/, which the workflow uploads, and which a
# fix commits as a regression case. Local: scripts/ci/consensus-fuzz.sh 30s
# (FUZZ_STEP_TIME=30s for FuzzStep as well).
set -euo pipefail
per="${1:-${FUZZ_TIME:-5m}}"
step="${FUZZ_STEP_TIME:-10m}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
targets=(
	"./internal/service/app/consensus/wire FuzzParseMessage"
	"./internal/service/app/consensus/wire FuzzParseSnapshotMeta"
	"./internal/service/app/consensus/wire FuzzParseAdmin"
	"./internal/service/app/consensus/wire FuzzParseToken"
	"./internal/service/app/consensus FuzzParseRecord"
	"./internal/service/app/consensus FuzzClassifyTail"
	"./internal/service/app/consensus FuzzScanSegment"
	"./internal/service/app/consensus/raft FuzzStep"
	"./internal/kernel/fs/seglog FuzzParseRecord"
	"./internal/kernel/fs/seglog FuzzOpenTail"
	"./internal/kernel/fs/seglog FuzzScanSegment"
)
failed=0
for t in "${targets[@]}"; do
	pkg="${t% *}"
	name="${t#* }"
	time="$per"
	if [ "$name" = FuzzStep ]; then
		time="$step"
	fi
	echo "::group::$name ($pkg, $time)"
	if ! go test -run '^$' -fuzz "^${name}\$" -fuzztime "$time" "$pkg"; then
		echo "::error title=fuzz crasher::$name in $pkg; the input is under $pkg/testdata/fuzz/$name/"
		failed=1
	fi
	echo "::endgroup::"
done
exit "$failed"
