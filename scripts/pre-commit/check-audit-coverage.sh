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
# A package that declares no codes is free to stay out.
set -euo pipefail

root="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"
cd "$root"

build="BUILD.bazel"
[ -f "$build" ] || { echo "✗ $build missing — run this from the repo root." >&2; exit 1; }

# Every package listed in the audit_sources filegroup, as a repo-relative path.
audited="$(grep -oE '"//[^"]+:audit_srcs"' "$build" | sed 's|"//||; s|:audit_srcs"||' | sort -u)"

missing=""
while IFS= read -r dir; do
	# Does this package declare codes at all? Test files are excluded: an audit
	# fixture deliberately declares colliding codes to prove the audit bites.
	if ! grep -lE 'errs\.Define|errs\.Code\(0x' "$dir"/*.go 2>/dev/null \
		| grep -qv '_test\.go$'; then
		continue
	fi
	grep -qxF "$dir" <<<"$audited" || missing="${missing}${dir}"$'\n'
done < <(find internal pkg third-party -name '*.go' ! -name '*_test.go' -printf '%h\n' 2>/dev/null | sort -u)

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
