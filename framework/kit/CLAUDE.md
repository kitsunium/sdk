<!-- updated: 2026-10-17T00:00:00Z -->
# framework/kit — the facade a product imports

The framework in which every product is its own diagram, as a product sees
it. Every name here is a type alias, a typed constant or variable, or a
forwarder over `framework/internal/kit`, which holds the implementation and
its `CLAUDE.md` (the files, the rules, the invariants, the error codes).
Nothing is implemented here: a change of behaviour is made there.

## Contents

| File | Holds |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) — that `go doc` prints: a product's building blocks, the diagram, the app's databases, modules, and what `Main` understands |
| one file per file of the implementation | its exported names: aliases (`type Store[T any] = kit.StoreService[T]`), constants, variables, and forwarders (`//:` comment; `//go:noinline` where the implementation is — a position is recorded by the caller of the facade, which `callerFrame` reaches past this package) |
| `facade_external_test.go` | the declarations' positions, taken through the facade, point at the product's lines; `kit.Only` through the facade — a mount for another environment left out, its refusal at the product's line |
| `initgraph_external_test.go` | `TestTheFacadeImportsNoOptInSubsystem`: `go list -deps` of this package holds none of the packages an opt-in subsystem brings — the server engine, static files, the process sampler (`proc/process`, its spawn engine, `runtime/metrics`), the Studio's sse and profiling (ADR 0172). It skips without a go toolchain and under Bazel (`TEST_SRCDIR`), whose sandbox holds no module even where the runner put `go` on PATH; its gate is CI's `test-386` job, which runs `go test` over every module with the toolchain on PATH (rule 12). **ADR 0179** (since step 1), under the same skips and gate: `TestTheCoreImportsOnlyTheSubsystemsStillInIt` refuses, in `framework/internal/kit`'s graph, `pkg/v1/data/docstore`, `pkg/v1/data/sql`, `pkg/v1/data/queue`, `pkg/v1/app/statemachine`, `pkg/v1/app/mail/spool`, `pkg/v1/app/scheduler` and `pkg/v1/app/i18n` (`leaving`, each with its registration package and its step) — `database/sql` stays, `kit.Engine` returning `*sql.DB` (Q5) — but those the allow-list `stillInCore` names, held tight both ways (a package listed that the core no longer imports fails too); refuses `framework/kit` bringing one without its registration package; and holds `plug`'s direct imports to the standard library and `framework/model`. `TestTheSmallestProductLinksOnlyTheSubsystemsStillInTheCore` builds `cliprobe` with no registration import and refuses a text symbol (`go tool nm`) of those packages' core and service halves or of kit's generic store engines (`StoreService`, `docEngine`, `sqlEngine`, `sealedEngine`, `historied`) that the allow-list does not name, logging those it does. At step 1 the allow-list names all seven and the engines: step 2 takes the engines out, step 3 `docstore` and `sql`, step 4 `queue`, step 5 `statemachine`, step 6 `mail/spool`, step 7 `scheduler`, step 8 `i18n`, each in the change that moves it, and step 10 deletes the list. **Still a target**: the other half of ADR 0179 §7 — `cliprobe` built with each registration package does link them —, written with the first registration package, `framework/kit/store` (step 3) |
| `fresh_process_bench_test.go`, `testdata/cliprobe`, `BENCH.md` | a fresh process of the smallest product (one default fail-safe command), built and exec'd: what a status line pays per render when nothing is warm |
| `server/`, `studio/`, `config/yaml/`, `config/toml/` | the subsystems, each enabled by a blank import: HTTP in the server profile, the Studio's dev API, YAML and TOML configuration files. A product links only those it imports; the behaviour is in `framework/internal/kit` (see each `CLAUDE.md`) |
| `kittest/` | a product's tests on disk (ADR 0175): `NewDisk(t)` — a data directory of the test's own, the secrets kit keeps beside it, and the option that runs an app there, which wins over a `kit.InMemory()` given before it —, `Disk.Holding`, the files that keep a word in clear; it skips where the SDK's disk file system refuses the platform (Windows) |
| `storetest/` | the conformance suite of kit's stores (`storetest.Run`, `BackendConfig`), run by kit's tests and by each engine module's on its engine |
| `facade_generics_external_test.go` | no function of the facade names a generic alias of the facade in its signature: `Bind`, `Fallback`, `WakeOn`, `Replace` take `ikit.PortService`, `ikit.Operation`, `ikit.TopicService` — an instance of a generic alias read from export data races in go/types (golang/go#79035) |
| `facade_sync_external_test.go` | every exported name of the implementation is here, under its product-facing name, and nothing else is: the rename table (`StoreService` → `Store`, `AppConfigurer` → `AppOption`…) and the constructors left out (`NewStoreService`…) |

## Rules

- A name added to, renamed in or removed from `framework/internal/kit` is
  added, renamed or removed here in the same commit; the implementation keeps
  the role-suffixed name, the facade the name a product writes (the table is
  in `framework/internal/kit/CLAUDE.md`).
- A forwarder only forwards: no logic, no check, no extra parameter.
- A forwarder of a function that records a position is `//go:noinline`.
- A signature names a generic type by its internal name (`ikit.PortService`),
  never by the facade's generic alias; a doc link to a member is
  `[Type].Member`, since every facade type is an alias (ADR 0138).

## Verify

```sh
cd framework && GOWORK=off go vet ./kit/ && GOWORK=off go test ./kit/
bazel test //framework/kit:kit_test && bazel shutdown
```
