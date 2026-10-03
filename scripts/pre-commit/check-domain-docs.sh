#!/usr/bin/env bash
# scripts/pre-commit/check-domain-docs.sh — root CLAUDE.md domain-list drift gate.
#
# The root CLAUDE.md is the first thing anybody reads, and it drifted badly:
# parallel branches merged its Purpose paragraph and its architecture tree by
# UNION, which produced two whole Purpose paragraphs, five domains described
# three times each, four competing core/ package lists, and — the part that
# matters — `events` documented only in the stale copy while `health` was in
# neither. Nothing failed. The file just quietly stopped describing the tree.
#
# Three mechanical invariants close that, all keyed on what is ON DISK rather
# than on a number someone maintains (CLAUDE.md rule 11, and the repository's
# own rule that a counter is measured and never deduced):
#
#   1. the architecture tree has ONE `core/` block, and it names EXACTLY the Go
#      packages under internal/core, no more and no fewer — every directory,
#      at any depth, that holds a non-test .go file, by its path relative to
#      internal/core (ADR 0155 §6). This compared the directories one level
#      down while the core was flat; grouped by family, that would have shrunk
#      to the family names, and a domain could have left the tree, or never
#      entered it, while its family stayed;
#   2. no domain is described twice in the Purpose section — a duplicate there
#      is the union-merge signature. Since ADR 0154 the domains are a TABLE
#      under a short Purpose paragraph, one row each, so the check is a
#      repeated row; the paragraph is still checked for a domain bolded twice,
#      should prose ever grow back into it;
#   3. the three ADR indexes (docs/adr/CLAUDE.md, docs/CLAUDE.md and this
#      file's Reference list) name exactly the ADRs on disk, once each. A
#      duplicated index row is what a re-run of a half-failed insertion script
#      leaves behind, and it happened here: the queue row was written twice and
#      neither copy was wrong, so nothing looked broken.
#
# It deliberately does NOT require every core package to have a row:
# `writer` and `logger/level` are parts of a domain rather than domains, and
# inventing a rule about which is which is how a guard starts lying.
#
# How the core/ block is read. It is the `├── core/` line, whose text after
# the name is a caption and is not read, and every `│` line under it, taken as
# ONE text so that a group or a note may wrap onto the next line:
#
#   ├── core/          domain interfaces + domain values
#   │                  crypto, net, proc,
#   │                  security/{authz, secret, session, token},
#   │                  observe/{logger, logger/{level, writer}, metrics,
#   │                           otel, trace},
#   │                  data/{cache, codec (the registry), codec/scratch}
#
#   - a parenthesised note is dropped first, whatever it holds, commas and
#     braces included — but not parentheses of its own, which do not nest;
#   - a brace group is expanded next, BEFORE any comma is split, innermost
#     first so that groups nest: what stands in front of the "{", back to the
#     nearest comma or "{", is put in front of every member, and what stands
#     after the "}", up to the next comma or "}", behind it — so
#     "logger/{level, writer}" names logger/level and logger/writer;
#   - what is left is split on commas; each name is trimmed, and a trailing
#     "/" is dropped.
#
# A name is therefore a package's full path. A family directory holds no Go
# code, so it is never a name of its own: it is the prefix of a group. A brace
# that does not pair into a group is refused rather than read as part of a path.
set -euo pipefail

root="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"
cd "$root"

doc="CLAUDE.md"
[ -f "$doc" ] || { echo "✗ $doc missing — run this from the repo root." >&2; exit 1; }

# --- 1. the architecture tree's core/ block vs the Go packages on disk -------

# Every Go package under internal/core, at any depth, as its path relative to
# internal/core: a directory holding a non-test .go file. What the go tool
# skips is skipped here — a testdata directory, and any file or directory whose
# name starts with "." or "_" — and a file at internal/core itself reads as
# ".". The prefix is stripped with sed rather than printed away with
# `-printf '%h'`: -printf is GNU find's, and the BSD find macOS ships refuses
# it (#260). This list and the block's are sorted, and compared, in the C
# locale, so the two orders cannot disagree whatever the caller's locale is.
core_pkgs="$(find internal/core -type f -name '*.go' ! -name '*_test.go' \
		! -name '.*' ! -name '_*' \
		! -path '*/testdata/*' ! -path '*/.*/*' ! -path '*/_*/*' \
	| sed -e 's#/[^/]*$##' -e 's#^internal/core$#.#' -e 's#^internal/core/##' \
	| LC_ALL=C sort -u)"
if [ -z "$core_pkgs" ]; then
	echo "✗ internal/core holds no Go package — is $root the repository root?" >&2
	exit 1
fi

# A package's NAME, the last element of its path, is what invariant 2 matches a
# bolded word against: `**authz**` names security/authz exactly as it named
# authz while the core was flat, instead of the family above it.
on_disk="$(printf '%s\n' "$core_pkgs" | sed 's#.*/##' | sort -u)"

