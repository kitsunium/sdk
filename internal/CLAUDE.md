<!-- updated: 2026-10-03T06:00:00Z -->
# internal/

## Purpose

The SDK's private layer. Everything here is blocked from external import by Go's `internal/` rule, which Bazel mirrors (ADR 0004; the firewall *between* the sublayers is a checked graph, not visibility — ADR 0068, below). Three sublayers model the SDK's dependency discipline:

```
kernel/    stdlib-only, generic primitives (no domain vocabulary), by family:
           concur/, collections/, fs/, and a root for errs, clock, backoff,
           semver and plugin (ADR 0155)
core/      domain interfaces and domain values
service/   concrete implementations of core contracts
```

## Dependency direction

Strictly top-down — enforced on the build graph by `scripts/check-layer-deps.sh` (`make lint` and CI — ADR 0068), not by `bazel build`:

```
kernel ──┐
core  ──┼──▶ service ──▶ (pkg/v1 re-exports / consumes)
         │
         └── pkg/v1 (direct for value types like Attr / Level)
```

| Layer | Imports allowed |
|---|---|
| `internal/kernel/**` | stdlib only |
| `internal/core/**`   | stdlib + `internal/kernel/*` + **sibling `internal/core/*`** |
| `internal/service/**`| stdlib + kernel + core + **sibling `internal/service/*`** — no module outside the SDK, the codecs under `data/codec/*` included (ADR 0156) |
| `pkg/v1/**` (consumes) | stdlib + kernel + core + service + **sibling `pkg/v1/*`** |

A service package may import another service package, and that is permitted
rather than tolerated: the firewall asserts a DIRECTION, and a lateral import
is not a direction. `check-layer-deps.sh`'s service query is
`deps(//internal/service/...) intersect (//pkg/... + //third-party/...)` — it
names what is above, and says nothing about siblings.

The row said "stdlib + kernel + core" until it was measured against the tree
and found to describe every lateral edge as a violation — 23 of them at the time
of writing. A table that contradicts the gate teaches a reader to distrust
whichever one they check second.

The edges are deliberately NOT listed here. A list in a document drifts the
moment someone adds an import, and a stale list is the same defect this
paragraph exists to correct. Ask the tree instead:

```sh
git grep -l '"github.com/kitsunium/sdk/internal/service/' -- 'internal/service/**/*.go' \
  ':!*_test.go' | while read -r f; do d=$(dirname "$f"); \
  grep -oE '"github.com/kitsunium/sdk/internal/service/[a-z/]+"' "$f" | tr -d '"' | \
  sed 's|github.com/kitsunium/sdk/||' | grep -v "^$d$" | sed "s|^|$d -> |"; \
  done | sort -u
```

A `pkg/v1` package may import another `pkg/v1` package, and that too is
permitted rather than tolerated. `check-layer-deps.sh`'s pkg query is
`deps(//pkg/...) intersect //third-party/...` — it names the one tree the public
module may not reach (ADR 0068 §Decision) and says nothing about siblings, and
every `pkg/v1` library is `//visibility:public`, so Bazel admits the edge too.
The facades are built on it: `client.New` and `server.TLS` take a
`tlsid.Identity`, `secret.KeyFile` returns a `crypto.Key`, `slogbridge` adapts a
`logger.Logger`, `proc` builds its refusal with `errs.New`, and `authz`, `codec`
and `token` type their codes as `errs.Code`.

The row said "stdlib + kernel + core + service" — the gap the service row had —
and an automated review read the omission as a layer rule. Ask the tree here
too, with `go list` rather than a grep (package doc comments quote `pkg/v1`
import paths, and a grep reports them as edges):

```sh
go list -f '{{$p := .ImportPath}}{{range .Imports}}{{$p}} -> {{.}}{{"\n"}}{{end}}' ./pkg/v1/... \
  | grep ' -> github.com/kitsunium/sdk/pkg/v1/' | sed 's|github.com/kitsunium/sdk/||g' | sort -u
```

A core package may import another core package on the same terms. The core
query, `deps(//internal/core/...) intersect (//internal/service/... + //pkg/...
+ //third-party/...)`, names what is above core and nothing beside it, and
`core/observe/metrics` and `core/observe/trace` both build on `core/observe/otel`'s shared attribute
model by decision (ADR 0051 §2). The row said "stdlib + `internal/kernel/*`", the gap the service row had.
The same `go list`, run from `internal/core` over `./...` with the grep on
`internal/core/`, lists the edges.

Any import going "upward" fails `make lint` and CI: `scripts/check-layer-deps.sh` asserts the direction on the build graph (ADR 0068). Visibility does not — Gazelle gives every package under `internal/` the visibility `//:__subpackages__`, which admits the whole repository, so an upward import builds. `.golangci.yml` ships a code-quality second-opinion ruleset only.

## Modules

Each sublayer is its own Go module (release independence + clean `go.sum` per layer):

