#!/usr/bin/env bash
# scripts/pre-commit/check-audit-coverage.sh — errs-audit coverage gate.
#
# The AST audits in internal/kernel/errs (code uniqueness, ADR 0005; PP-range
# ownership, ADR 0035) can only judge files that reach them. Under Bazel they
# reach them as runfiles of //:audit_sources, so a package that declares error
# codes but is missing from that filegroup list is audited by NOTHING — and it
# passes, silently, which is the worst shape a verification gap can take.
#
# This is not hypothetical. ADR 0020 records the same gap having already
# excluded ~10 namespaced emitters (ring + every logger middleware and sink),
# and it was found by reading rather than by any gate. internal/service/net/*
# (client, server, sse, tlsid, websocket) sat outside the list until this
# guard's own commit.
#
# So the rule is mechanical rather than remembered: a package containing an
# errs.Define call or an errs.Code constant MUST appear in //:audit_sources.
# A package that declares no codes is free to stay out, and so is one that
# only RE-EXPORTS codes (`const CodeX errs.Code = core.CodeX`, pkg/v1's form):
# it allocates nothing, so there is nothing for the audits to judge.
#
# The guard fails CLOSED. A step that cannot answer — a BUILD.bazel with no
# audit_srcs entry, a find that errors, a tree with no Go package, a file grep
# cannot read — stops it with a message rather than being read as "nothing
# to check", because a coverage gate that passes on an empty answer is the
# exact gap it exists to close.
set -euo pipefail

root="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"
cd "$root"

build="BUILD.bazel"
[ -f "$build" ] || { echo "✗ $build missing — run this from the repo root." >&2; exit 1; }

# Every package listed in the audit_sources filegroup, as a repo-relative path.
if ! audited="$(grep -oE '"//[^"]+:audit_srcs"' "$build" | sed 's|"//||; s|:audit_srcs"||' | sort -u)"; then
	echo "✗ $build lists no audit_srcs entry — refusing to pass on a list it could not read." >&2
	exit 1
fi

# Every directory holding a production Go file. Portable on purpose: this used
# `find -printf '%h\n'` with stderr discarded, and -printf is GNU-only — BSD
# find (macOS) rejects it, printed nothing, and the loop below saw an empty
# tree and passed. A find that fails now stops the script.
if ! packages="$(find internal pkg third-party -name '*.go' ! -name '*_test.go' | sed 's|/[^/]*$||' | sort -u)"; then
	echo "✗ find could not list internal/, pkg/ and third-party/ — refusing to pass on a tree it did not see." >&2
	exit 1
fi
[ -n "$packages" ] || {
	echo "✗ found no Go package under internal/, pkg/ or third-party/ — refusing to pass vacuously." >&2
	exit 1
}

# What declaring a code looks like. The typed constant is the house form —
# `const CodeX errs.Code = 0x…`, or `CodeX errs.Code = 0x…` inside a block —
# and it is the ONLY form in internal/core/codec, which formats its codes into
# its registry errors and never calls Define. The conversion
# `CodeX = errs.Code(0x…)` is the other spelling. Before this pattern, the
# conversion was the only code-declaration alternative, it matched nothing in
# the tree, and core/codec could have left //:audit_sources unnoticed. A hex
# literal is required on the right so a re-export, whose right side is a
# selector, stays exempt.
declares='errs\.Define|errs\.Code[[:space:]]*=[[:space:]]*0[xX]|errs\.Code\([[:space:]]*0[xX]'

missing=""
while IFS= read -r dir; do
	status=0
	declaring="$(grep -lE "$declares" "$dir"/*.go)" || status=$?
	# grep says 0 for a match, 1 for none, and anything else when it could
	# not read — which must not be mistaken for "no codes here".
	if [ "$status" -gt 1 ]; then
		echo "✗ could not scan $dir for code declarations — refusing to guess." >&2
		exit 1
	fi
	# Nothing in this package declares a code.
	[ "$status" -eq 0 ] || continue
	# Test files are excluded: an audit fixture deliberately declares
	# colliding codes to prove the audit bites.
	grep -qv '_test\.go$' <<<"$declaring" || continue
	grep -qxF "$dir" <<<"$audited" || missing="${missing}${dir}"$'\n'
done <<<"$packages"

if [ -n "$missing" ]; then
	echo "✗ these packages declare error codes but are absent from" >&2
	echo "  //:audit_sources in BUILD.bazel, so the errs AST audits never see" >&2
	echo "  them — a code collision or a stolen PP range there would pass:" >&2
	printf '%s' "$missing" | sed 's/^/    /' >&2
	echo >&2
	echo "  Add an audit_srcs filegroup to each package's BUILD.bazel and list" >&2
	echo "  it in the root audit_sources (CLAUDE.md rule 3 / ADR 0035)." >&2
	exit 1
fi
