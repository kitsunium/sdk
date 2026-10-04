#!/usr/bin/env bats
# BATS tests for the guards in scripts/pre-commit/, in five groups: the two
# guards that piped into an early-exiting reader, check-domain-docs.sh reading a
# core grouped by family, check-core-symmetry.sh holding the core to the
# service (ADR 0160), check-platforms.sh holding the platforms table to the
# cross-build matrix, and the portability of every guard. Each group says
# below what it pins and why.
#
# The early-exit cases were found by finishing the sweep ADR 0088 opened and
# recorded as INCOMPLETE: the former commit-msg hook was measured and fixed there,
# but two `scripts/pre-commit/` checks pipe into `grep -q` the same way and had
# not been. They fail in OPPOSITE directions, which is why both are covered:
#
#   check-ktn-phases-1-7.sh  fails CLOSED — a clean linter run is reported as
#                            "the linter did not complete".
#   check-audit-coverage.sh  fails OPEN   — a package that declares error codes
#                            and is absent from //:audit_sources passes.
#
# The mechanism is one line long in both: `grep -q` / `grep -qv` exits on its
# first match, the writer on the left of the pipe takes EPIPE, and `set -o
# pipefail` reports 141 — a status the surrounding code reads as "no match".
#
# Both tests were confirmed RED against the pre-fix form of their script. The
# fixture sizes below are the measured thresholds, not guesses; the per-cell
# measurements are recorded in the comment above each fix.

setup() {
  SCRIPTS="$(cd "${BATS_TEST_DIRNAME}" && pwd)"
  WORK="$(mktemp -d)"
  cd "$WORK"
}

teardown() {
  [ -n "${WORK:-}" ] && rm -rf "$WORK"
}

# --- check-ktn-phases-1-7.sh ------------------------------------------------

# A clean run whose output is large enough to outlast grep's early exit. The
# marker is placed FIRST because that is the cell that fires: grep -q closes
# the pipe as soon as it sees it, with the rest still unwritten.
@test "ktn-phases: a clean linter run is not reported as a failed run" {
  mkdir -p bin ws
  {
    echo '#!/usr/bin/env bash'
    echo "echo 'No issues found'"
    # 400 000 bytes after the marker: measured 40/40 failures pre-fix.
    echo "head -c 400000 /dev/zero | tr '\\0' 'x'"
    echo 'exit 0'
  } >bin/ktn-linter
  chmod +x bin/ktn-linter

  PATH="$WORK/bin:$PATH" run "$SCRIPTS/check-ktn-phases-1-7.sh" "$WORK/ws"

  # Pre-fix this is status 1 with "gate FAILS CLOSED" on stderr, because the
  # pipeline returned 141 and the elif went false.
  [ "$status" -eq 0 ]
  [[ "$output" != *"did not complete"* ]]
}

# The negative control. Same size, marker LAST: grep reads to the end, nothing
# takes EPIPE, and the pre-fix script already passed here. Keeping it makes the
# test above diagnostic rather than merely red — if both went red, the cause
# would be the volume, not the early exit.
@test "ktn-phases: negative control — marker last, same volume, already passed" {
  mkdir -p bin ws
  {
    echo '#!/usr/bin/env bash'
    echo "head -c 400000 /dev/zero | tr '\\0' 'x'"
    echo "echo 'No issues found'"
    echo 'exit 0'
  } >bin/ktn-linter
  chmod +x bin/ktn-linter

  PATH="$WORK/bin:$PATH" run "$SCRIPTS/check-ktn-phases-1-7.sh" "$WORK/ws"

  [ "$status" -eq 0 ]
}

# --- check-audit-coverage.sh ------------------------------------------------

