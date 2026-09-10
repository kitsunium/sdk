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
# Two mechanical invariants close that, both keyed on what is ON DISK rather
# than on a number someone maintains (CLAUDE.md rule 11, and the repository's
# own rule that a counter is measured and never deduced):
#
#   1. the `core/` list in the architecture tree names EXACTLY the directories
#      under internal/core, no more and no fewer;
#   2. no domain is bolded twice in the Purpose paragraph — a duplicate there is
#      the union-merge signature.
#
# It deliberately does NOT require every core package to appear in the Purpose
# prose: `writer` and `logger/level` are parts of a domain rather than domains,
# and inventing a rule about which is which is how a guard starts lying.
set -euo pipefail

root="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"
cd "$root"

doc="CLAUDE.md"
[ -f "$doc" ] || { echo "✗ $doc missing — run this from the repo root." >&2; exit 1; }

# --- 1. the architecture tree's core/ list vs the directories on disk --------

on_disk="$(find internal/core -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort)"

# The tree block: the `├── core/` line plus every `│` continuation under it.
# Names are comma-separated and may carry a parenthesised note, e.g.
# "codec (+ scratch)" — the note is stripped, the name is not.
in_doc="$(awk '
	/^├── core\// { grab = 1; next }
	grab && /^│/  { print; next }
	grab          { exit }
' "$doc" \
	| sed 's/^│ *//' \
	| tr ',' '\n' \
	| sed -e 's/([^)]*)//g' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' \
	| grep -v '^$' \
	| sed 's#/.*##' \
	| sort -u)"

if [ -z "$in_doc" ]; then
	echo "✗ $doc: could not find the 'core/' block of the architecture tree." >&2
	echo "  The guard keys on a line starting '├── core/'; if the tree was" >&2
	echo "  reshaped, update this guard in the same change (rule 11)." >&2
	exit 1
fi

missing="$(comm -23 <(printf '%s\n' "$on_disk") <(printf '%s\n' "$in_doc") || true)"
extra="$(comm -13 <(printf '%s\n' "$on_disk") <(printf '%s\n' "$in_doc") || true)"

if [ -n "$missing" ] || [ -n "$extra" ]; then
	echo "✗ $doc: the architecture tree's core/ list disagrees with internal/core." >&2
	[ -n "$missing" ] && { echo "  on disk but NOT in the doc:" >&2; printf '%s\n' "$missing" | sed 's/^/    /' >&2; }
	[ -n "$extra" ]   && { echo "  in the doc but NOT on disk:" >&2; printf '%s\n' "$extra"   | sed 's/^/    /' >&2; }
	exit 1
fi

# --- 2. no domain bolded twice in the Purpose paragraph ---------------------

purpose="$(sed -n '/^Go SDK providing/p' "$doc")"
if [ -z "$purpose" ]; then
	echo "✗ $doc: the Purpose paragraph ('Go SDK providing …') is missing." >&2
	exit 1
fi

# More than one Purpose paragraph is itself the defect this guard was written for.
count="$(printf '%s\n' "$purpose" | wc -l)"
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