# The reader below takes ONE block: a second either runs into it, when nothing
# separates them, or is never read at all. A second list of the same layer is
# exactly what a union merge of the tree leaves behind, so it is refused here
# rather than half-compared.
blocks="$(grep -c '^├── core/' "$doc" || true)"
if [ "$blocks" -gt 1 ]; then
	echo "✗ $doc: found $blocks 'core/' blocks in the architecture tree — there" >&2
	echo "  must be one. A second list of the same layer is what a union merge" >&2
	echo "  leaves behind; read, it would have been merged into the first or skipped." >&2
	exit 1
fi

# The block, read as the header describes. Empty lines are dropped with
# sed '/^$/d' and not grep -v '^$': grep exits 1 when it selects nothing,
# pipefail hands that status to the assignment, and set -e then ended the
# script — so a tree with no core/ block failed with no message at all, and
# the one below was never printed.
in_doc="$(awk '
	/^├── core\// { grab = 1; next }
	grab && /^│/  { print; next }
	grab          { exit }
' "$doc" \
	| sed 's/^│ *//' \
	| tr '\n' ' ' \
	| sed 's/([^)]*)//g' \
	| awk '
	{
		# The first "}" closes an innermost group, and the nearest "{" before
		# it opens that group, so neither the members nor the prefix hold a
		# brace. Every pass consumes one "}", which is what ends the loop.
		s = $0
		for (;;) {
			rb = index(s, "}")
			if (rb == 0) break
			lb = rb - 1
			while (lb > 0 && substr(s, lb, 1) != "{") lb--
			if (lb == 0) break
			p = lb - 1
			while (p > 0 && index(",{", substr(s, p, 1)) == 0) p--
			q = rb + 1
			while (q <= length(s) && index(",}", substr(s, q, 1)) == 0) q++
			prefix = substr(s, p + 1, lb - p - 1)
			sub(/^[ \t]+/, "", prefix)
			suffix = substr(s, rb + 1, q - rb - 1)
			n = split(substr(s, lb + 1, rb - lb - 1), member, ",")
			out = ""
			for (i = 1; i <= n; i++) {
				m = member[i]
				sub(/^[ \t]+/, "", m)
				sub(/[ \t]+$/, "", m)
				out = out (i > 1 ? "," : "") prefix m suffix
			}
			s = substr(s, 1, p) out substr(s, q)
		}
		print s
	}' \
	| tr ',' '\n' \
	| sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' -e 's#/*$##' -e '/^$/d' \
	| LC_ALL=C sort -u)"

if [ -z "$in_doc" ]; then
	echo "✗ $doc: could not find the 'core/' block of the architecture tree, or" >&2
	echo "  it names no package. The guard keys on a line starting '├── core/';" >&2
	echo "  if the tree was reshaped, update this guard in the same change (rule 11)." >&2
	exit 1
fi

case "$in_doc" in
*[{}]*)
	echo "✗ $doc: the core/ block has a brace that does not pair into a group —" >&2
	echo "  one left open, one closed that was never opened, or two groups side" >&2
	echo "  by side:" >&2
	printf '%s\n' "$in_doc" | sed -n '/[{}]/s/^/    /p' >&2
	exit 1
	;;
esac

missing="$(LC_ALL=C comm -23 <(printf '%s\n' "$core_pkgs") <(printf '%s\n' "$in_doc") || true)"
extra="$(LC_ALL=C comm -13 <(printf '%s\n' "$core_pkgs") <(printf '%s\n' "$in_doc") || true)"

if [ -n "$missing" ] || [ -n "$extra" ]; then
	echo "✗ $doc: the architecture tree's core/ block disagrees with the Go packages" >&2
	echo "  under internal/core." >&2
	[ -n "$missing" ] && { echo "  on disk but NOT in the doc:" >&2; printf '%s\n' "$missing" | sed 's/^/    /' >&2; }
	[ -n "$extra" ]   && { echo "  in the doc but NOT on disk:" >&2; printf '%s\n' "$extra"   | sed 's/^/    /' >&2; }
	echo "  A package is named by its path under internal/core, and a family's" >&2
	echo "  packages may be grouped as family/{a, b} — see this guard's header." >&2
	exit 1
fi

# --- 2. no domain described twice in the Purpose section --------------------

purpose="$(sed -n '/^Go SDK providing/p' "$doc")"
if [ -z "$purpose" ]; then
	echo "✗ $doc: the Purpose paragraph ('Go SDK providing …') is missing." >&2
	exit 1
fi