# Builds a root where exactly one package declares error codes and is NOT in
# //:audit_sources. The guard must refuse. Pre-fix it passes, because the file
# that declares them is dropped by a 141 and the package looks empty.
#
# $1 = number of re-export lines after the declaring line.
mkroot() {
  mkdir -p internal/core/widget pkg third-party
  # One real declaration FIRST — the line `grep -qv` stops on — then enough
  # re-export lines to outrun the pipe. 200 lines measured 40/40 pre-fix;
  # 100 measured 0/40, so the fixture sits above the threshold on purpose.
  {
    echo 'package widget'
    printf '\tCodeOwn errs.Code = 42\n'
    for _ in $(seq 1 "$1"); do printf '\tCodeReexport errs.Code = core.Thing\n'; done
  } >internal/core/widget/codes.go
  # A filegroup entry must exist or the guard stops on "lists no audit_srcs
  # entry" — which would make the test green for the wrong reason. It names a
  # DIFFERENT package, so internal/core/widget is genuinely uncovered.
  echo 'filegroup(name = "audit_sources", srcs = ["//internal/kernel/errs:audit_srcs"])' >BUILD.bazel
}

@test "audit-coverage: an uncovered package with a large codes.go is still caught" {
  mkroot 400

  run "$SCRIPTS/check-audit-coverage.sh" "$WORK"

  # Pre-fix: status 0, no output — the gap passes.
  [ "$status" -eq 1 ]
  [[ "$output" == *"internal/core/widget"* ]]
}

# The negative control for the same guard: identical shape, small enough that
# the writer finishes before grep exits. Pre-fix AND post-fix this is red-for-
# the-right-reason, i.e. the guard catches the gap. If this one ever flips, the
# cause is the guard's logic, not the pipe.
@test "audit-coverage: negative control — same shape under the threshold" {
  mkroot 20

  run "$SCRIPTS/check-audit-coverage.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"internal/core/widget"* ]]
}

# --- check-domain-docs.sh ---------------------------------------------------

# ADR 0155 groups internal/core by family. The guard compared the tree's core/
# list with the directories ONE level down, which a nested core turns into the
# family names: a domain could then leave the tree, or never enter it, while
# its family stayed. It now compares every Go package, at any depth, with a
# block that may write a family's packages as family/{a, b}. Every case below
# was confirmed RED against the guard as it was, for the reason its comment
# gives, and green against the guard as it is.
#
# mkdocroot builds a root the rest of the guard passes — one Purpose paragraph
# and its table, one ADR in all three indexes — so that the core/ block alone
# decides each case. $1 is the block's lines under `├── core/`, `│` included;
# every further argument is a package made under internal/core, holding one
# non-test .go file.
mkdocroot() {
  block="$1"
  shift
  for pkg in "$@"; do
    mkdir -p "internal/core/$pkg"
    printf 'package %s\n' "${pkg##*/}" >"internal/core/$pkg/doc.go"
  done
  mkdir -p docs/adr
  echo '# ADR 0001' >docs/adr/0001-fixture.md
  echo '| `0001-fixture.md` | fixture |' >docs/adr/CLAUDE.md
  echo '| `adr/0001-fixture.md` | fixture |' >docs/CLAUDE.md
  {
    printf '# fixture\n\n## Purpose\n\nGo SDK providing a fixture.\n\n'
    printf '| Domain | What |\n|---|---|\n| `authz` | fixture |\n\n'
    printf '## Architecture at a glance\n\n```\ninternal/\n'
    printf '├── kernel/        primitives\n│                  errs\n'
    printf '├── core/          domain interfaces + domain values\n'
    printf '%s\n' "$block"
    printf '└── service/       implementations\n```\n\n'
    printf '## Reference\n\n- ADR 0001 — fixture — `docs/adr/0001-fixture.md`\n'
  } >CLAUDE.md
}

# The shape the guard reads now: families as brace groups, one group wrapped
# onto a second line and another nested in it, a note holding commas and
# braces, packages three deep. Red before: commas were split first, so the
# groups and the note came apart into fragments no directory is called —
# `secret (a note`, `writer}`, `metrics}`.
@test "domain-docs: a core grouped by family, written with brace groups, passes" {
  mkdocroot \
'│                  crypto, net,
│                  security/{authz, secret (a note, {with} braces)},
│                  observe/{logger, logger/{level, writer},
│                           metrics},
│                  data/{codec, codec/scratch}' \
    crypto net security/authz security/secret \
    observe/logger observe/logger/level observe/logger/writer observe/metrics \
    data/codec data/codec/scratch

  run "$SCRIPTS/check-domain-docs.sh" "$WORK"

  [ "$status" -eq 0 ]
}

