#!/usr/bin/env bash
# scripts/pre-commit/check-core-symmetry.sh — the core mirrors the service (ADR 0160).
#
# ADR 0160 made two decisions a reader cannot keep by remembering them: every
# service domain has a core, and every error code is declared in the core, at
# the service's path, so a domain's codes are in one place and its facade's
# sentinels all alias the core. Both were applied by hand across nine tracks,
# and both reopen silently: a new engine that declares its own sentinels
# compiles, passes every test and every audit (check-audit-coverage.sh only
# asks that the package be audited, not WHERE it sits), and a codes-only core
# mirror whose engine moved still compiles too. So the rules are mechanical:
#
# THE DOMAINS. A domain is named by its path relative to its layer
# (internal/service or internal/core — ADR 0155):
#
#   - in the families that hold several domains — security, observe, data,
#     app — a domain is <family>/<name>, one per directory directly under the
#     family that holds a Go package at it or beneath it, `internal` excepted:
#     that is Go's visibility rule for the family's shared helpers
#     (observe/internal/otlp), not a domain;
#   - in the families that are one domain — crypto, net, proc — the domain is
#     the family itself, and what sits beneath it is that domain's.
#
#   A top-level directory of either layer that is neither refuses the run: a
#   new family is added to FAMILIES or ROOTS below in the change that creates
#   it, or it is checked by nothing.
#
# A Go package is what the go tool builds: a directory holding a non-test .go
# file, testdata and names starting with "." or "_" skipped (the same reading
# as check-domain-docs.sh). A file DECLARES A CODE when it calls Define on
# internal/kernel/errs under any import name (errs.Define, kerrs.Define, a dot
# import's bare Define), or declares a Code constant the way
# check-audit-coverage.sh counts one — a re-export (`CodeX errs.Code =
# core.CodeX`) allocates nothing and is not one. Full-line comments are not
# read, and neither is a _test.go file: an audit fixture declares codes on
# purpose.
#
# THE RULES. The run fails when:
#
#   (a) a production .go file under internal/service declares a code
#       (ADR 0160 §2: the service only uses what the core declares);
#
#   (b) a service domain has no core: internal/core/<the same path> must be a
#       Go package (ADR 0160 §1);
#
#   (c) an internal/core package declares a code and none of these holds:
#         1. it MIRRORS a service path — internal/service/<the same path> holds
#            a Go package at it or beneath it: the engine whose codes moved
#            here (ADR 0160 §2), or, for a family's own core (crypto, net,
#            proc, data/codec, observe/logger/writer), the directory of the
#            engines its contract serves;
#         2. it sits BENEATH a domain (never at one) and every range
#            codeRangeOwners assigns it was allocated by the core itself —
#            layer byte 2 (ADR 0160 §3: LL records the allocating layer). That
#            is a sub-contract of its domain with no engine of its own
#            (observe/logger/level, 0.2.17.*). A layer-3 range was allocated
#            to an engine, so it may only sit at that engine's path.
#       A domain's core is not exempt by being one: a core domain declaring
#       codes with no engine at its path is the other half of ADR 0160 §1's
#       "the two lists are the same list". The one core family member with no
#       engine, observe/otel, declares no code (it takes the caller's sentinel)
#       and is not judged.
#
# codeRangeOwners is read from internal/kernel/errs/
# registry_ownership_external_test.go, the hand-maintained table the ownership
# audit holds every declaration to (ADR 0035), so "the ranges this package
# owns" is the audit's own answer, not a second reading of the constants.
#
# The guard fails CLOSED, like its siblings: a layer that is missing, a find
# that errors, a tree with no package, a file grep cannot read, or an owner
# table with no entry stops it with a message rather than passing on an empty
# answer. It runs on macOS's /bin/bash 3.2 and BSD tools (scripts/CLAUDE.md).
set -euo pipefail

root="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"
cd "$root"

service="internal/service"
core="internal/core"
owners_file="internal/kernel/errs/registry_ownership_external_test.go"
errs_import="github.com/kitsunium/sdk/internal/kernel/errs"

# The families of ADR 0155, as the layers group them.
FAMILIES="security observe data app"
ROOTS="crypto net proc"

for layer in "$service" "$core"; do
	[ -d "$layer" ] || { echo "✗ $layer missing — run this from the repository root." >&2; exit 1; }
done

# packages_of prints the Go packages of a layer, as paths relative to it.
packages_of() {
	find "$1" -type f -name '*.go' ! -name '*_test.go' ! -name '.*' ! -name '_*' \
		! -path '*/testdata/*' ! -path '*/.*/*' ! -path '*/_*/*' \
		| sed -e 's#/[^/]*$##' -e "s#^$1\$#.#" -e "s#^$1/##" \
		| LC_ALL=C sort -u
}