# More than one Purpose paragraph is itself the defect this guard was written for.
# BSD wc pads its count with spaces; tr keeps the message below readable.
count="$(printf '%s\n' "$purpose" | wc -l | tr -d '[:space:]')"
if [ "$count" -ne 1 ]; then
	echo "✗ $doc: found $count paragraphs starting 'Go SDK providing' — there must" >&2
	echo "  be exactly one. Two coexisting Purpose paragraphs is what a union" >&2
	echo "  merge leaves behind, and the newer one is not necessarily the fuller." >&2
	exit 1
fi

# Only bolded words that NAME a core package count. Restricting to what is on
# disk is what keeps the check precise: the paragraph also bolds ordinary
# emphasis ("**zero** go.opentelemetry.io imports"), and treating that as a
# domain would make the guard cry wolf until somebody disabled it.
dupes="$(printf '%s' "$purpose" \
	| grep -oE '\*\*[a-z][a-z/]*\*\*' \
	| tr -d '*' \
	| grep -Fxf <(printf '%s\n' "$on_disk") \
	| sort | uniq -d || true)"
if [ -n "$dupes" ]; then
	echo "✗ $doc: these domains are described more than once in the Purpose" >&2
	echo "  paragraph — the signature of a union merge:" >&2
	printf '%s\n' "$dupes" | sed 's/^/    /' >&2
	echo >&2
	echo "  Merge the clauses into one and delete the stale copy; do not leave" >&2
	echo "  both, because they will disagree." >&2
	exit 1
fi

# The domains themselves are a table in the Purpose section (ADR 0154): one row
# per domain, its name in backticks in the first cell. The prose rule above
# cannot see a row, so the union-merge signature is looked for where the
# domains now are — the same domain on two rows, the second one stale.
domains="$(awk '
	/^## Purpose/   { grab = 1; next }
	grab && /^## /  { exit }
	grab && /^\| `/ { print }
' "$doc" | sed -E 's/^\| `([^`]+)`.*/\1/')"
if [ -z "$domains" ]; then
	echo "✗ $doc: the Purpose section has no domain table — one row per domain," >&2
	echo "  its name in backticks in the first cell (ADR 0154). If the section was" >&2
	echo "  reshaped, update this guard in the same change (rule 11)." >&2
	exit 1
fi
dupes="$(printf '%s\n' "$domains" | sort | uniq -d)"
if [ -n "$dupes" ]; then
	echo "✗ $doc: these domains have more than one row in the Purpose table —" >&2
	echo "  the signature of a union merge:" >&2
	printf '%s\n' "$dupes" | sed 's/^/    /' >&2
	echo >&2
	echo "  Keep one row per domain and delete the stale one; do not leave both," >&2
	echo "  because they will disagree." >&2
	exit 1
fi

# --- 3. the ADR indexes against the ADRs on disk ----------------------------

adr_disk="$(ls docs/adr/[0-9][0-9][0-9][0-9]-*.md 2>/dev/null | grep -oE '/[0-9]{4}-' | tr -d '/-' | sort)"
[ -n "$adr_disk" ] || { echo "✗ docs/adr: no numbered ADR found — has the layout changed?" >&2; exit 1; }

check_adr_index() {
	file="$1"
	pattern="$2"
	label="$3"
	listed="$(grep -oE "$pattern" "$file" | grep -oE '[0-9]{4}' | sort)"

	dupes="$(printf '%s\n' "$listed" | uniq -d)"
	if [ -n "$dupes" ]; then
		echo "✗ $file: these ADRs are listed more than once in $label:" >&2
		printf '%s\n' "$dupes" | sed 's/^/    /' >&2
		echo "  A re-run of a partially-failed insertion leaves exactly this." >&2
		exit 1
	fi

	missing="$(comm -23 <(printf '%s\n' "$adr_disk") <(printf '%s\n' "$listed" | uniq) || true)"
	extra="$(comm -13 <(printf '%s\n' "$adr_disk") <(printf '%s\n' "$listed" | uniq) || true)"
	if [ -n "$missing" ] || [ -n "$extra" ]; then
		echo "✗ $file: $label disagrees with docs/adr/." >&2
		[ -n "$missing" ] && { echo "  on disk but NOT listed:" >&2; printf '%s\n' "$missing" | sed 's/^/    /' >&2; }
		[ -n "$extra" ]   && { echo "  listed but NOT on disk:" >&2; printf '%s\n' "$extra"   | sed 's/^/    /' >&2; }
		exit 1
	fi
}

check_adr_index "docs/adr/CLAUDE.md" '^\| `[0-9]{4}' "the Contents table"
check_adr_index "$doc"               '^- ADR [0-9]{4}' "the Reference list"
# The THIRD index. It was not checked, and it had silently lost 23 of the 74
# ADRs on disk — including every one written since 0061. An index nothing
# verifies is a list of the ADRs somebody remembered.
check_adr_index "docs/CLAUDE.md"     '^\| `adr/[0-9]{4}' "the Contents table"
