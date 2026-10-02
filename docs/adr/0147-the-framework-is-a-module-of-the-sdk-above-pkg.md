# ADR 0147 — the framework is a module of the SDK, above `pkg`, and a product imports nothing else

- **Status**: Proposed
- **Date**: 2026-09-28
- **Deciders**: SDK maintainers
- **Amended by**: [ADR 0157](0157-one-module-per-vendor-released-with-the-sdk.md) — §9: the vendor modules join the release chain; [ADR 0158](0158-distribution-mechanisms-are-the-frameworks-not-the-sdks.md) — the framework holds the distribution mechanisms, and the ssh identity as a connector
- **Amends**: [ADR 0005](0005-sdk-error-codes-dotted-quad.md) §Layout (layer `4` is allocated), [ADR 0007](0007-sdk-release-and-versioning.md) §2 (the release chain gains the framework and its connectors), [ADR 0068](0068-layer-firewall-is-a-checked-graph.md) (a fifth query)
- **Related**: [ADR 0001](0001-sdk-go-multimodule-layout.md) (the four layers), [ADR 0009](0009-pkg-public-module-resolvability.md) (a published module resolves without `replace`), [ADR 0012](0012-logger-writer-registry.md) (vendor code under `third-party/` or a module of its own), [ADR 0035](0035-pp-range-ownership-enforcement.md) (range ownership), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) / [ADR 0040](0040-changing-a-published-shape-while-v0.md) (what a published shape may do while v0), [ADR 0137](0137-a-lane-that-loops-over-modules-reads-the-census.md) (the module census), [ADR 0142](0142-one-key-per-subject-under-a-rotating-root-and-an-erasure-destroys-it.md) (the erasure the framework's privacy wave rests on)

## Context

`kitsunium/platform` holds **kit**: a framework in which every product is its
own diagram — services, endpoints, ports, stores, topics, workflows, declared
in Go, served, and described by a graph the product serves about itself. kit
sits on this SDK (`pkg/v1/*`, published versions only), and its ADR 0001 drew
the line this record moves: *the SDK holds the mechanisms, kit is the
framework*.

The platform's plan ("kit design-first", v7) moves the line for three reasons,
each one a property of the product, not of kit:

1. **A product imports only the SDK (D2).** The design files (`design/`)
   become the law of a product's structure and kit becomes a stand-alone tool
   — a CLI in the manner of `symfony` — that generates the product's
   shells and checks them against the design. A tool the product runs is not
   a library the product links. If the runtime stayed in `kitsunium/platform`,
   every product would link the Studio's server, the analyzer and
   `golang.org/x/tools`, and `kit check` could not refuse
   `import "github.com/kitsunium/platform/..."` in a product (D17), since the
   product needs it.
2. **One identity rule, written once.** A node's ID —
   `<service>/<kind>/<name>` — is derived today by two implementations (the
   runtime and the analyzer) held together by a test. Design files, the
   generator, the JSON Schema published for `design/`, `kit check` and the
   Studio now need it too. The grammar belongs with the types that carry it,
   in a module every one of them can import without importing the others.
3. **The SDK's rules are the rules a framework needs.** kit reports its own
   failures under `Major 0x7F` — the top of the *application* range ADR 0019
   reserves for products — so a product using kit loses the code space it was
   promised. kit's errors are partly typed (`errs.Define` for 40 codes) and
   partly strings (`kit.Error`'s `snake_case` codes, which the todo's clients
   branch on). Moving the runtime here puts it under the dotted-quad registry,
   the Public/Private split, the ownership audit and the build graph checks.

A framework is not a domain. `internal/core` holds ports and values,
`internal/service` their engines, `pkg/v1` the stable surface — and a
framework is an OPINION assembled from all of them: which store a
declaration uses, which queue a subscription consumes, which lifecycle
component starts first. Putting kit's 25 000 lines into `internal/service`
would give every domain's engine a view of every other's and a framework's
vocabulary; putting them into `pkg/v1` would make `pkg`'s release carry a
framework's churn and its dependency graph.

## Decision

### 1. `github.com/kitsunium/sdk/framework` is a Go module of its own, in `go.work`

The framework lives under `framework/` with its own `go.mod`, module path
`github.com/kitsunium/sdk/framework`, and is the sixth workspace module. Its
packages:

| Package | Holds |
|---|---|
| `framework/model` | the graph types — nodes, edges, the kinds, `Version` — and the ID grammar, written once (§4) |
| `framework/kit` | the runtime a product imports: declarations, the app, its lifecycle, its profiles (§5) — a facade of aliases and forwarders, as `pkg/v1/*` is over `internal/` |
| `framework/internal/kit` | its implementation, whose types name their role (`StoreService`, `AppConfigurer`); the facade keeps the names a product writes (`Store`, `AppOption`) |
| `framework/telemetry` | the telemetry port the runtime emits on, and its exporter (§6) |
| `framework/connectors/<engine>` | one database driver each, a Go module each (§7) |

The name `kit` is kept for the runtime package: the design's generated shells
are written in kit's words (`kit.Name`, `kit.Bind`), and a product that
migrates changes an import path, not a vocabulary.

### 2. The framework is a layer above `pkg/v1`, and the build graph says so

The framework imports `pkg/v1/*` and, to define its sentinels, the meta
package `internal/kernel/errs` — exactly the two things `pkg/v1` itself
imports to define its own. It imports no other `internal/` package: a
mechanism the framework needs and `pkg/v1` does not publish is added to
`pkg/v1` first, in its domain, under its own ADR, and the framework consumes
the published shape like any product would. So a framework feature can never
depend on an SDK detail no consumer could reach.

Nothing below depends on the framework, and the framework depends on no
`third-party/` code. Both are asserted on the build graph by
`scripts/check-layer-deps.sh` (ADR 0068's mechanism, a fifth and sixth query):

```
kind("go_library", deps(//internal/... + //pkg/... + //third-party/...)) intersect //framework/...        == ∅
kind("go_library", deps(//framework/..., 1)) intersect (//internal/... except //internal/kernel/errs)     == ∅
kind("go_library", deps(//framework/...)) intersect //third-party/...                                    == ∅
```

`framework/BUILD.bazel` declares the package group `framework_packages`
(`//framework/...`), the visibility of every package under `framework/**/internal`;
`//internal/kernel:kernel_consumers` gains `//framework/...` because
`kernel/errs` is how a sentinel is defined.

### 3. Layer `4` of the dotted-quad is the framework's

ADR 0005 laid codes out as `MM.LL.PP.SS` with `LL` = 0 meta, 1 kernel, 2 core,
3 service, and "others reserved". `LL = 4` is allocated to the framework:
every framework package owns a `PP` slot under `0.4.PP.*`, recorded in
`codeRangeOwners` in the change that introduces its codes (ADR 0035). The
first allocations:

| Range | Owner |
|---|---|
| `0.4.1.*` | `framework/model/internal/core` (declared there, re-exported by `framework/model`) |
| `0.4.2.*` | `framework/internal/kit` (declared there, re-exported by `framework/kit`) |
| `0.4.3.*` | `framework/telemetry` |
| `0.4.16.*`–`0.4.31.*` | reserved for `framework/connectors/*`, one each |

kit's `0x7F.1.*` codes move into `0.4.2.*` with the same serial order, so a
product keeps the whole application range ADR 0019 promised it. The
`errs` AST audits walk `framework/` beside `internal/`, `pkg/` and
`third-party/`, and every framework package declaring codes is a member of
`//:audit_sources`.

### 4. The graph types and the ID grammar are one package, stdlib plus `errs`

`framework/model` is the platform's `model` package moved, at `Version` 5.
Version 5 adds what a product is made of beyond its services — the binary and
its process roles, the short CLI command, the listener that is not HTTP, the
library shared between components, the presentation — and the edge between
two roles, which carries a versioned contract. It removes what the Studio no
longer does (§8): the respond, fail and delay mock modes and the control
event.

The ID grammar is written once, as data a schema generator can read
(`SegmentPattern`, `NamePattern`, `RoutePattern`, `IDPattern`) and as a parser
(`ParseID`) whose refusal, `INVALID_ID` (`0.4.1.1`), names the part that
failed and never the input:

```
id       = "external" | service | service "/" kind "/" name
         | "binary:" segment [ "/role/" segment ] | "library:" segment
service  = [ segment "." ] segment        ; "<module>.<service>"
segment  = [a-z][a-z0-9-]{0,62}
name     = [A-Za-z][A-Za-z0-9_.-]{0,62} | method " " path   ; the route, endpoints only
```

A binary, its roles and a library belong to no service, so they take a
scope prefix a service name cannot spell (`:` is outside `segment`) instead of
a reserved service name a product could collide with.

### 5. The runtime moves in waves, each one a release of the chain

The runtime moves in four waves, each shipped when it builds and its tests
pass under the SDK's rules, and each a release (§9):

| Wave | Content |
|---|---|
| V-A | the declarative core — service, endpoint, port, `Bind`/`Implement`/`Fallback`, commands and queries, the app and its lifecycle — plus the two profiles a product that is not a web server needs: the **short CLI** (parse, run once, exit with a typed status) and the **daemon without HTTP** (listeners, loops, stop on idle); a listener on a private socket as an inbound port; process roles; the fan-out primitive (a topic delivering to every subscription's queue) |
| V-B | stores and CQRS read models, over `docstore` and the connectors |
| V-C | workflows and their history, over `statemachine` |
| V-D | confidentiality, modules and watches — the erasure is already ADR 0142's |

Until a wave ships, the platform's `kit` keeps the code the wave will move;
after it, `platform/kit` becomes a facade of type aliases over
`framework/kit` for that wave's symbols, deprecated, and the platform keeps
only what a product does not link: the Studio, the analyzer, the generator,
`kit check`.

### 6. `kit.Error` stays the product's refusal, and `errs` is what it is made of

A product returns `*kit.Error` from a handler to choose the status and the
message its caller reads; its `Code` is a `snake_case` string the product's
clients branch on (`not_found`, `invalid_argument`, …). Those strings are a
wire contract the todo's clients already hold, so **they are preserved
byte-for-byte**. What changes is what stands behind them:

- the framework's own failures are `errs` sentinels in `0.4.2.*`, with a
  `Public` sentence, a `Private` diagnosis and, where a caller is at fault,
  an HTTP status set by `errs.WithHTTPStatus`;
- the wire codes keep their values and take the name `Wire*` (`kit.WireNotFound`), since `Code*` names a dotted-quad `errs.Code` in the SDK and the registry audit holds every `Code*` constant to that; the platform's facade keeps the old names as aliases;
- one function, `describe`, maps any error to the wire: a `*kit.Error` as it
  says; an `errs` error whose status is a 4xx as that status, the Reason in
  lower case as its code and its Public as the message; anything else as a
  500 whose body says `internal` and nothing more;
- `*kit.Error` implements `Unwrap`, so `errs.HasCode` sees through it to a
  sentinel it wraps.

So the two families never compete for one field: the string is what a client
branches on, the dotted-quad is what an operator greps for.

### 7. A database driver is a module of its own, and the framework links none

A connector (`postgres`, `mysql`, `sqlite`) is one driver behind the
framework's database port, and each is a Go module under
`framework/connectors/<engine>`, requiring the framework and its one driver.
A product that keeps its data in files links none of them; one that uses
PostgreSQL links `pgx` and nothing for MySQL. The drivers are already in the
root module's graph (the `third-party/db` writers and integration tests), so
the workspace gains no module Bazel has not already resolved.

### 8. The framework serves no action — the Studio shows, it does not act

The platform's decision D13 removes from the Studio every route that acts on
the running product: mocks and injected faults, `jobs/run`,
`workflows/fire`, `commands/dispatch`, `queries/ask`, `loops/wake`,
`privacy/erase|export|retention`, `profile/cpu`. **None of them exists in
`framework/kit`**, and nothing in the framework reads a mock set from outside
the process. A test's replacement (`kit.Replace`) stays, refused at the start
of a program `go test` did not build. The privacy operations a person's rights
require are commands of the product's own CLI profile, run by an operator with
the product's credentials — never an HTTP route a browser can reach.

### 9. One release, one version, every module of the chain

ADR 0007 released `pkg` and tagged the three `internal/*` modules in
LOCKSTEP at the same version, because `pkg` requires them exactly. The
framework requires `pkg`, and a connector requires the framework, so the
chain grows and stays in lockstep:

```
internal/kernel/vX  internal/core/vX  internal/service/vX  pkg/vX  framework/vX  framework/connectors/<engine>/vX
```

The chain is read from `go.work` — every workspace module but the root — so
a module added to the workspace is released without anyone editing a script,
exactly as ADR 0137's census made every lane reach a new module. The release
commit pins every intra-SDK requirement at the version being cut and drops
every intra-SDK `replace`. `compute-bumps.sh` emits `pkg` when `pkg/` or an
`internal/` module reaching it changed (unchanged), and `framework` when
`framework/` changed; either token cuts the whole chain once. The size is
still a maintainer's label (ADR 0135). One version for the whole chain is
the compatibility matrix: `framework/v0.12.0` requires `pkg/v0.12.0` and
nothing else is supported.

## Consequences / Semantics

- A product's `go.mod` names `github.com/kitsunium/sdk/framework` (and, if it
  keeps data in a database, one connector). It never names
  `github.com/kitsunium/platform`, and `kit check` can refuse the import.
- The platform and the statusline test bench consume the framework by
  pseudo-version (`go get github.com/kitsunium/sdk/framework@<sha>`) while a
  wave is on a branch, and by tag once it is released — never by `replace`.
- A framework change that needs a new mechanism lands the mechanism in
  `pkg/v1` in the same pull request or an earlier one; the framework cannot
  reach around it.
- The framework's documentation follows the SDK's: a `CLAUDE.md` and a
  generated `README.md` per package, `BENCH.md` beside every benchmark.

## Breaking changes

None in the SDK: `framework` is a new module, layer `4` was unallocated, and
no published shape of `pkg/v1` changes. For the platform, `model.Version`
goes from 4 to 5 and the mock modes other than `replace` and the control event
disappear from the document — which is D13, decided there.

## Alternatives considered

- **Keep the runtime in `kitsunium/platform` and publish the graph types
  alone.** Refused: it fixes the identity rule and leaves D2 unmet — every
  product links the platform, and D17's refusal of that import cannot be
  written.
- **The runtime in `pkg/v1/framework`.** Refused: `pkg` would carry a
  framework's churn and dependency graph into every consumer that only wants
  a logger, and ADR 0009's resolvability work would have to be redone for a
  surface ten times larger.
- **The runtime in `internal/service`, faceted by `pkg/v1`.** Refused: a
  framework is assembled from every domain, and the layer rule that a
  service engine sees no sibling engine's internals is exactly the rule a
  framework must break. A layer of its own lets it break none.
- **Keep `Major 0x7F` for the framework's codes.** Refused: that range is
  the product's (ADR 0019); a product built on the framework would be the one
  consumer the reservation does not protect.
- **Version the framework independently of `pkg`.** Refused for now: it
  needs a compatibility matrix — which framework release accepts which `pkg`
  range — that nobody maintains yet, while lockstep makes the matrix one
  line. Independent versions become possible once `pkg` is v1 and its surface
  is frozen.

## Deferred

- Independent versions for the framework and its connectors (§9, last
  alternative).
- The JSON Schema of `design/`, generated from `framework/model`'s patterns,
  is the platform's (its ADR 0010); this record only publishes the patterns.

## Verification

```
bash scripts/check-layer-deps.sh                        # the six queries empty
bazel test //internal/kernel/errs:errs_test              # framework codes audited and owned
GOWORK=off go -C framework test ./...                    # the module builds alone
bats scripts/release/test-compute-bumps.bats scripts/release/test-cut-tags.bats
grep -rn 'jobs/run\|workflows/fire\|commands/dispatch\|queries/ask\|loops/wake\|privacy/erase\|profile/cpu' framework/ ; test $? -eq 1
```

## References

- The platform's plan, "kit design-first" v7 — decisions D2, D13, D17, D22 and
  the round-5 amendments.
- The platform's ADR 0001 (the SDK holds the mechanisms, kit is the
  framework), which this record reverses for the runtime.
