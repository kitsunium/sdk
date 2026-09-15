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

# What declaring a code looks like — the same spellings the ownership audit
# (registry_ownership_external_test.go, classifyCodeSpec) counts as an
# allocation, so no package can be judged there and missed here:
#
#   - an errs.Define call;
#   - a Code-named constant typed as the Code type, whatever its right side —
#     `CodeX errs.Code = 0x…`, `= iota + 0x…`, a decimal, another constant —
#     spelled `errs.Code`, through any import name, or `Code` inside kernel/errs;
#   - a Code-named constant converted to it, `CodeX = errs.Code(…)`.
#
# The one exemption is the audit's: a right side that is a bare selector is a
# RE-EXPORT (`CodeX errs.Code = core.CodeX`, pkg/v1's form) and allocates
# nothing. This used to require a hex literal on the right, which left an iota
# group, a decimal and a constant expression invisible to the guard while the
# audit, once reached, would have judged them — so a package declaring its codes
# only that way stayed out of //:audit_sources and out of both audits.
define='errs\.Define'
typed='(^|[[:space:](])Code[[:alnum:]_]*[[:space:]]+([[:alpha:]_][[:alnum:]_]*\.)?Code[[:space:]]*='
converted='(^|[[:space:](])Code[[:alnum:]_]*[[:space:]]*=[[:space:]]*([[:alpha:]_][[:alnum:]_]*\.)?Code\('
reexport='=[[:space:]]*[[:alpha:]_][[:alnum:]_]*\.[[:alpha:]_][[:alnum:]_]*[[:space:]]*(//.*)?$'

# declaringFiles prints the production files of one package that declare a
# code, and fails only when grep could not read one of them.
declaringFiles() {
	local file status
	for file in "$1"/*.go; do
		case "$file" in *_test.go) continue ;; esac
		status=0
		grep -qE "$define" "$file" || status=$?
		[ "$status" -gt 1 ] && return 2
		if [ "$status" -eq 0 ]; then
			echo "$file"
			continue
		fi
		status=0
		# Redirection, never `grep -E … | grep -qvE …`. Under `pipefail`
		# (line 27) the pipeline's status is NOT "the last grep's": `grep -qv`
		# exits on its FIRST non-matching line, the producing grep takes
		# EPIPE, and 141 wins. `status` is then non-zero for a file that DOES
		# declare codes, the file is not printed, and a package whose only
		# declaring file is that one drops out of the scan — the gate passes
		# on the exact coverage gap it exists to close. Fails OPEN, silently.
		# Measured on this shape, 40 trials per cell, the non-re-export line
		# placed FIRST:
		#
		#     100 declaring lines (3.8 KB) →  0/40
		#     200 declaring lines (7.6 KB) → 40/40
		#    3200 declaring lines (122 KB) → 40/40
		#
		# and with that line placed LAST instead, 0/40 at every size — the
		# negative control that pins the cause to the early exit. The largest
		# declaring file in the tree today is 33 lines, so this is latent
		# rather than firing; the threshold is one generated codes.go away.
		# A process substitution keeps the inner grep out of the pipeline.
		grep -qvE "$reexport" < <(grep -E "$typed|$converted" "$file") || status=$?
		# The inner grep's status does NOT reach this line — a process
		# substitution drops it — and a grep that cannot read prints nothing,
		# which the outer `grep -qv` reports as 1: the same status as "every
		# match is a re-export". Measured on a file access(2) calls readable
		# and read(2) fails on (/proc/self/mem):
		#
		#     grep -qE … "$file"                 -> 2   the read error
		#     grep -qvE … < <(grep -E … "$file") -> 1   indistinguishable
		#     [ -r "$file" ]                     -> 0   TRUE, and proves nothing
		#
		# so `[ -r ]` is not what makes this safe and must not be read as such:
		# it is access(2) on the permission bits and never reads a byte. The
		# `$define` grep above is: reaching this line requires it to have exited
		# 1, and grep exits 1 only after reading to EOF with no error — a read
		# failure is 2 there and has already returned 2. `[ -r ]` covers the
		# narrower race of the file going away between the two greps.
		[ -r "$file" ] || return 2
		[ "$status" -eq 0 ] && echo "$file"
	done
	return 0
}

missing=""
while IFS= read -r dir; do
	# Test files are skipped inside: an audit fixture deliberately declares
	# colliding codes to prove the audit bites.
	if ! declaring="$(declaringFiles "$dir")"; then
		echo "✗ could not scan $dir for code declarations — refusing to guess." >&2
		exit 1
	fi
	# Nothing in this package declares a code.
	[ -n "$declaring" ] || continue
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