# The regression the change exists for, written with full paths so the old
# reading parses the block the same way. Red before: both sides came down to
# `security`, and the guard passed with session missing from the tree.
@test "domain-docs: a package the block leaves out is named, though its family is listed" {
  mkdocroot '│                  security/authz, security/secret' \
    security/authz security/secret security/session

  run "$SCRIPTS/check-domain-docs.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"on disk but NOT in the doc:"$'\n'"    security/session"$'\n'* ]]
}

# The other direction: a member no package answers to, and a family named on
# its own, which holds no Go code and so is no package either. Red before: the
# refusal named `token}`, a fragment of the group, instead of either.
@test "domain-docs: a name that is no package is refused, a family named alone included" {
  mkdocroot '│                  security, security/{authz, token}' security/authz

  run "$SCRIPTS/check-domain-docs.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"in the doc but NOT on disk:"$'\n'"    security"$'\n'"    security/token"$'\n'* ]]
}

# A package is a directory the go tool would build: one holding only tests, a
# testdata tree, and a name starting with "." or "_" are not, and the block
# need not name them. Red before: every top-level directory counted, so
# `_scratch` and `.hidden` were required.
@test "domain-docs: tests, testdata and dot or underscore names are not packages" {
  mkdocroot '│                  security/authz' security/authz
  mkdir -p internal/core/security/authz/testdata internal/core/security/authz/authztest \
    internal/core/security/skipped internal/core/_scratch internal/core/.hidden
  echo 'package fixture' >internal/core/security/authz/testdata/fixture.go
  echo 'package authztest' >internal/core/security/authz/authztest/authz_test.go
  echo 'package skipped' >internal/core/security/skipped/_skipped.go
  echo 'package scratch' >internal/core/_scratch/scratch.go
  echo 'package hidden' >internal/core/.hidden/hidden.go

  run "$SCRIPTS/check-domain-docs.sh" "$WORK"

  [ "$status" -eq 0 ]
}

# A brace that does not pair into a group is refused as one. Read as text it
# surfaces as a name no package has, which points at the wrong fix — and that
# is what happened before: the refusal named `secret`, a package the disk does
# hold, and nothing said "brace".
@test "domain-docs: a brace left open is refused as a brace, not read as a path" {
  mkdocroot '│                  security/{authz, secret' security/authz security/secret

  run "$SCRIPTS/check-domain-docs.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"does not pair into a group"* ]]
}

# Red before: `grep -v '^$'` exits 1 when it selects nothing, pipefail handed
# that to the assignment and set -e ended the script — status 1 and no message,
# the explanation written under it never printed.
@test "domain-docs: a tree without a core/ block says so" {
  mkdocroot '' security/authz
  grep -v '^├── core/' CLAUDE.md >CLAUDE.md.new
  mv CLAUDE.md.new CLAUDE.md

  run "$SCRIPTS/check-domain-docs.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"could not find the 'core/' block"* ]]
}

# The union merge the guard's header records left four competing core/ lists.
# The reader takes one block, so a second must be refused rather than skipped:
# here it sits in a second, stale tree below the first. Red before: the first
# block matched the disk, the stale one was never read, and the guard passed.
@test "domain-docs: a second core/ block is refused, not ignored" {
  mkdocroot \
'│                  security/authz
└── service/       implementations
internal/
├── core/          a stale copy
│                  authz, token' \
    security/authz

  run "$SCRIPTS/check-domain-docs.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"found 2 'core/' blocks"* ]]
}

# --- check-core-symmetry.sh -------------------------------------------------