if ! svc_pkgs="$(packages_of "$service")"; then
	echo "✗ find could not list $service — refusing to pass on a tree it did not see." >&2
	exit 1
fi
if ! core_pkgs="$(packages_of "$core")"; then
	echo "✗ find could not list $core — refusing to pass on a tree it did not see." >&2
	exit 1
fi
[ -n "$svc_pkgs" ] || { echo "✗ $service holds no Go package — refusing to pass vacuously." >&2; exit 1; }
[ -n "$core_pkgs" ] || { echo "✗ $core holds no Go package — refusing to pass vacuously." >&2; exit 1; }

# in_list reports whether a word is one of a space-separated list.
in_list() {
	case " $2 " in *" $1 "*) return 0 ;; esac
	return 1
}

# is_package reports whether a list of package paths names exactly <path>.
is_package() {
	local line
	while IFS= read -r line; do
		[ "$line" = "$1" ] && return 0
	done <<<"$2"
	return 1
}

# holds_package reports whether a list of package paths names <path> or a
# package beneath it.
holds_package() {
	local line
	while IFS= read -r line; do
		case "$line" in "$1" | "$1"/*) return 0 ;; esac
	done <<<"$2"
	return 1
}

violations=""
violation() { violations="${violations}${1}"$'\n'; }

# domains_of prints the domains of a layer and records, as a violation, every
# top-level directory holding Go code that is no family of ADR 0155.
# $1 = the layer, $2 = its packages. Sets the global `domains`.
domains_of() {
	local layer="$1" pkgs="$2" dir name sub child
	domains=""
	for dir in "$layer"/*/; do
		[ -d "$dir" ] || continue
		name="$(basename "$dir")"
		holds_package "$name" "$pkgs" || continue
		if in_list "$name" "$ROOTS"; then
			domains="${domains}${name}"$'\n'
		elif in_list "$name" "$FAMILIES"; then
			for sub in "$dir"*/; do
				[ -d "$sub" ] || continue
				child="$(basename "$sub")"
				case "$child" in internal | testdata | .* | _*) continue ;; esac
				holds_package "$name/$child" "$pkgs" && domains="${domains}${name}/${child}"$'\n'
			done
		else
			violation "$layer/$name holds Go code but is no family this guard knows — add it to FAMILIES or ROOTS (ADR 0155)"
		fi
	done
}

# is_domain reports whether a path, relative to its layer, is a domain by the
# rules above (a family member or a root family).
is_domain() {
	local family="${1%%/*}" rest
	if in_list "$1" "$ROOTS"; then
		return 0
	fi
	in_list "$family" "$FAMILIES" || return 1
	rest="${1#*/}"
	[ "$rest" != "$1" ] || return 1
	case "$rest" in */* | internal | testdata) return 1 ;; esac
	return 0
}

# errs_names prints the names a file imports internal/kernel/errs under: the
# alias, or `errs`, or `.` for a dot import. A blank import is skipped.
errs_names() {
	awk -v path="\"$errs_import\"" '
		/^import[ \t]*\(/ { block = 1; next }
		block && /^\)/    { block = 0; next }
		block || /^import[ \t]/ {
			line = $0
			sub(/^import[ \t]+/, "", line)
			sub(/^[ \t]+/, "", line)
			sub(/[ \t]*\/\/.*$/, "", line)
			n = split(line, f, /[ \t]+/)
			if (n == 1 && f[1] == path) print "errs"
			else if (n == 2 && f[2] == path && f[1] != "_") print f[1]
		}
	' "$1"
}

# What declaring a Code constant looks like — check-audit-coverage.sh's own
# spellings, so the two guards can never disagree about what a declaration is.
typed='(^|[[:space:](])Code[[:alnum:]_]*[[:space:]]+([[:alpha:]_][[:alnum:]_]*\.)?Code[[:space:]]*='
converted='(^|[[:space:](])Code[[:alnum:]_]*[[:space:]]*=[[:space:]]*([[:alpha:]_][[:alnum:]_]*\.)?Code\('
reexport='=[[:space:]]*[[:alpha:]_][[:alnum:]_]*\.[[:alpha:]_][[:alnum:]_]*[[:space:]]*(//.*)?$'

# declares reports whether one production file declares a code. Returns 0 for
# yes, 1 for no, and 2 when the file could not be read — never a guess.
declares() {
	local file="$1" body names name pattern status
	if ! body="$(grep -vE '^[[:space:]]*//' "$file")"; then
		# grep -v exits 1 when every line is a comment: nothing to declare.
		[ -r "$file" ] && return 1
		return 2
	fi
	if ! names="$(errs_names "$file")"; then
		return 2
	fi
	while IFS= read -r name; do
		[ -n "$name" ] || continue
		if [ "$name" = "." ]; then
			pattern='(^|[^[:alnum:]_.])Define[[:space:]]*\('
		else
			pattern="(^|[^[:alnum:]_.])${name}\\.Define[[:space:]]*\\("
		fi
		status=0
		grep -qE "$pattern" <<<"$body" || status=$?
		[ "$status" -gt 1 ] && return 2
		[ "$status" -eq 0 ] && return 0
	done <<<"$names"
	status=0
	# A process substitution, never a pipe into `grep -qv` — see the comment in
	# check-audit-coverage.sh on the 141 that pipefail makes of an early exit.
	grep -qvE "$reexport" < <(grep -E "$typed|$converted" <<<"$body" || true) || status=$?
	[ "$status" -gt 1 ] && return 2
	[ "$status" -eq 0 ] && return 0
	return 1
}

