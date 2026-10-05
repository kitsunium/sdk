<!-- updated: 2026-10-05T00:00:00Z -->
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
| `decl_gen.go` | written by kit gen from the design (ADR 0170): `Analyzer`, `Profile`, `IdleStop`, `Singleton`, `SingletonPer`, `Stop`, `Auth`, `AuthOptional`, `Catalog`, `NewID`, `DefaultCommand`, `FailSafe`, `Queued`, `NoTransaction`, `Listen`, `DataDir`, `Studio`, `Analyze`, `Logs`, `InMemory`, `Keeps`, `Migrations`, `MaxBody`, `RateLimit`, `Timeout`, `Bulkhead`, `Anyone`, `AnyUser`, `HoldSubject`, `SocketPath`, `SocketPer`, `AllowPeers`, `WakeEvery`, `WakeAt`, `From`, `MailAttempts`, `Requires`, `MinLength`, `NotReused`, `NotCommon`, `RateLimitPerClient`, `SetCookie`, `ClearCookie`, `ClientIP`, `UserAgent`, `Now`, `NewTicker`, `After`, `Log`, `Purpose`, `DeleteOnErasure`, `RetentionByProduct`, `Revisions`, `PerEnv`, `Generated`, `RotateEvery`, `KeepVersions`, `Optional`, `SecretStore`, `Required`, `ConfigFiles`, `Root`, `ReadModel`, `Telemetry`, `DesignDigest`, `MaxDeliveries`, `Parallelism`, `OwnStores` and `Transact`, each one call of its unexported body, measured to inline with the body inlined into it. Every body stays hand-written, in the files this document names — each wrapper's under its unexported name |
| one file per file of the implementation | its exported names: aliases (`type Store[T any] = kit.StoreService[T]`), constants, variables, and forwarders (`//:` comment; `//go:noinline` where the implementation is — a position is recorded by the caller of the facade, which `callerFrame` reaches past this package) |
| `facade_external_test.go` | the declarations' positions, taken through the facade, point at the product's lines |
| `fresh_process_bench_test.go`, `testdata/cliprobe`, `BENCH.md` | a fresh process of the smallest product (one default fail-safe command), built and exec'd: what a status line pays per render when nothing is warm |
| `server/`, `studio/`, `config/yaml/`, `config/toml/` | the subsystems, each enabled by a blank import: HTTP in the server profile, the Studio's dev API, YAML and TOML configuration files. A product links only those it imports; the behaviour is in `framework/internal/kit` (see each `CLAUDE.md`) |
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