# ADR 0160: every service domain has a core, and every error code is declared
# in the core, at the service's path. mksymroot builds a tree the guard passes,
# and each case below plants ONE violation in it; every such case was seen red
# for the reason its comment gives. The tree holds what the guard must NOT
# refuse as well: a family's Go-internal helpers (observe/internal), a root
# family whose core is one package serving a directory of engines (net), a
# sub-contract beneath a domain owning only a range the core allocated
# (app/widget/level), a core member with no engine and no code (observe/otel),
# a re-export of a core code in a service, a Define in a service test, and a
# comment calling Define under the file's own import name.
mksymroot() {
  mkdir -p internal/kernel/errs \
    internal/service/app/widget internal/service/net/client \
    internal/service/observe/internal/otlp internal/service/observe/metrics \
    internal/core/app/widget/level internal/core/net \
    internal/core/observe/otel internal/core/observe/metrics
  cat >internal/kernel/errs/registry_ownership_external_test.go <<'EOF'
package errs_test

var codeRangeOwners = map[uint64]string{
	0x00_02_01_00: "internal/core/app/widget",
	0x00_03_01_00: "internal/core/app/widget",
	0x00_02_02_00: "internal/core/app/widget/level",
	0x00_02_03_00: "internal/core/net",
	0x00_02_04_00: "internal/core/observe/metrics",
}
EOF
  cat >internal/core/app/widget/codes.go <<'EOF'
package widget

import "github.com/kitsunium/sdk/internal/kernel/errs"

const CodeWidgetBroken errs.Code = 0x00_03_01_01
EOF
  cat >internal/core/app/widget/errors.go <<'EOF'
package widget

import "github.com/kitsunium/sdk/internal/kernel/errs"

var WidgetBroken = errs.Define(CodeWidgetBroken, "WIDGET_BROKEN", "broken", "service/app/widget: broken")
EOF
  cat >internal/core/app/widget/level/level.go <<'EOF'
package level

import kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

const CodeLevelUnknown kerrs.Code = 0x00_02_02_01

var LevelUnknown = kerrs.Define(CodeLevelUnknown, "LEVEL_UNKNOWN", "unknown", "level: unknown")
EOF
  cat >internal/core/net/net.go <<'EOF'
package net

import "github.com/kitsunium/sdk/internal/kernel/errs"

var Refused = errs.Define(0x00_02_03_01, "REFUSED", "refused", "net: refused")
EOF
  printf 'package otel\n\ntype AttrValue struct{}\n' >internal/core/observe/otel/otel.go
  printf 'package metrics\n\ntype Meter interface{}\n' >internal/core/observe/metrics/metrics.go
  cat >internal/service/app/widget/widget.go <<'EOF'
package widget

import (
	corewidget "github.com/kitsunium/sdk/internal/core/app/widget"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// The sentinel is declared with kerrs.Define(...) in the core, never here.
const CodeWidgetBroken kerrs.Code = corewidget.CodeWidgetBroken

func Fail(err error) error { return kerrs.Wrap(err, kerrs.WrapParams{Code: CodeWidgetBroken}) }
EOF
  cat >internal/service/app/widget/widget_test.go <<'EOF'
package widget

import "github.com/kitsunium/sdk/internal/kernel/errs"

var fixture = errs.Define(0x00_03_01_99, "FIXTURE", "fixture", "fixture")
EOF
  printf 'package client\n' >internal/service/net/client/client.go
  printf 'package otlp\n' >internal/service/observe/internal/otlp/otlp.go
  printf 'package metrics\n' >internal/service/observe/metrics/meter.go
}

# add_owner appends one codeRangeOwners entry, as a range is allocated: $1 the
# key, $2 the owning directory. awk rather than sed, whose newline in a
# replacement is not one on every BSD.
add_owner() {
  owners=internal/kernel/errs/registry_ownership_external_test.go
  awk -v entry="	$1: \"$2\"," '/^}$/ { print entry } { print }' "$owners" >owners.tmp
  mv owners.tmp "$owners"
}

@test "core-symmetry: a symmetric tree passes, with everything it must not refuse" {
  mksymroot

  run "$SCRIPTS/check-core-symmetry.sh" "$WORK"

  [ "$status" -eq 0 ]
}

# (a), the plain spelling.
@test "core-symmetry: a service file calling errs.Define is refused" {
  mksymroot
  cat >internal/service/app/widget/errors.go <<'EOF'
package widget

import "github.com/kitsunium/sdk/internal/kernel/errs"

var Stalled = errs.Define(0x00_03_01_02, "STALLED", "stalled", "stalled")
EOF

  run "$SCRIPTS/check-core-symmetry.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"internal/service/app/widget/errors.go declares an error code"* ]]
}

# (a) through an import name: `errs\.Define` alone would match kerrs.Define by
# accident and miss any other name, so the guard reads the import.
@test "core-symmetry: a Define under another import name is refused" {
  mksymroot
  cat >internal/service/app/widget/errors.go <<'EOF'
package widget

import e "github.com/kitsunium/sdk/internal/kernel/errs"

var Stalled = e.Define(0x00_03_01_02, "STALLED", "stalled", "stalled")
EOF

  run "$SCRIPTS/check-core-symmetry.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"internal/service/app/widget/errors.go declares an error code"* ]]
}

# (a) for a code with no Define: a constant allocates a value as surely, while
# the re-export the fixture already holds allocates nothing and passes.
@test "core-symmetry: a Code constant declared in a service is refused" {
  mksymroot
  cat >internal/service/app/widget/codes.go <<'EOF'
package widget

import "github.com/kitsunium/sdk/internal/kernel/errs"

const CodeWidgetStalled errs.Code = 0x00_03_01_02
EOF

  run "$SCRIPTS/check-core-symmetry.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"internal/service/app/widget/codes.go declares an error code"* ]]
}

# (b): an engine with no contract under internal/core.
@test "core-symmetry: a service domain with no core is refused" {
  mksymroot
  mkdir -p internal/service/app/gadget
  printf 'package gadget\n' >internal/service/app/gadget/gadget.go

  run "$SCRIPTS/check-core-symmetry.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"internal/service/app/gadget is a domain with no core"* ]]
}

# (b) for a root family: the domain is the family itself.
@test "core-symmetry: a root family whose core is missing is refused" {
  mksymroot
  rm -r internal/core/net

  run "$SCRIPTS/check-core-symmetry.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"internal/service/net is a domain with no core"* ]]
}

# (c), the mirror left behind: its engine went away, its layer-3 range stayed.
@test "core-symmetry: a codes-only mirror with no engine is refused" {
  mksymroot
  mkdir -p internal/core/app/widget/gone
  cat >internal/core/app/widget/gone/codes.go <<'EOF'
package gone

import "github.com/kitsunium/sdk/internal/kernel/errs"

var Gone = errs.Define(0x00_03_09_01, "GONE", "gone", "service/app/widget/gone: gone")
EOF
  add_owner 0x00_03_09_00 internal/core/app/widget/gone

  run "$SCRIPTS/check-core-symmetry.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"internal/core/app/widget/gone declares error codes but mirrors no service path"* ]]
}

# (c), the carve-out's edge: the sub-contract the fixture passes with a layer-2
# range is refused the moment it owns a range an engine allocated.
@test "core-symmetry: a sub-contract owning a layer-3 range is refused" {
  mksymroot
  add_owner 0x00_03_02_00 internal/core/app/widget/level

  run "$SCRIPTS/check-core-symmetry.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"internal/core/app/widget/level declares error codes but mirrors no service path"* ]]
}

# (c) at a domain: a core-allocated range does not exempt a domain's core, or a
# domain with no engine would pass (ADR 0160 §1).
@test "core-symmetry: a domain's core declaring codes with no engine is refused" {
  mksymroot
  mkdir -p internal/core/app/lonely
  cat >internal/core/app/lonely/codes.go <<'EOF'
package lonely

import "github.com/kitsunium/sdk/internal/kernel/errs"

var Alone = errs.Define(0x00_02_05_01, "ALONE", "alone", "lonely: alone")
EOF
  add_owner 0x00_02_05_00 internal/core/app/lonely

  run "$SCRIPTS/check-core-symmetry.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"internal/core/app/lonely declares error codes, and internal/service/app/lonely holds no engine"* ]]
}

# A family the guard does not know is checked by nothing, so it is refused.
@test "core-symmetry: a top-level directory that is no family is refused" {
  mksymroot
  mkdir -p internal/service/misc/thing
  printf 'package thing\n' >internal/service/misc/thing/thing.go

  run "$SCRIPTS/check-core-symmetry.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"internal/service/misc holds Go code but is no family this guard knows"* ]]
}

# Fails closed: without codeRangeOwners, (c) has nothing to judge against.
@test "core-symmetry: a tree without codeRangeOwners is refused, not passed" {
  mksymroot
  rm internal/kernel/errs/registry_ownership_external_test.go

  run "$SCRIPTS/check-core-symmetry.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"cannot read codeRangeOwners"* ]]
}

# --- check-platforms.sh -----------------------------------------------------

# The platforms table genindex reads and the cross-build matrix CI compiles are
# written in two files, so the guard holds them equal: the same cells, in the
# same order. mkplatroot copies the real table and workflow into the fixture,
# so each case changes one thing.
mkplatroot() {
  root="$(cd "$SCRIPTS/../.." && pwd)"
  mkdir -p scripts/ci .github/workflows
  cp "$root/scripts/ci/platforms.sh" scripts/ci/
  cp "$root/.github/workflows/bazel-ci.yml" .github/workflows/
}

@test "platforms: the table and the cross-build matrix name the same cells" {
  mkplatroot

  run "$SCRIPTS/check-platforms.sh" "$WORK"

  [ "$status" -eq 0 ]
  [[ "$output" == *"the same 12 cells, in the same order"* ]]
}

@test "platforms: a cell the matrix lacks is refused, both lists shown" {
  mkplatroot
  grep -v 'goos: solaris' .github/workflows/bazel-ci.yml >wf.tmp
  mv wf.tmp .github/workflows/bazel-ci.yml

  run "$SCRIPTS/check-platforms.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"are not one table"* ]]
  [[ "$output" == *"solaris/amd64"* ]]
}

@test "platforms: the same cells in another order are refused" {
  mkplatroot
  sed -e 's#^illumos/amd64$#TMP#' -e 's#^solaris/amd64$#illumos/amd64#' -e 's#^TMP$#solaris/amd64#' \
    scripts/ci/platforms.sh >t.tmp
  mv t.tmp scripts/ci/platforms.sh

  run "$SCRIPTS/check-platforms.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"are not one table"* ]]
}

@test "platforms: a table that prints nothing is refused, never passed" {
  mkplatroot
  printf '#!/usr/bin/env bash\nexit 0\n' >scripts/ci/platforms.sh

  run "$SCRIPTS/check-platforms.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"printed no cell"* ]]
}

@test "platforms: a workflow whose matrix cannot be found is refused" {
  mkplatroot
  sed 's/^  cross-build:/  cross-compile:/' .github/workflows/bazel-ci.yml >wf.tmp
  mv wf.tmp .github/workflows/bazel-ci.yml

  run "$SCRIPTS/check-platforms.sh" "$WORK"

  [ "$status" -eq 1 ]
  [[ "$output" == *"no cell found in the cross-build matrix"* ]]
}

# --- portability (#260) -----------------------------------------------------

# Every guard must work with the tools a Mac ships: /bin/bash 3.2 and the BSD
# find in /usr/bin, because `make lint` runs them on a developer's machine as
# well as in CI. CI runs bash 5 and GNU find, where none of the constructs below
# fails, so no run there can notice one coming back; reading the sources can.
# The pattern names what broke the guards before #260 — readarray / mapfile and
# associative arrays (bash 4), the case-changing expansions (bash 4) and
# `find -printf` (GNU) — which is a narrower claim than "portable", and the
# portability itself was proven by running the guards on both. A match on a
# comment line is ignored: the guards say what they avoid.
#
# Confirmed RED against the pre-fix scripts: it named
# check-domain-docs.sh:38 (-printf) and check-readme-drift.sh:35 (readarray).
@test "portability: no guard needs bash 4 or GNU find's -printf" {
  root="$(cd "$SCRIPTS/../.." && pwd)"
  pattern='readarray|mapfile|(declare|local|typeset) -[a-zA-Z]*A|\$\{[A-Za-z_][A-Za-z0-9_]*(,,?|\^\^?)[^}]*\}|-printf'
  hits="$(grep -nE "$pattern" \
      "$SCRIPTS"/*.sh "$root/scripts/gen-error-codes.sh" \
    | grep -vE '^[^:]+:[0-9]+:[[:space:]]*#' || true)"
  if [ -n "$hits" ]; then
    printf 'bash-4 or GNU-only construct in a guard:\n%s\n' "$hits"
    return 1
  fi
}
