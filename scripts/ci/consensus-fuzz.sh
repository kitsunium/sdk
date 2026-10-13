#!/usr/bin/env bash
# scripts/ci/consensus-fuzz.sh — the long fuzz lane of ADR 0152 (D15).
#
# Runs every fuzz target of the consensus domain for a bounded time each:
# the frame, snapshot-metadata, admin and token decoders of wire/, and the
# log record decoder and tail classification of the engine. A crash, a hang
# or a broken property fails the run; go writes the failing input under the
# package's testdata/fuzz/<Target>/, which the workflow uploads, and which a
# fix commits as a regression case. Local: scripts/ci/consensus-fuzz.sh 30s.
set -euo pipefail
per="${1:-${FUZZ_TIME:-5m}}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
targets=(
	"./internal/service/app/consensus/wire FuzzParseMessage"
	"./internal/service/app/consensus/wire FuzzParseSnapshotMeta"
	"./internal/service/app/consensus/wire FuzzParseAdmin"
	"./internal/service/app/consensus/wire FuzzParseToken"
	"./internal/service/app/consensus FuzzParseRecord"
	"./internal/service/app/consensus FuzzClassifyTail"
)
failed=0
for t in "${targets[@]}"; do
	pkg="${t% *}"
	name="${t#* }"
	echo "::group::$name ($pkg, $per)"
	if ! go test -run '^$' -fuzz "^${name}\$" -fuzztime "$per" "$pkg"; then
		echo "::error title=fuzz crasher::$name in $pkg; the input is under $pkg/testdata/fuzz/$name/"
		failed=1
	fi
	echo "::endgroup::"
done
exit "$failed"