| Module path | Build with |
|---|---|
| `github.com/kitsunium/sdk/internal/kernel`  | `cd internal/kernel && GOWORK=off go build ./...` |
| `github.com/kitsunium/sdk/internal/core`    | `cd internal/core && GOWORK=off go build ./...`   |
| `github.com/kitsunium/sdk/internal/service` | `cd internal/service && GOWORK=off go build ./...`|

`replace` directives in each `go.mod` resolve intra-repo dependencies without published pseudo-versions; `go.work` at the repo root resolves the workspace modules without `replace` — though a pattern never crosses a module boundary, so `go build ./...` at the repo root builds the root module alone; checking every module loops over `bash scripts/ci/go-modules.sh`. No `internal/*` module requires a module outside the SDK: every codec is written natively on the standard library (YAML as a named subset of YAML 1.2.2, whose full yaml.v3 reader is the opt-in `third-party/codec/yaml` module), and `proc/self` reads pseudo-versions with `internal/kernel/semver`, the stdlib primitive that replaced `golang.org/x/mod` (ADR 0156 §4) — its two other callers, `entitlement` and `selfupdate`, are the framework's (ADR 0158) and compare versions through `pkg/v1/data/semver`.

## Conventions

- **Role-suffix types.** Exported structs take a role suffix (`AttrValue`, `RecordEvent`, `WrapParams`) per ktn-linter `KTN-STRUCT-ROLE`. Short, clean names are re-exported at `pkg/v1/*` via type aliases (`Attr = AttrValue`).
- **Types that belong to one another share a file** (`KTN-STRUCT-PARTITION`).
  This reverses the former "one exported struct per file" rule: a value and
  the port that carries it, or a request and its response, were split across
  two small files that nothing else referenced, and the split had to be
  re-learned at every call site. `RequestValue` now sits beside
  `ResponseValue`, `AttrValue` beside `RecordEvent`. A file still holds ONE
  group: unrelated declarations sharing a file is `KTN-STRUCT-COHESION`, and
  it still fires.
- **Doc comments follow Effective Go.** Lead with the identifier name and name the parameters and return values inline (`Foo returns the X computed from y and z.`). No Javadoc-style `Params:` / `Returns:` sections — they were removed project-wide in PR #26. Every control block still takes a `//:` intent comment; every `case` label has its own intent comment.
- **Tests.** `*_internal_test.go` for white-box, `*_external_test.go` for black-box; table-driven with a `runCase` **closure declared inside the test function** — `runCase := func(t *testing.T, c tc) { … }` — so the linter's static analyser sees direct calls. NOT a shared helper: measured, **0 of 927** test files under `internal/` define one, while **437** declare the closure. This line said "helper" until three separate reviews in one day asked for a package-level function that has never existed here, and one rejection of that request cited `grep 'func runCase'` — a pattern the convention cannot produce.
- **Interface assertions live in `*_compliance.go`**: a compile-time `var _ Port = (*impl)(nil)` sits in a `<name>_compliance.go` file (or a test file), never beside the implementation — `KTN-IFACE-ASSERT-PLACEMENT` (17 occurrences).
- **Dotted-quad code ranges.** Each emitter package owns a 256-slot `PP` octet (ADR 0005 + ADR 0006). The `Code` constants and the `errs.Define` sentinels that name them are one
  group and may share a file — `failed.go`, `match.go`, `unknown.go` — or stay
  split as `codes.go` / `errors.go` where the package is large enough for the
  separation to earn itself. What is enforced is the CODE, not the filename. The AST audit enforces uniqueness and `reason = screamingSnake(varName)` OR `screamingSnake(CodeConst − "Code")` (the namespaced style — ADR 0006/0020). Every emitter package must ship an `audit_srcs` filegroup and appear in `//:audit_sources`, else it is unaudited under Bazel.

## Subtree

- `kernel/` — see `internal/kernel/CLAUDE.md`
- `core/`   — see `internal/core/CLAUDE.md`
- `service/`— see `internal/service/CLAUDE.md`

## Do NOT

- Move a logger-specific or codec-specific concept into `kernel/`. The kernel rule is both "stdlib-only" AND "generic". `level` was moved OUT for that reason.
- Call `fmt.Errorf` / `errors.New` in production. All errors go through `errs.Define` / `errs.Wrap`.
- Reference `service/*` from `core/*` or `kernel/*`; the direction is top-down.
- Add a third-party import in `kernel/*` or `core/*` — codec parsers and encoders live in `service/data/codec/*`.

## Verification

```
# Primary (Bazel — source of truth for CI)
bazel test --config=race //internal/...

# Layer firewall — every layer reaches only what is below it (ADR 0068)
bash scripts/check-layer-deps.sh
# expected: exit 0; the kernel query alone:
bazel query 'kind("go_library", deps(//internal/kernel/...)) except //internal/kernel/...'
# expected: empty result

# Fallback (go test — still works for quick local iteration)
GOWORK=off
for m in internal/kernel internal/core internal/service; do
  (cd $m && go test -race -cover ./...)
done
```