# declaring_files prints the production files of one package directory that
# declare a code, and returns 2 when one of them could not be read.
declaring_files() {
	local file status
	for file in "$1"/*.go; do
		[ -f "$file" ] || continue
		case "$file" in *_test.go) continue ;; esac
		status=0
		declares "$file" || status=$?
		[ "$status" -eq 2 ] && return 2
		[ "$status" -eq 0 ] && echo "$file"
	done
	return 0
}

# --- (a) no service package declares a code ----------------------------------

while IFS= read -r rel; do
	if ! found="$(declaring_files "$service/$rel")"; then
		echo "✗ could not read a file of $service/$rel — refusing to guess." >&2
		exit 1
	fi
	while IFS= read -r file; do
		[ -n "$file" ] || continue
		violation "$file declares an error code — declare it in $core/$rel (codes.go / errors.go) and use it from the service (ADR 0160 §2)"
	done <<<"$found"
done <<<"$svc_pkgs"

# --- (b) every service domain has a core --------------------------------------

domains_of "$service" "$svc_pkgs"
svc_domains="$domains"
[ -n "$svc_domains" ] || { echo "✗ found no service domain under $service — refusing to pass vacuously." >&2; exit 1; }
while IFS= read -r dom; do
	[ -n "$dom" ] || continue
	is_package "$dom" "$core_pkgs" ||
		violation "$service/$dom is a domain with no core — create the Go package $core/$dom with its codes, values and ports (ADR 0160 §1)"
done <<<"$svc_domains"

# The core's own top-level directories answer to the same families.
domains_of "$core" "$core_pkgs"

# --- (c) every code the core declares is accounted for ------------------------

[ -f "$owners_file" ] || { echo "✗ $owners_file missing — cannot read codeRangeOwners." >&2; exit 1; }
# "<layer byte> <owner>" per codeRangeOwners entry, e.g. "02 internal/core/x".
owners="$(awk '
	/^var codeRangeOwners = map\[uint64\]string\{/ { table = 1; next }
	table && /^\}/ { exit }
	table && match($0, /^[ \t]*0x[0-9A-Fa-f_]+:[ \t]*"[^"]+"/) {
		key = $0
		sub(/^[ \t]*0x/, "", key)
		sub(/:.*/, "", key)
		gsub(/_/, "", key)
		owner = $0
		sub(/^[^"]*"/, "", owner)
		sub(/".*/, "", owner)
		if (length(key) == 8) print substr(key, 3, 2), owner
	}
' "$owners_file")"
[ -n "$owners" ] || { echo "✗ read no entry of codeRangeOwners in $owners_file — refusing to judge (c) without it." >&2; exit 1; }

# core_only_ranges reports whether codeRangeOwners assigns the package at least
# one range and only ranges of layer 2.
core_only_ranges() {
	local layer owner seen=1
	while read -r layer owner; do
		[ "$owner" = "$core/$1" ] || continue
		[ "$layer" = "02" ] || return 1
		seen=0
	done <<<"$owners"
	return "$seen"
}

while IFS= read -r rel; do
	if ! found="$(declaring_files "$core/$rel")"; then
		echo "✗ could not read a file of $core/$rel — refusing to guess." >&2
		exit 1
	fi
	[ -n "$found" ] || continue
	holds_package "$rel" "$svc_pkgs" && continue
	if ! is_domain "$rel" && core_only_ranges "$rel"; then
		continue
	fi
	if is_domain "$rel"; then
		violation "$core/$rel declares error codes, and $service/$rel holds no engine — a domain's core with no service is half a domain (ADR 0160 §1)"
	else
		violation "$core/$rel declares error codes but mirrors no service path ($service/$rel holds no package) and owns a range the core did not allocate — move the codes to the core of the engine that raises them, or remove the orphaned mirror (ADR 0160 §2–3)"
	fi
done <<<"$core_pkgs"

if [ -n "$violations" ]; then
	echo "✗ the core no longer mirrors the service (ADR 0160):" >&2
	printf '%s' "$violations" | sed 's/^/    /' >&2
	echo >&2
	echo "  The rules are in this script's header; root CLAUDE.md rule 3 states them." >&2
	exit 1
fi
