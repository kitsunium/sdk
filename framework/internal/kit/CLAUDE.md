<!-- updated: 2026-10-02T19:57:17Z -->
# framework/internal/kit — the implementation of framework/kit

Declarations (`NewService`, the generic methods of `*Service`), the runtime
(`App`), and the product's self-description (`App.Graph`, the read-only
`/_kit/api` surface). Moved from `kitsunium/platform/kit` into the SDK's
framework module (ADR 0147): the platform keeps a deprecated facade of aliases
over the SDK's, and the tools a product runs but does not link — the
Studio's pages, the analyzer, the generator. The platform's root `CLAUDE.md`
holds the invariants; this file maps them to files. Every one still holds
here, but invariant 4's Studio is now the kit tool's: the product serves the
API it reads, never its pages, and no route that acts (D13). An ADR
numbered below 0100 here is the platform's (`kitsunium/platform/docs/adr/`:
0003 settings, 0004 stores and databases, 0005 CQRS, 0006 classification,
0007 versions, 0008 modules, 0010 design-first); 0104, 0138, 0147, 0148 and 0149
are the SDK's.

## The facade

A product imports `framework/kit`, never this package (Go refuses an
`internal/` import from outside the module). `framework/kit` holds one file
per file of this package that exports something: type aliases, typed
constants and variables, and forwarders marked `//:` (and `//go:noinline`
where the implementation is). The SDK's convention (`pkg/v1/*` over
`internal/`) applies: here, a type's name says its role; the facade keeps the
name a product writes.

| Here | framework/kit |
|---|---|
| `StoreService`, `TopicService`, `EndpointService`, `PortService`, `SettingService`, `WorkflowService`, `RecordsService`, `PasswordPolicyService` | `Store`, `Topic`, `Endpoint`, `Port`, `Setting`, `Workflow`, `Records`, `PasswordPolicy` |
| `SubscriptionWorker`, `AuthenticatorHandler` | `Subscription`, `Authenticator` |
| `StdioValue`, `DatabaseURLValue`, `EmptyValue`, `FieldRefValue` | `Stdio`, `DatabaseURL`, `Empty`, `FieldRef` |
| `ViolationMessage`, `WakeEvent`, `WrittenEvent`, `ChangeEvent`, `RevisionEvent` | `Violation`, `Wake`, `Written`, `Change`, `Revision` |
| `<X>Configurer` (method `<x>Configure`), for App, Command, Database, Endpoint, Expose, Listener, Loop, Mailer, Mount, Password, Port, Query, Secret, Setting, Static, Store, Subscription | `<X>Option` |
| `ModuleConfigurer`, `Keeper` (method `keep`), `AttrsProvider` | `ModulePart`, `Keepable`, `Principal` |
| `CommandLineConfigurer` (method `commandLineConfigure`), `ScopeValue`, `ActivityHandler` | `CLIOption`, `Scope`, `Activity` |

The `New*` constructors the declaring methods call (`NewStoreService`,
`NewEndpointService`, `NewError`…) and the hooks a subsystem package calls
as it is imported (`RegisterConfigFormat`, `EnableServer`, `EnableStudio`)
are this package's only: the facade does not export them. `framework/kit/facade_sync_external_test.go` fails when a
name is added to one side and not the other — its two tables are the list
above; `facade_external_test.go` proves the positions a declaration records
through the facade (`callerFrame` skips the facade's frames:
`facadePackage` in `node.go`). A facade signature never names a generic alias
of the facade — `Bind` takes `*ikit.PortService[Req, Resp]`, not
`*Port[Req, Resp]` —: an instance of a generic alias read from export data
races in go/types (golang/go#79035) for every tool that type-checks a
product's packages at once; `facade_generics_external_test.go` pins it. A
facade doc link to a member is `[Type].Member` (ADR 0138).

## Files

| File | Holds |
|---|---|
| `doc.go` | this package's short documentation; the long one — a product's building blocks, the diagram, the app's databases, modules, and what `Main` understands — is the facade's (`framework/kit/doc.go`) |
| `kit_compliance.go`, `*_interface.go` | the interface assertions (`var _ I = (*T)(nil)`) and the package's interfaces, each beside the file that uses it (`database_interface.go`, `expose_interface.go`, `node_interface.go`, `setting_interface.go`, `introspect_interface.go`, `mailer_interface.go`, `listener_interface.go`) |
| `service.go`, `node.go` | registration, node IDs, declaration positions (`callerPos`, `funcInfo`; a declaration records only its call's return addresses — `site`, resolved to file, line and package at the first read, `pos.file()`…, the facade's frames skipped then —, so a start that describes nothing pays one `runtime.Callers` per declaration; `pos.pkg`, the declaring package — `packageOf` reads it from the runtime's function name), the node-in-context marker |
| `module.go`, `mount.go`, `module_check.go`, `gomodule.go`, `module_database.go` | modules (ADR 0008): `NewModule` adopts its services (`adopt`, `rename`: qualified IDs, settings, problems), `Requires`, `qualifiedKey`/`flatKey`/`variableOf` (a module's `<module>.<name>`, `<module>-<name>` in a store, `<APP>_<MODULE>_<NAME>`); `Mount`, `Prefix`, a `*Module` as an `AppOption` — `mounts` resolves them into the app's modules and services, in start order (`orderModules`: the required ones first, needs before those who need them), `prefixOf` serves a module's routes under its prefix, `mountModules` sets `Module.Prefix` while the app runs; `moduleProblems`, every refusal with both declarations named (`foreignBuilders`: an operation's builders called from outside the module's Go module; `foreignPolicies`: a password policy put on a module's store from outside it); `goModuleOf` (the build's Go module of a package), `goModuleSource` (a position outside the product's Go module — a module's, a library's — relative to its Go module's root, which `moduleRoots` keeps for the source endpoint), `moduleBuild` in `build.go`; a module on the product's databases (`module_database.go`): `(*Module).keep`, `migrations` — `kit.Migrations`' value, both a `DatabaseOption` and a `ModulePart` —, `migrationSets(d)` (the modules' sets a database keeps, under `<module>_migrations`, then the product's), `moduleKeepers`, `moduleMigrationProblems` |
| `operation.go` | `Operation[Req, Resp]`: what `Fallback`, `Bind` and `Replace` take — an endpoint, a port (and, ADR 0005, a command or a query); unexported methods (`operation`, `perform` — its in-process run —, `edgeKind`), so only kit implements it. The `pipeline` every operation runs, whoever calls it: the validate tags, the policies around the steps of its kind (`steps`) and the handler or a test's replacement (`call`), a panic answered as an internal error (`panicked`); `inProcess`, an in-process run in its span — its `callSpec` names the app, the node, the edge and the span —, `admitted` requiring the caller's user when the operation asks for one; a request's run is one `outcome` (`endpoint.go`) |
| `command.go`, `query.go` | commands and queries (ADR 0005), internal until exposed — how one service reaches another: `Service.Command`, `Service.Query`, `Dispatch`, `Ask` (`inProcess`: a span of op `dispatch` or `ask`, its payloads in dev), the builders `Allow`, `Authorize`, `Key`, `Expose` (its `ExposeOption`s), `kit.Queued`; a command's `steps` inside its policies — its key, then its transaction (`transacted`, unless `kit.NoTransaction()`), and in it its authorization and its handler —; `drawn`, the pipeline in the order a dispatch runs it (`transactionMechanic`); no component for a command that is not queued |
| `access.go` | `kit.Principal`; `Allow` — a permission as data, the SDK's `authz.Check` over the auth data's attributes — and `Authorize` — a rule that needs the data —, both last before the handler; `refusal`: the one 403 whatever the cause, a NotFound (a rule hiding what exists) and an Unauthenticated kept, a fault logged |
| `keys.go` | a command's key: the SDK's memory lock, one locker per command and app (`lockerOf`, on the app's clock), a lease renewed while its run lasts (`lock.Keepalive`), the keys a run holds carried in its context — a run taking its own key again is `CodeCommandReentrant`, never a deadlock |
| `queued.go` | queued commands: the queue (`<data>/<service>/commands/<name>`, memory without a data directory; its step in the pipeline says where it waits — `queuePlace` — and the attempts), the envelope (trace, user, the dispatching node, key, input — decoded as encoding/json decodes), the dispatch's checks as its caller (`accept`: validate, authorize, key — a key waiting or running is deduplicated), the put — held until the commit inside a transaction, its key given back by a rollback —, the consumer (one dispatch per worker: `BatchSize` 1), the handling (a `handle` span in the dispatcher's trace, no edge; the Studio's fault, then the policies around the key, the transaction and the handler), the dead letters on the node |
| `expose.go` | `Expose`: an endpoint over an operation — named after it (`kit.Name` for a second), its authentication the operation's, the rest of the pipeline the operation's own, 202 for a queued command, a declared `dispatches` or `asks` edge (`EndpointInfo.Exposes`); `ExposeConfigurer` (every `EndpointConfigurer`, and the markers `kit.Anyone`, `kit.AnyUser` — `exposureOption`, which an endpoint does not take —, `EndpointInfo.Access`); `check` and `accessProblem`, at the start once every builder ran: who may run the operation — its `Allow` or `Authorize` (`guarded`), else one marker that agrees with its `authMode` —, both markers refused where they are given |
| `port.go` | ports (`Service.Port`, `Port.Call`: a span on the port, a test's replacement, then the bound operation's own `perform`), `Fallback`, `Bind`; `resolvePorts` — the start's one rule, shared with the analyzer: `Bind` (the last of a port wins), else the one `Service.Implement` among the mounted services, else the fallback; every problem at once; `wire` keeps the decision for the run (`App.wired`), `unmount` forgets it; `describe` draws the binding as a declared edge |
| `replace.go` | `kit.Replace`: `replacements` (refused outside a test binary — `testBinary`, `testing.Testing`: what the linker set when go test built the binary, which no run-time state of a product can turn on — a flag named test.v, a call to testing.Init —, `TestATestBinaryIsWhatTheLinkerSays` —, nil, or of a service not mounted), `replaced` (the handler an operation's run swaps in: counted, marked `mock: replace`, a panic answered as a handler's), `mockList` (the Studio's mocks in dev and the replacements) |
| `endpoint.go`, `decode.go`, `schema.go` | typed endpoints — for what HTTP itself is about, a webhook —, and `Service.Implement` — the one endpoint with no route (`routeless`, `model.ExposePrivate`), marked with the port it implements, named by its `kit.Name`, else its handler's name, else its port's; strict request binding (path, query, header, cookie), `model.Schema` from the SDK's `codec/jsonshape` (what encoding/json writes: members, omission, null — a member with omitempty/omitzero is never null; a request's path/query/header/cookie fields named by their tag, `json:"-"` ones found with `reflect.VisibleFields`); a JSON body is read by the SDK's `codec/strictjson` (`readJSON`, which words its refusals the way kit always did — never the body, a JSON Pointer at most) |
| `auth.go` | the auth handler node, `Auth`/`AuthOptional` (an `OperationOption`: an endpoint's, a command's, a query's), the principal in ctx (`UserID`, `AuthData`, `WithUser`), app-level auth checks — one handler whenever an endpoint, a command or a query asks for a user; the warning that every `Allow` refuses when the auth data is no `kit.Principal` |
| `request.go` | per-request helpers: `SetCookie`/`ClearCookie` (hardened), `ClientIP`, `UserAgent`, `Log`; the app's clock for product code — `Now`, and `NewTicker`/`After` for loops written by hand, so a test's manual clock drives them; `responseState` |
| `store.go`, `index.go`, `store_engine.go` | kit's store — a store that remembers runs on `historied` (history.go) — — a port (ADR 0004); `kit.ReadModel()` marks it derived from others (ADR 0005: `StoreInfo.ReadModel`, nothing else changes): `Store[T]` holds a `storeEngine[T]` (unexported; every call takes the caller's context; refusals are docstore's sentinels, which `said` words for every engine — on a database `sqlStoreRefusal` too), opened at start as the app places the store — `docEngine`, the SDK's document store (`docstore`: one snapshot `<service>/<name>.json` plus one small file per write in `<name>.json.d/`, folded back; a write costs one entity; in a transaction it records what a write replaced, `before`/`after`, and holds its hooks until the commit), or `sqlEngine` on a database (store_sql.go) —, made a node, observed, its refusals said in kit's words (`said`: NotFound, Conflict, Invalid, Unavailable, kit's failure codes; a key quoted only where kit always quoted one; `WriteUnconfirmed` is a warning, the write stands); write modes (`upsert`, `insertOnly`, `replaceOnly`); four funnels every write goes through — `write`, `modify`, `remove`, and a workflow's `transitionWrite` —, which take the data's writer turn for a store in files or memory (`turn`; none for a store `apart` — a cache, `InMemory` on the store, and kit's store of data keys (`dataKeysRole`) —, which `outside` keeps out of every transaction and `placementOf` never puts on SQLite) and tell the store's watches once kit confirms a write (`notify`, `feeds`: `watch.go`; held until the commit in a transaction) — a store no watch hears pays one atomic load; indexes declared and checked by kit (`Unique`, `Index`), kept by the SDK (`Lookup`, `Find`, `Filter`); `onDelete` and `onWrite` hooks, kit's lists, called by the store's announcements outside every lock; `folder`, a sibling interface `docEngine` implements, folds a store's files after an erasure (ADR 0006); `Store.open` in the data directory and `openSQL` on a database put a store whose entity has a member kit seals — or any product store at rest of an app that keeps data keys: `opensSealed` — behind the sealing engine (`seal_store.go`), `sealsAtRest` says when; `started`, kit's own store of keys finishing a re-wrap at start, wherever it lives; the store's privacy hooks — its `privacy` options (`declarePrivacy`), `removeUnlessHeld` in `Delete`, the subject index as one more spec at start (`kitIndexes`), `privacyInfo` in the description, `redacted` for the data browser; its revisions (`kit.Revisions`, `declareRevisions`, revisions*.go) — files that keep versions a store no longer declares refuse the start, `STORE_VERSIONS` |
| `store_sql.go`, `store_sql_migrate.go` | stores on a database (ADR 0004, step 2): `sqlEngine` over the SDK's `docstore.SQLStore` — every call through `callContext` (transact_sql.go): the level's transaction on its database, else the pool, within `<name>-timeout`; `retold`, a write's hooks told again when kit's transaction rolls back —; `startSQL` and `openSQL` (`sqlTable`, `openSQLEngine` for any document type — a sealing store's `sealedDoc` too, its sealed members strings kept as written —; the index keys hashed — `referenceKeys.indexKey`, a third HKDF subkey —, the rows filed again when their fingerprint changed, `reindex`, a sealing store's refused while a record does not open; `sqlOpenRefusal`); `KeyedEntries`, what a sealing store lists its records by; `sealedEngine.onDatabase`; `databaseOf`, `storesOnDatabase`; the tables (`tableName` is `model.TableName`: `<service>__<store>`, `__history`, `<service>__<workflow>__workflow`; `kitTables`, `tableProblems`); kit's own set (`setKit`, `kit_migrations`): its registry `kit_tables` (`tableRecord`: a table's version and its index fingerprint), `resolveKitSet` — a new table at the next generation (`gen<<32 \| nameDigest(name)`, FNV-32a) —, `applyKitSet` (retried past `MIGRATION_OUT_OF_ORDER`), `planKitSet` |
| `transact.go`, `transact_sql.go`, `transact_turn.go`, `transact_warn.go` | `kit.Transact` (ADR 0004): a transaction (`txn`) and its levels (`unit`, in the context: `unitOf`, `withoutUnit`), `beginTransaction`/`finish` (a span of op `transaction`: database, backend, outcome, effects, savepoint), the one database (`joinCall`, `claimLocal`, `spanRefusal` — `CodeTransactionSpan`), the effects held until the commit (`hold`, `heldEffect`, `let`; `inside` for a hook that stays in the process), what runs when it ends whatever its outcome, before them (`atEnd`: the data keys' stripes), the rollback (`undoStep`s newest first, the effects dropped, `retell`); on a database each level's transaction or savepoint parked on a goroutine inside the SDK's `Transact` (`sqlScope`, `openScope`, `close`); the data's writer turn (`writerTurn`: a transaction alone, the writes of none shared, `turnWait` 10s; `sharesTurn`); in dev, the warning of a command or a `kit.Transact` block whose code writes two databases (`transactionWarnings`) |
| `topic.go` | fan-out over one SDK queue per subscription — a publish in a transaction encoded at the call and held until the commit —; `MaxDeliveries` and `Parallelism` are `DeliveryOption`s — a subscription's, a queued command's; trace context in the envelope; a publish wakes declared loops; consumers are woken by the SDK queue itself (ADR 0104 there), so `pollInterval` (5 s) only bounds what another process publishes |
| `consumer.go` | a queue's consumer, whoever's: `openQueue` — a subscription's, a watch's, a queued command's queue, its lease, retry delay and attempts — and `consumer.deliver`, the one delivery a subscription and a watch run: the trace restored, a span on the consumer from where the message came over a delivers edge, the loop told, a panic the handler's failure; `pollInterval` |
| `workflow.go`, `workflow_ports.go` | state machine declared by kit — events (`On`), timers (`After`, `At`), guards (`When`), hooks, `stateField`, `stateType` — and run by the SDK's engine (`statemachine`): `definition` (kit's own OnTransition hook first: the Studio's transition and census events), `said`/`refusal` (the engine's errors in kit's words, unchanged), `report` (OnTransition failures → problems; panics → log with stack), `observe` (a span per transition the loop fires), `onLoop` (the Runtime view); the store's writes and deletions reach the engine (`changed`/`forget` → `Changed`/`Deleted`) through `Store.watch`, whose hooks have an identity: a failed start takes its own back, and `stop` removes the workflow's own, never another's. A transition is one transaction (ADR 0004): `inTransaction` around `Start` and `Fire` — the writer turn taken before the entity —, one begun at `observe` for the loop's; the `OnTransition` hooks held until the commit (`afterCommit` reports a held hook's failure). `storePort` is the engine's port over `Store` (`transitionWrite`: a key taken or an entity gone is an answer); `workflowJournal` keeps the records in `<svc>/<name>.workflow.json`, in the format kit always wrote, and `sqlJournal` in `<svc>__<workflow>__workflow` when the store lives on a database |
| `mailer.go` | outbound mail: `Send` → the SDK's mail spool (in a transaction, the outbox ID minted at the call and the mail held until the commit, then `SendWithID`) (`mail.NewSpool`: queue, retries, lease, duplicates, dead letters) → `outboxTransport` (kit's span and loop run per attempt) → SDK `mail` transport (SMTP, or `NewCapture(1)` in dev); the Studio's `mailbox` is kept from the spool's events (`observe`); the SMTP URL is the mail connector's secret `smtp-url` (`kitSecret`), read again before every delivery — `current` rebuilds the transport when it changed |
| `secret.go`, `secretstore.go`, `secretcmd.go` | declared secrets (`Service.Secret`: provided — `Optional` when the product can do without it: absent is no problem, `Present` says whether it is set now, and its run reads through `firstFound`, where the start looks, at every use —, or `Generated` with `RotateEvery`/`KeepVersions`, never optional; `Value`, `Present`, `Seal`/`Open`, `Sign`/`Verify` in `secret` spans; a rotation loop on the app's clock, whose rotator keeps what `inUse` says a version still needs and runs `afterRotation` after — kit's own `data-key`'s: `kitOwn`, read from `KIT_<NAME>`, kept as `kit-<name>`, `envName`/`keptName`/`variable`); where the environment keeps them (`KIT_SECRETS`: unset = beside the data, `<data>/.secrets/store` + `key`, or memory; `env`; `memory`; `file:<dir>` + `KIT_SECRETS_KEY`) — the variable `<APP>_<NAME>` (or `_FILE`) always first, kit's own under `KIT_` and `kit-` in the store; `Main`'s `secrets list\|set\|rotate` (`list` says `operator, optional` and `absent`) |
| `database.go`, `database_place.go`, `database_check.go`, `database_run.go`, `database_migrate.go`, `database_view.go`, `migratecmd.go` | databases (ADR 0004, steps 1 and 2) — declarations, placement, problems, the run, migrations, what the graph sees, the command: `Database(name, engine, …)` — an `AppOption`, its position recorded — with `Keeps` (a `Keepable`: a service, a store, a module) and `Migrations` (the SDK's `sql.Migration`; its value is also a `ModulePart`); the public `Engine` (Dialect, Describe → `DatabaseURL`, Open with a `current` URL read again before each connection) that the engine modules (`connectors/*`, own Go modules) implement; `placementOf` (a store kept by name, then its service, then its module, then the default database — the one without `Keeps` —, which never takes kit's own `kit.privacy` stores: `kit.Keeps(kit.Privacy)` places them, `asKept`; `InMemory` wins; a store `apart` — kit's data keys — is never on SQLite, whose one writer a transaction may hold while a key is written: it stays in the data directory; the store lives on its database once open, in dev without a URL in the data directory); `databaseProblems` (all at once, at the `kit.Database`: name, two defaults, kept twice, unmounted, memory store kept by name, derived name taken, two of kit's tables under one name; then the URL — `<APP>_<NAME>_URL`, `_FILE`, `<name>-url` in the store, `<data>/<name>.sqlite` for SQLite —: missing outside dev, unreadable, invalid, TLS left to the driver outside dev; warnings in dev); the settings kit declares for each (`<name>-max-open`…, `settingBase.database`, a `check`); the component `database:<name>` after the secrets and before the stores (`startDatabase`: open, pool and transactor, check, `migrateAtStart` — kit's set first, then the modules' and the product's; `start` or `manual` —), a readiness check never a liveness one, `runtime.databases` and `GET /_kit/api/databases` (dev); `Main`'s `migrate [status\|up\|down SET VERSION]` — kit's set first, never down. Nothing a driver says leaves these files: errors are `dbFailure` (kit's words, the SDK's verdict, ≤ 120 runes) |
| `setting.go`, `configcmd.go` | declared settings (`Service.Setting[T]`: text, bool, int, int64, float64, duration, list; `Required`; `Get` reads the app's resolved value, the default before) resolved at the config step (`resolveSettings`): default < `config/config.<ext>` < `config/<env>.<ext>` (embedded, `ConfigFiles`, read through the SDK's `config.FSSource`; only the JSON, YAML and TOML codecs are imported — `codec/json`, `codec/yaml`, `codec/toml` — so no other format's dependency reaches a product) < `<APP>_<NAME>` (read by name, parsed per type) < `kit.Set`; every problem at once, never a value; a secret's name in a file is refused; a stray `<APP>_*` variable is a warning (Kubernetes service links excepted); `runtime.config` gets `service`/`key`/`type`/`detail`; `Main`'s `config` prints it all; a workflow timer may wait a duration setting (`Delay`), which must then be positive (ADR 0003) |
| `loop.go` | declared loops (`Service.Loop`: wakes, one goroutine, app clock), hand-written loops (`Service.Go`: run by the SDK's `lifecycle.Supervisor` on the app's clock and kit's backoff, its events drawn on the loop's state — `Routine.observe`), topic wake hooks |
| `job.go`, `static.go` | scheduled jobs; frontends, served by the SDK's `server/static` — plugged in by `framework/kit/server` (`plug.NewStaticFiles`, `serverkit`) (a single-page application: a route with no extension is the page, a missing file with one is a 404, no directory listed, kit's CSP) — kit draws and observes each request. The Studio's own bundle stays on `introspect.go`'s `assets`, which serves it gzipped: the SDK's handler does not pre-compress yet |
| `app.go`, `config.go`, `main.go` | the components sorted into their groups (`componentNodes`: a command that is not queued is none), started in order, the serving ones last (`addServing`); `With` returns a copy (`appOptions.clone`: what an option changes in place — `kit.Set`'s map, the bindings, the replacements, the mounts — is copied) and resolves the mounts: `a.own` is what `NewApp` lists, `a.services` every service the app runs, the modules' first; lifecycle, closed-by-default config (`envName`: KIT_ENV as written, which names the environment's configuration file), `Main` (serve / graph / healthcheck — the SDK's `health.Ask`, the reason on stderr — / config / secrets / migrate / privacy); `Start` times each of its steps (`step`: config — which opens the secret stores when the app has a secret or a mailer —, declarations, mount, routes, data, handler, components, serving) and `settings`/`secretSettings` say what the config read and from where — `runtime.boot` and `runtime.config`, the Studio's boot sequence; secrets start first, before the stores; kit's listener on the SDK's server engine, plugged in by `framework/kit/server` (`startHTTP`: `plug.NewHTTPServer`, `serverkit` — `server.New` + one `http` group with kit's bounds and ONE socket — `httpShards`: the engine would otherwise shard with SO_REUSEPORT, letting a second process bind a busy port and `:0` open several ports —, bound address from `State`; in dev the engine's goroutines inherit the `http` loop label because `startHTTP` runs `Start` under `pprof.Do`; `stopHTTP` gives `Shutdown` the `httpDrain` deadline, which the engine applies only in `Serve`) |
| `app_profiles.go` | the process profiles (ADR 0147 §5, V-A): `Profile` (`server`, the default; `daemon` — no HTTP, no `/_kit/`, listeners, `IdleStop`; `cli` — the stores and databases only, one command run by `Main`), `IdleStop` (the `idle` component: stop once, for d, no listener connection was open and no `Service.Activity` said busy), `Singleton` (a `pkg/v1/lock` file lock in `ipc.RuntimeDir`, `CodeSingletonHeld`) and `SingletonPer` (the lock named after `ScopeKey`), `profileProblems` (`optionProblems`, `nodeProfileProblem`, `servesHTTP`), `profileState` — next to `App`, which holds it; `Stop(ctx)`, a product ending its own run from any context kit gave it (`profileState.asked`, `Run`'s reason `asked`); `product()`, the runtime directory's name — the binary's for a role of one, so two products never share one (`locks/`, sockets, telemetry). A CLI run (`cliRun`) starts only its stores — never the singleton, so `<binary> daemon status` runs while the daemon holds it (`TestACLICommandDoesNotTakeTheSingleton`) —: no Studio, no git (`describeBuild` asked as production), no data directory unless a store is mounted, no health probe, no HTTP handler, no telemetry, no announcement on its streams — `TestACLIRunStartsOnlyWhatItsCommandReads` |
| `cli.go` | `Service.CLI` (its one-line help kept in `doc`; `CommandLineConfigurer`s: `DefaultCommand` — `Main` runs it when the first argument names no command, and with none, never answering a usage error; two are a problem, `defaultProblems` —, `FailSafe` — its status always 0, a failure, a start that fails and a panic logged, nothing written to its streams), `Stdio`, `CLIFunc`, `runCLI` over `runCommand` (a span `cli`, `CodeCLIFailed` on a non-zero status, `exitSoftware` on a panic), `startFailed`, `reservedCommands`, `usageCommands` — the product's commands listed after `Main`'s usage, the default marked |
| `scope.go`, `scope_user_{other,windows}.go` | `ScopeValue`: a part of a path known at the start — `PerUID` (the user's UID, its SID on Windows), `PerExecutable` (links resolved), `PerConfigDir`, `PerEnv(name, fallback)` (`~/` expanded) —; `ScopeKey`, 16 hex digits of their SHA-256, which a client computes as the daemon did; `CodeScopeUnknown` when one cannot be read; used by `SingletonPer` and `SocketPer`; `currentUser` per GOOS (`scope_user_other.go`: the kernel's UID, no cgo; `scope_user_windows.go`: the token's SID) |
| `activity.go` | `Service.Activity`: a function saying the product is busy (`ActivityHandler`, kept in `Service.activities`); the idle watch (`startIdle`) calls every mounted service's on each tick and restarts the idle time while one says true — a panic is busy, and logged |
| `listener.go`, `listener_interface.go`, `listener_compliance.go` | `Service.Listen`: an inbound port that is not HTTP — a private socket (`pkg/v1/ipc`, ADR 0148; a named pipe on Windows) speaking a versioned contract —, a lifecycle component; each connection a goroutine and a `connect` span; the stop closes the listener, cancels the handlers and closes their connections; `Listener.Dial`, `SocketPath`, `SocketPer` (the socket's name carries `ScopeKey`), `AllowPeers`, `declarationProblem`; `SocketPathIn`/`DialIn`, the path and the dial a client of another process role computes from the declaration and the daemon's app name, and `SocketPathFor(app, service, name, scopes...)`, the same rule for a client that does not import the declaration (D22); a connection's close error is logged (`closeLogged`), `accepter` is what the accept loop needs of the socket, `var _ starter = (*Listener)(nil)` |
| `telemetry.go` | the telemetry port (ADR 0149): `Telemetry(path, gids…)` and `KIT_TELEMETRY`/`KIT_TELEMETRY_GIDS` (an exporter on a private socket, configured at the start only, never in a CLI run), `DesignDigest` for the handshake, `startTelemetry`/`stopTelemetry`, `report` (every span) and `reportPhase` |
| `roles.go` | `NewBinary`, `Binary.Role` (an App per process role, selected by its leading arguments), `Binary.Talks` (the one edge between two roles, a versioned contract — D22), `Binary.Main`; `describeBinary` draws the binary, its roles, the `contracts` edges and what this role `runs` (`roleInfo`); `exitConfig` and `exitUsage` are its exit statuses |
| `app_analysis.go` | `Analyzer`: the static analysis is injected — the framework links no analyzer; the platform's kit tool gives one in dev |
| `observe.go` | spans → hub (a command of the product's CLI, with no Studio, opens no SDK span: no identifier minted — `traces`): counters, per-entry traces (and those through a node: a delivery never roots one), SSE fan-out; pprof labels, span users and payloads; in dev, the product line that made a call (`callSite`: `code.filepath`/`code.lineno`, first frame outside kit, the SDK and the runtime); only a 5xx makes a span an error |
| `graph.go`, `introspect.go`, `introspect_{unix,windows,other}.go` | `App.Graph`, the Studio API (its event stream is the SDK's `server/sse`, plugged in by `framework/kit/studio` (`plug.OpenEventStream`, `studiokit`); its source endpoint reads a file with `readSource` — the name's `Lstat`, then `readSeen`: one open with `sourceOpen`'s flags, the handle's `Stat` the same file (`os.SameFile`), regular and within the bound, read from that handle; `sourceOpen` is `O_NONBLOCK` on Unix, where `os.Root` follows a link whatever the flags, and `FILE_FLAG_OPEN_REPARSE_POINT` on Windows, where it opens the link itself), the injected static analysis (`app_analysis.go`) |
| `daemon.go`, `daemon_http.go`, `daemon_loops.go`, `process.go` | `Runtime`: the signals that end `Run` (SDK `signal`), phase history, the run's config and boot steps, loops (provenance, live state), the scheduler runner, the listener's connection counts — the engine's, `countAccepts` — and its request counters and middleware (`instrumentHTTP`), the process sample (the SDK's `process.Self`, as the model says it) |
| `architecture.go` | `Graph.Architecture`: C4 context and containers, derived from the declarations — which nodes keep data (`keepsData`: a queued command's queue too), and where (`onDisk`) |
| `devtools.go` | the dev tools' read-only routes — the process, databases, goroutines, the heap profile, logs, the mailbox, a field's former values —, `goTracked`; no route acts on the product (D13, ADR 0147 §8) |
| `logs.go`, `redact.go` | the product's logger — the terminal gated at the app's level by the SDK's `logger.LevelGate`, the ring at every level in dev, and on the terminal an encoder kit wraps (`redactingEncoder`: names, URL credentials, a value logged whole as its redacted JSON) — and the hub's log ring; redaction of payloads and log attributes by the SDK's `redact` (`redactor`: its words, the `kit` tag's `secret`, and a field rule — `boundSecret` — for the classes `personal`, `special` and the `subject` option by the comma rule, a cookie or a secret-named header, errors by `describe`) |
| `classify.go`, `classify_plan.go`, `classify_walk.go`, `classify_warn.go` | ADR 0006's grammar — `parseTag` (a class — `public`, `personal`, `special`, `secret` — and the options `subject`, `moderated`, `plain`, `erased`, `history=N`; every mistake a problem), the schema's classes (`classify`), `sealedAtRest` (a sensitive member, or the subject, not plain: what kit seals at rest), `rules.byName` (a document's walk finds a field by its JSON name); the plan per type (`planOf`, cached: `rules` walked over values to any depth, `members` as JSON pointers with `*` for every element, the subject judged); the walks (`clearSensitive`, `cleared`, `stampErased`, `clearPath`, `subjectOf`, `withoutSecrets` — as deep as a document goes, a recursive type's too), the start's check (`classificationProblems`, each type once, at the first declaration that uses it: `typeSource.dataTypes` on stores, endpoints, topics, auth handlers, commands and queries), a store's items redacted by type (`redacted`); the name heuristic (`classify_warn.go`: `looksPersonal` — a name that mentions personal data, on a type that can hold what it mentions, `holdingOf` —, and `secretLooksPersonal` — a field tagged secret that reads so and whose name says no credential, `credentialNames`: the migration from secret to personal —, each warning keyed by the field where it is declared, `declaredAt`, so a promoted field is warned of once per app) |
| `privacy.go`, `privacy_keys.go`, `privacy_erase.go`, `privacy_export.go`, `privacy_info.go` | kit's own service (`kit.Privacy`; a per-app copy, `kit.privacy`, mounted first by `mountPrivacy` when the app keeps personal data or seals what it keeps on disk — its store of data keys and its `data-key` only then, `declareKeys`); the reference keys (`index-key`: `KIT_INDEX_KEY` or `kit-index-key`, made on first use, never rotated; HKDF subkeys, HMAC references) and the store's `kit:subject` index (`kitIndexes`, `subjectKeys`); `Store.Erase` and `kit.Erase` (`eraseRecord`, `deleteRecord` — what they wrote is folded even when the journal entry fails —, `eraseOnRequest` returning the data keys a record was sealed under, `sealedRefs`, `moveOwn` a held record's former values away from its person's key); `kit.Export` (secret members left out at any depth); `privacyInfo` for the model, `privacyProblems` (errors always; the warnings when `explain`: dev, `config`, `privacy`), `privacySettings` |
| `retention.go`, `retention_loop.go`, `retention_run.go` | the store options (`EraseAfter`/`EraseAt`, `DeleteAfter`/`DeleteAt`, `Anonymise`, `HeldUntil`, `Purpose`, `DeleteOnErasure`, `RetentionByProduct` — the product keeps the store's retention: no loop, no warning of a missing retention or subject, no such gap in the register, refused beside kit's own —: one `privacyOption` type, checked for the entity type by `declarePrivacy`'s `privacyDecl`), `KIT_RETENTION`; each store's loop (`retentionRun`: an agenda from the store's writes — `watch` —, a timer on the app's clock), started by the `retention` component after the daemon's loops; its runs (`sweep`, `retainOne` under the store's hold lock, `fold`) |
| `hold.go`, `journal.go` | legal holds in `kit.privacy/store/holds` (`Store.Hold`/`Release`, `kit.HoldSubject`, `holdRefusal`: the `Conflict` `Delete` answers; `lockHolds`: placing a hold, and checking for one before an erasure or a deletion, one at a time per store); the privacy journal in `kit.privacy/store/journal` (`journal` — an entry made in a transaction held until the commit —, `appendJournal`, `chainHash`, `verifyChain`) |
| `seal.go`, `seal_json.go`, `seal_store.go`, `seal_store_engine.go`, `seal_message.go`, `seal_erase.go`, `seal_check.go` | ADR 0006, step 3 — sealed at rest: the `sealer` (the SDK's `SubjectKeys` over kit's store of data keys, `keyStore`, wrapped under `data-key`'s keyring; `seal`/`open` a member's value, `"sealed:v1:"` and the SDK's box in base64url, `"sealed:v1:root:"` under data-key itself, padded to 16 bytes; `boxRef`; `sealLocks`, 64 stripes a write holds shared from its seal to its commit — in a transaction to its end, `heldToTheEnd` — and a destruction takes `alone`, by `TryLock`; kit's store of data keys is apart from every transaction), `a.sealing` made once per run, `dataKeyring` (the command line's, no loop), `dataKeysInUse` and `rewrapDataKeys` (rotation), `sealsAnything` (who gains kit's own service); a document walked as it rests (`objectMembers`, `sealWalk` over the members kit seals, `boxWalk` over every box, `setAt`); a store that seals (`seal_store.go`: how a record is opened and sealed; `seal_store_engine.go`: the port's calls; `sealedEngine`: a `storeEngine[sealedDoc]` inside, whatever the app places the store on — `sealedDoc` is one type for every record type, so the engines under it are compiled once, not once per record type: the test binary's compile stays under the machine's memory cap — `docKey` and the index functions see a record opened, `lenient` reads the key from what is not sealed, `refOf` the data key — its subject's, else its own, its own while a hold keeps it —, `sealKeyed` keeps in clear what the store's key reads and says so once, `Write` seals a replacement under the writers' lock, `dropOwnKey` — at the commit in a transaction —, `refsAt`, `inClear`); messages (`sealedTopic`, `sealedQueued`: a topic's and a queued command's envelopes, bound to a random `ref`, the subject's key, the dispatcher's, else data-key); `shredPeople` (kit.Erase's keys, a `Shred` span, `shred` journal entries, the keys' store folded), `shredIfLast` (a person's last record gone) — both at the commit in a transaction; `sealingProblems` (keys or data-key in memory with a store on disk refused, `KIT_DATA_KEY` not 32 bytes refused, a database keeping the keys beside what they seal warned of — never SQLite, which keeps no keys (`placementOf`), so a store they seal lives beside kit.Privacy there) |
| `history.go`, `history_engine.go`, `history_write.go`, `history_read.go`, `history_view.go` | ADR 0007 §1, a field's former values (`history=N`): `historied`, the engine a store that remembers — or has a password policy — runs on (`withHistory` at start: the history in `<svc>/<store>.history.json`, where the store keeps its data, reconciled with its records); every write goes through the engine's `Update` (`Write`: a Put whose key is new inserts, and updates when another writer inserted it meanwhile; a record the type no longer decodes is overwritten as before), the history written inside it, before the record (`updating`, `record`: only a field that changed; `pushed`: a head equal to the value replaced — a crash's duplicate — goes; `prune`: n per field, none while a hold keeps the record), put back when the engine refuses the record (`undo`); `Delete` removes the history after the record; `Store.Former` (a secret's without its value) and `formerAll` (read past a crash's duplicate); an erasure takes the former values of what it clears (`erasing`, `historyIntent`, `keepsErasable`); an export's former values (`exportFormer`); `historyInfo` for the model; `shownFormer` and `GET /_kit/api/former` for the Studio; `toHistory`/`fromHistory` seal and open a former value (under the data key of the record it was part of, bound to the store, the record, the field and `former`), `sealFormer` (the privacy command) and `moveFormer` (a held record's, at its person's erasure) |
| `passwords.go`, `passwords_use.go`, `passwords_warn.go` | ADR 0007 §2, the password policy: `Store.Passwords` (`//go:noinline`; its field found by calling the function on a zero entity; a secret field, one policy each), `MinLength` (15 by default, 8 at least), `NotReused` (1–100, implies history=n), `NotCommon` (every policy's: the SDK's `password.IsCommon`, `isCommon`, before anything is hashed); `checkPasswords` refuses a write that puts anything but a PHC string into the field — a value left as it was passes; `Set`, `Change` and `Verify` in spans labelled `password`, on the SDK's `password` (`hashPassword`, `verifyPassword`, `needsRehash`: variables the tests replace); `verifyAll`, every former hash verified whichever matched, concurrently, at most `GOMAXPROCS`; a missing key verified against `dummyHash`, made before the first read; a weaker hash stored again at a login, recording nothing; `passwordWarnings`, in dev, the endpoints that set or change a password with no rate limit — found on the runtime's edges and the static analysis's |
| `revisions.go`, `revisions_engine.go`, `revisions_diff.go`, `revisions_restore.go`, `revisions_privacy.go`, `revisions_view.go` | ADR 0007 §3, a record's revisions (`kit.Revisions(n)`, 1–100): the SDK's document store keeps them with each record, in its own write (`versionsOn`: `Versions`, the app's clock, `Held` — `versionsHeld`, kit's holds and the store's `HeldUntil`; `<table>___vs` on a database, one more table of kit's set, `tableMigration`); every engine writes stamped (`stampOf`: `by`, `command` — `withCommand`, set where a command's pipeline begins —, `InPlace` — `withInPlace`); `versionKeeper`, the sibling interface that reads and rewrites them as they rest (docEngine, sqlEngine, and the sealing and history engines, which hand them on); `Revisions`, `Revision` (secrets zeroed, `CodeRevisionDecode`), `Diff` (`jsonpatch.Diff`, a secret's values replaced by `secretTokens` and taken out), `Restore` (the store's own update: `keptOnRestore` — the secrets, the workflows' `states` —, `restorable`, `copyMember`); a sealing store's writes (`keptUpdate`: a record that means what it meant kept as it rests, `sameRest`, or sealed again in place, `sealInPlace`); a transition of the state alone in place (`stateOnly`, workflow_ports.go); a local rollback's write back by nobody, pruning nothing (`undoing`), then `forget`; ADR 0006 — `eraseVersions` (in `clearRecord` and `clearMembers`), `versionsErasable`, `moveVersions` (in `moveOwn`), `sealVersions` (`privacy seal`), `exportVersions` —; the Studio's `GET /_kit/api/revisions` (`revisionsShown`, `shownEdits`), a read — restoring is `revisionscmd.go`'s |
| `revisionscmd.go` | `Main`'s `revisions restore -store ID -key KEY -rev N [-field PATH]…` (ADR 0010, D13: the Studio shows this command, it restores nothing): `withData` as the privacy command does, the product stopped; `restoreVia`, one root span on the store that says who asked (`via: cli`), no edge |
| `records.go`, `register.go` | ADR 0008's port: `kit.Mark`, `App.Fields`, `App.Records` (`Get` without secrets, `EraseFields`, `Delete`, journaled) and `kit.RecordsOf(ctx, store)`, the same for the code the app runs — a module's watch; the register of processing (`register`, `recipientsOf` the graph's reads edges and their mailers) |
| `watch.go`, `watch_run.go` | ADR 0008's watch: `Service.Watch` (a node of kind subscription — `SubscriptionOption`s, a problem for a mark that is none of `marks`, a nil handler), `kit.Written`; `feeders`, the start's one rule shared with the analyzer — the app's stores (`productStores`: kit's own left out) whose type carries the mark (`markedFields`, `App.Fields`' too), but the watch's own module's, unless `kit.OwnStores()` (`ownStores`, a `SubscriptionOption` a topic's subscription refuses) —; `start` opens its queue (`<data>/<svc>/queues/<name>`), puts it on each feeding store (`Store.feed`, which returns what takes exactly it off) and starts its consumer (`BatchSize` 1, woken by `change`); `stop` takes it off, then ends the consumer; `Store.notify`, called by the four funnels once kit confirms a write — `write`, `modify`, `remove` in `store.go`, `transitionWrite` in `workflow_ports.go` —, which skips a watch that hears its own stores when the write is its own handler's (`ownWrite`: the handler's context is marked with its watch, `handling`), and `Watch.notice`, which queues `watchNotice` (store, key, deleted, the write's traceparent) with the caller's cancellation removed, a refusal said as `watch.lost`; `deliver`, the handler told `Written` with the store's marked fields, in a context marked as its own; `describe`: `SubscriptionInfo.Mark`, `OwnStores`, `Stores`, a declared `delivers` edge from each store, dead letters |
| `privacycmd.go`, `privacycmd_print.go`, `privacycmd_seal.go`, `privacyapi.go` | `Main`'s `privacy register\|export\|erase\|holds\|journal\|retention\|seal` (`seal STORE…`: `sealAll`, the records and former values kept in clear sealed, the store folded) (one function per sub-command; `withData` opens the stores itself, after `dataReady` refused data kept in memory, a data directory that does not exist, and a product that answers); the Studio's Privacy page (`GET /_kit/api/privacy`, `POST …/privacy/retention\|export\|erase`, each a control event) |
| `profile.go` | the Studio's heap profile and goroutine view, answered by the profiler `framework/kit/studio` plugs in (`plug.StudioProfiler`, `studiokit`: the SDK's `profiling` captures, decodes, folds and groups; the attribution to nodes is there too); kit keeps the routes, `CodeProfileRead` and `moduleSource`, which says where a function of the product lies |
| `plug/`, `serverkit/`, `studiokit/` | the opt-in subsystems out of kit's import graph: `plug` holds the hooks kit reads (stdlib and model only), `serverkit` and `studiokit` the SDK's server, static, sse and profiling behind them, which `framework/kit/server` and `framework/kit/studio` plug in as they are imported — a product that imports neither links and initialises none of them |
| `errors.go`, `catalog.go` | the wire mapping (`describe`), the wire codes a caller branches on — `Wire*` (`WireNotFound = "not_found"`, the platform's `Code*` values byte-for-byte, renamed because `Code*` names a dotted-quad `errs.Code` in the SDK) —, the framework's codes (`0.4.2.*`), mechanics catalog, `NewID` |
| `words.go`, `locales/{en,fr}.json` | kit's sentences, loaded at the first one said (`printers`, a `sync.OnceValue`: a start that says nothing pays nothing; the check of the catalogues is linear): every diagnostic is a `phrase` — `say(key, name, value, …)` renders the catalogues' key in every language kit speaks (SDK `i18n`), a phrase argument in the same language, `listOf` lists the way each language does; `Message` is the English, `Texts` every language, which the Studio shows in its reader's |
| `build.go`, `ratelimit.go` | what the binary was built from (the SDK's `process.ParseBuild`, read once per process — `readBuild`, `parsedBuild`, `productionBuild`, `kitVersion` —, every app and every graph handed its own copy (`cloneBuild`, `TestEachAppOwnsItsBuild`); in dev, `git.Head` for a local module), kit dev's account of the build; `RateLimitPerClient` on the SDK's keyed limiter |

The framework's own failure codes are `0.4.2.*` — layer 4 is the framework
module, PP 2 this package (ADR 0147 §3) —, numbered in the order the
platform's `0x7F.1.*` codes had, all declared in `errors.go` but
`CodeProfileRead` (`profile.go`), the profiles' (`app_profiles.go`, `cli.go`)
and the scopes' (`scope.go`).

What is a mechanism rather than framework moves to the SDK, wave by wave
(the platform's `docs/adr/0001-the-sdk-holds-the-mechanisms-kit-is-the-framework.md`
has the map): before writing something generic here, look for it there.

## Rules specific to this package

- A diagnostic is a sentence of the catalogues, never a format string:
  `s.problem(at, node, "key", "name", value…)`, `a.problem(node, say(…))`,
  `diagnosticOf(severity, node, source, phrase)`. A new one adds its key to
  `locales/en.json` AND `locales/fr.json` (same placeholders);
  `TestKitSpeaksEveryLanguage` refuses a key the code says and the catalogues
  lack, or the reverse. English stays what the terminal and the tests read.
  Wire errors (`describe`, `Invalid`…) are the product's API and stay English.

- A new constructor that records a position is `//go:noinline` and gets a line
  in `TestSourcesPointAtTheDeclarations`.
- Everything a span, a response or the event stream carries about an error
  comes from `describe(err)`; caller text quoted in a message goes through
  `clip`. One exception, documented: a mail's status in the mailbox carries
  `publicText(err)` — a kit error's message or the SDK's `Public` text, both
  wire-safe by contract — so a developer reads "The mail server could not be
  reached" rather than "internal error". Never `err.Error()`.
- A goroutine or timer started by a run is joined by `Stop` (`a.wg`, the
  `stopping` flag); `TestGraphNoticeDoesNotOutliveItsRun` shows how to prove it.
  Loops, the outbox and consumers are joined by their component's `stop`,
  bounded by the stop context: a product function that ignores its context is
  reported by a failed stop, not waited for forever.
  A goroutine the framework starts on its own — a listener's accept loop, the
  idle watcher — goes through `goTracked`.

## Invariants of kit v2

- **Auth spans carry no edge.** The auth handler runs in a span on its own
  node, `From` = the endpoint it guards, `Edge` empty: the hub records no
  edge for it. Which endpoints ask for a user is drawn by their pipeline
  (`auth` mechanic first) and `EndpointInfo.Auth`. What the handler does — a
  session read — is drawn from the auth node like any code.
- **Auth runs before decoding.** The serve path is: span → response state →
  authenticate → decode → validate → policies → handler. The pipeline lists
  `auth` first; changing the order changes the diagram.
- **An in-process call is not authenticated again.** `Endpoint.Call` carries
  the caller's principal (a context value); an `Auth()` endpoint called
  without one answers 401 before running. `WithUser` is the way in for jobs,
  loops and tests.
- **Credentials never leave the mailer.** `KIT_SMTP_URL` is parsed by the
  SDK's `mail.ParseURL` (`parseSMTPURL`), handed to `mail.NewSMTP`, and never
  stored, printed, logged or put in an error: diagnostics say the clause the
  SDK named, or its public text, never the URL; the graph shows host:port and
  the TLS mode only. `TestMailCredentialsNeverLeak` pins it.
- **A unique index never lies.** The SDK's document store maintains the
  indexes with the entity, in the same write; a refused write leaves both
  untouched; data breaking a unique index refuses to start
  (`CodeStoreIndex`). An index key is never quoted in an error: it is often an
  e-mail or a token hash.
- **Loops wait on the app clock.** Declared loops arm `a.clock` timers only,
  so a test's `clock.ManualClock` drives them. The Studio cannot move the
  app's time: timers are tested with `kit.Clock` and a manual clock. A
  loop's next run is its state's own copy: `loopState.idle` takes the instant
  by value, since the graph and the event stream hand `NextRun` out by
  pointer and a loop writes its own variable again
  (`TestALoopsNextRunIsItsOwnCopy`).
- **A workflow never polls.** The SDK engine's loop runs at the start, at the
  next transition due on its agenda, and after a write of its store that can
  bring one forward (the store's `onWrite` → `changed` → `Machine.Changed`) —
  never sooner than `minDeadlineGap` after a run; an entity whose transition
  failed is retried alone after its backoff (`loopBackoff`). A write that can
  make nothing due — an entity in a state no timer or guard leaves — wakes
  nothing. A guard (`When`) sees the entity only; a condition on the time is
  an instant (`At`). `TestWorkflowTimerAndGuard` pins that an idle workflow
  does not run.
- **One entity, one transition at a time.** Transitions of different entities
  run at once. An OnEnter hook that changes the state it enters or the key is
  `CodeWorkflowHookChange`; one that fires its own workflow is
  `CodeWorkflowReentrant` — refused, never a deadlock. The store's `onWrite`
  and `onDelete` hooks run outside every lock: `Machine.Changed` waits for the
  entity's lock unless a transition of it is in flight.
- **One run at a time, a burst is one run.** Wakes during a run are gathered
  (`loopRun.pending`, a one-token channel) into one run after it; the first
  reason wins. After a failure the next run waits 1s→1m — the SDK's
  `resilience.Backoff` (`loopBackoff`) — a nudge excepted;
  a past deadline waits at least `minDeadlineGap` after a run. The loop
  arms a timer only to sleep on it: a wake already there is taken first,
  and one held for the backoff arms only its end
  (`TestALoopTakesWhatCameBeforeItArmsATimer`).
- **A delivered mail is not sent twice by this process.** The SDK's mail
  spool drops a redelivery of a mail already sent, and every attempt keeps the
  Message-ID `<outbox-id@sender-domain>` it stamps at `Send`. Retries back off
  1s→5m (`mailRetry`) by renewing the lease and letting it lapse; the queue's
  delivery count decides the dead letter. A failure is logged by
  `outboxTransport`, inside the attempt's span; `observe` only keeps the
  mailbox — it runs under the spool's observer lock and must never call
  `Send`.
- **Labels are restored in `end()`.** `begin` and `end` of one span run on one
  goroutine: `begin` sets `kit_node` on it (dev only), `end` puts back the
  labels of the context `begin` was given. `TestLabelsAreRestoredWhenASpanEnds`
  pins it, nested spans included.
- **Dev tools exist in dev only, and they only read.** Payloads, the log
  ring, the heap profile and every `/_kit/api` route beyond the graph,
  events, traces, source, instances and items are mounted or switched on
  only with the Studio (`cfg.studio`) in a serving app.
  `TestDevToolsAreAbsentInProduction` walks every route;
  `TestTheStudioActsOnNothing` walks every former control — mocks, faults,
  `jobs/run`, `workflows/fire`, `loops/wake`, `commands/dispatch`,
  `queries/ask`, `privacy/*`, `profile/cpu` — and wants a 404 in dev (D13).
- **Every Studio route** goes through `api(…)` in `mountIntrospection` —
  behind `hostGuard`. `TestDevToolsRefuseStrangers` walks every route.
- **What the Studio shows is redacted.** A payload or a log attribute goes
  through `redactor` (`redact.go`, the SDK's `redact`); an error in a log attribute is `describe(err)`'s text;
  kit's own log lines (`a.log`) never reach the ring — only `kit.Log`'s
  (`a.productLog()`).
- **A port calls what the start bound.** One rule, the analyzer's too
  (`resolvePorts` on both sides): `kit.Bind`, else the one `Service.Implement`
  among the mounted services, else the port's `kit.Fallback`. The declarations
  step refuses the rest, every problem at once; a replaced port needs no
  binding. `Port.Call` never looks a binding up by type: `wire` stored it at the
  mount step.
- **A replacement is a test's, and replaces code, not mechanics.** The start
  refuses one outside a test binary (`testBinary`); no Studio route sets one.
  It swaps the handler inside the operation's run (`replaced`), so decoding,
  authentication, validation and the policies still run around it.
- **`With` copies.** It never changes the app it is called on: a test's
  options end with the test (`TestWithLeavesItsReceiverUntouched`).
- **Personal data never shows** (ADR 0006). A member classified `personal`,
  `special` or `secret`, or naming the subject, is redacted in payloads,
  spans, the log ring, the data browser (`redacted`, by type) and the
  terminal (`redactingEncoder`); the privacy journal and the holds' listing
  carry references only. `TestPersonalDataIsRedactedEverywhere` pins it.
- **What kit keeps is sealed at rest** (ADR 0006 §4). A personal, special
  or secret member, or the subject, not plain, of a store on disk, a
  former value or a message in a queue on disk is a box; kit opens it where
  it reads, so code, indexes and workflows see plain values, and the Studio
  gets `[sealed]`, never a box nor its plaintext. A box opens only where it
  was sealed — its store, its record's key, its pointer —; a member whose
  data key is destroyed reads as its zero value. A held record rests under
  its own key; a person's erasure destroys their key. A destruction never
  waits as a writer on a stripe a write holds (`alone`): a write's store
  hook may wait on a transition that seals under the same stripe
  (`TestNothingPersonalRestsInClear`, `TestAnErasureReachesEveryCopy`,
  `TestABoxOpensOnlyWhereItLies`, `TestAHeldRecordRestsUnderItsOwnKey`).
- **A retention never polls.** Its loop runs when a record of its agenda is
  due, a write brings one forward, or a nudge; with nothing due it waits for
  a write alone (`TestAnIdleRetentionDoesNotRun`). It arms a timer only to
  sleep on it: a wake already there is taken first
  (`TestARetentionTakesWhatCameBeforeItArmsATimer`). A held record — a hold, or
  `HeldUntil` before its instant — is neither erased nor deleted, and
  `Store.Delete` answers a `Conflict` naming the hold, never its reason.
- **A hold fails closed.** When kit cannot read the holds or the index key,
  whatever would erase or delete a record stops with the error
  (`TestWhatKitCannotReadStopsIt`); a journal or holds it cannot read are
  errors for the command and the Studio, never an empty list.
- **The journal is append-only and chained.** One writer at a time
  (`privacyRun.chain`), each entry's hash over the previous one's and its
  own JSON; `verifyChain` breaks where an entry changed or one is missing.
- **A history is written before its record** (ADR 0007), inside the
  engine's `Update`: a crash between the two leaves the current value at the
  head of the history, which kit reads past, never a former value lost; a
  refused record puts the history back. A value is recorded only when its
  field changes (`TestACrashLeavesADuplicateNeverAGap`,
  `TestAnUnchangedFieldRecordsNothing`).
- **A version is written in its record's write** (ADR 0007 §3): the SDK's
  document store keeps it in the same overlay entry or the same transaction,
  stamped with the caller's context — its user, the command its pipeline
  marked —, never the transaction's. The same record makes no version, a
  store that seals compares it opened (`keptUpdate`), and a transition of
  the state alone writes in place (`stateOnly`). A version never gives a
  secret back: `Revisions` zeroes it, `Diff` says it changed, `Restore` keeps
  the current one and the workflows' states. What a rollback undid never
  shows in the history (`forget`), and an erasure clears the versions as it
  clears the record (`TestARecordKeepsItsRevisions`,
  `TestASecretNeverLeavesAVersion`, `TestARollbackLeavesNoVersion`,
  `TestAnErasureClearsTheVersions`).
- **A password is only ever a hash, and its former hashes stay in kit.** A
  policy's field takes a PHC string or nothing; `Former`, an export and the
  Studio say when it changed, never what it was; a refusal verifies every
  former hash and never says which matched (`TestAPasswordIsNotReused`).
- **A module qualifies by construction.** `NewModule` renames what its
  services declared and they are born qualified after: a node's ID, a
  setting's ID, a problem's node are read lazily, never captured with the
  bare name. A setting's or a secret's key (`key()`) is qualified from its
  service's module at every read. A module's settings are read from its
  section of a file (`moduleSections`), never from a dotted key at the top.
- **A module's services are its Go module's.** `foreignDeclarations`
  refuses a node or a setting whose declaring package (`pos.pkg`) lies in
  another Go module of the build; nothing is judged when the binary carries
  no build information. An exposure is a node, judged as any; an
  operation's builders — `Allow`, `Authorize`, `Key` — record where they
  are called (`builderCall`, through the same `callerPos`), and
  `foreignBuilders` judges them too: a product dispatches a module's
  command from an endpoint of its own, and narrows access there. A store's
  password policies record theirs (`Store.Passwords`), and
  `foreignPolicies` judges them: a product sets, changes and verifies a
  module's passwords through a policy the module declares.
- **One app runs a module at a time**, as its services: `Module.Prefix` is
  set once the services are mounted (`mountModules`) and taken back only by
  the app that set it.
- **An exposure says who may run it** (issue #34). A command or a query
  is internal until it is exposed; its exposure is refused at the start —
  at the `Expose`'s line, in every language — unless the operation declares
  `Allow` or `Authorize`, or the exposure carries one marker that agrees
  with the user the operation asks for: `kit.Anyone()` (never for an
  operation that requires a user), `kit.AnyUser()` (only for one that
  does). A marker beside a rule, and both markers, are refused too. A marker
  adds no authentication (`TestAnExposureSaysWhoMayRunIt`,
  `TestAnExposureOpenOnPurposeSaysSo`).
- **A command is checked alike wherever it comes from.** Its exposure only
  authenticates and decodes: the validation, the key and the authorization
  are the command's, in process and over HTTP. Its authorization runs last
  before its handler, under its key; a queued command is checked at its
  dispatch, as its caller, and its handling runs as the user who dispatched
  it, in the dispatcher's trace. A command is a transaction, opened once its
  key is held, its authorization and its handler in it
  (`TestACommandIsATransaction`); `kit.NoTransaction()` opts it out.
- **A product without commands pays nothing.** A command that is not queued
  is no lifecycle component and starts no goroutine; its locker exists from
  its first keyed run. Endpoints run the shared `pipeline` of
  `operation.go`, not a second one (`TestAProductWithoutCommandsPaysNothing`).
- **A write tells its watches where kit confirms it** (ADR 0008). The four
  funnels — `Store.write`, `modify`, `remove`, `transitionWrite` — call
  `notify` once the engine confirmed the write (`said` nil,
  `WriteUnconfirmed` included), with the write's context, inside its span;
  a write fn refused, or a hold, tells nothing. A notice carries no value,
  and a queue that refuses it leaves the write standing (`watch.lost`). A
  notice of a write made in a transaction is held until the commit, and a
  rollback drops it (`TestAWatchNoticeWaitsForTheCommit`); the watch's queue
  is no database's, so a crash between the commit and the queuing loses it.
- **A transaction writes one database** (ADR 0004). Its first call on a
  database, or its first write on the data directory — which counts as
  one —, makes it that database's; a write elsewhere is `Invalid`,
  `CodeTransactionSpan` (`TestATransactionWritesOneDatabase`). A level is a
  savepoint of the one around it; its end ends its transaction or its
  savepoint, and joins the goroutine parked in the SDK's `Transact`: nothing
  of a level outlives it. What leaves the process is held until the
  outermost commit — released once the writer turn is given back, with a
  context outside the transaction —, and a rollback drops it
  (`TestEffectsWaitForTheCommit`).
- **The writer turn is a write's outermost lock.** A transaction on the data
  directory or in memory holds it alone from its first write; a write of
  none shares it. It is taken before the hold lock, and by `Fire` and
  `Start` before the engine's entity lock; a wait is bounded by `turnWait`,
  never forever (`TestDataDirectoryTransactionsTakeTurns`). A cache
  (`InMemory` on a store) takes none, and is in no transaction.
- **A watch that hears its own stores never hears itself.** With
  `kit.OwnStores()`, a watch is fed by its own module's stores too; its
  handler runs in a context marked with its watch (`handling`), and a funnel
  tells it of no write made in that context (`ownWrite`) — a write the
  handler causes later, elsewhere, is heard
  (`TestAWatchHearsItsOwnStoresNeverItsOwnWrites`).
- **Marks cost nothing without a watch.** A store's `feeds` are nil unless a
  running watch put itself on it; a write loads that pointer and nothing
  more (`TestAStoreFeedsAWatchOnlyWhileItRuns`,
  `TestMarksCostNothingWithoutAWatch`).

## Hooks for the runtime half

| Hook | Where | Contract |
|---|---|---|
| `func (l *Loop) Nudge()` | `loop.go` | a product's own wake — never the Studio's (D13); reason `manual`; skips a failure backoff |
| `mailbox` (`mails(limit)`, `mail(id)`) | `mailer.go` | implemented by `*Mailer`; newest first; SMTP keeps summaries only |
| `a.kitLoop(...)` | `loop.go` | registers a loop entry with provenance and library, filling only what `a.loop` left empty |

## Error codes (`0.4.2.*`)

| Serials | Area |
|---|---|
| `1`–`5` | store (`5`: a unique index broken at load) |
| `6`–`7` | topic |
| `8`–`11` | queue, subscription (`10`: a handler's panic — a listener's too) |
| `12`–`15` | workflow |
| `16`–`18` | app |
| `19`–`23` | mailer |
| `24`–`26` | loops |
| `27`–`30` | secrets |
| `31`–`34` | databases |
| `35`–`38` | privacy |
| `39`–`42` | history |
| `43`–`46` | commands and queries |
| `47` | watches |
| `48` | profiles (`CodeProfileRead`, in `profile.go`) |
| `49`–`57` | the framework's own signals and refusals, typed at the move (`JOB_OVERLAPPED`, `HISTORY_MOVED`, `NOT_SOURCE`, `PASSWORD_MOVED`, `NOTHING_TO_ERASE`, `KEY_REKEYED`, `MIGRATE_REFUSED`, `CATALOGUE_INCOMPLETE`, `RETENTION_PANICKED`) |
| `58`–`59` | process profiles (`CodeSingletonHeld` in `app_profiles.go`, `CodeCLIFailed` in `cli.go`) |
| `60` | scopes (`CodeScopeUnknown` in `scope.go`) |
| `61`–`65` | sealing at rest (`CodeSealKey`, `CodeSealWrite`, `CodeSealOpen`, `CodeSealRewrap`, `CodeSealShred`) |
| `66` | transactions (`CodeTransactionSpan`: a transaction wrote a store of a second database) |
| `67`–`69` | revisions (`CodeRevisionRead`, `CodeRevisionWrite`: an erasure's, a hold's or a rollback's rewrite of the versions, `CodeRevisionDecode`: a version the type no longer decodes) |
| `70`–`75` | the signals kit ends one of its own steps with and catches itself, typed when `make guard` made rule 2 a gate — no caller receives one: `SEAL_ERASED` (`errErased`), `SEAL_KEY_MOVED` (`errNotCurrent`), `RESEAL_IN_PLACE` (`errReseal`), `RESEAL_MOVED` (`errRestMoved`), `WORKFLOW_NOT_IN_PLACE` (`errNotInPlace`), and `CodeTransactionPanic` with two reasons, `TRANSACTION_PANICKED` (`errTransactionPanicked`) and `TRANSACTION_HELD_PANICKED` (`errHeldPanicked`, `transact_sql.go`). `TestKitSignalsAreTyped` (`signals_internal_test.go`) pins each code and reason: `errs.New` answers a malformed declaration with a validation error, not a panic |

## Test

`go test -race ./kit/` — the product under test is `product_test.go` (a shop,
an audit service, and `members`: indexed accounts and sessions, the `session`
auth handler, a mailer, a generated secret, a declared loop and a
hand-written one; the shop's `quote` port falls back to its list price, and
the members implement it); `port_test.go` binds and refuses ports, `implement_test.go` implements them,
`replace_test.go` replaces endpoints and ports (`replace_internal_test.go`:
the refusal outside a test);
`start`/`startManual` run it in memory, on a free port, in dev, optionally on
a manual clock, with mail always captured (`KIT_SMTP_URL` forced empty). A
test moves a manual clock once the loop it drives has armed its timer —
`armed` counts the app's waits: moved before, the clock puts the timer past
the instant it moves to. A wake the digest loop must hold for its backoff
comes during a run held at its gate, so that the loop arms one timer
(`loop_internal_test.go`: what a declared loop's wait arms); its entry is
read once it counts the run `waitWakes` saw start (`digestRan`).
`mailer_test.go` brings an in-test SMTP relay and a refused port.
`module_test.go` declares the `reviews` module — two services, one named
like it, two stores, a setting, a generated secret, a port with a fallback
and a watch of `kit.Moderated` —, which requires `ratings`: qualified names,
and what the graph says of the modules; `watch_test.go` runs its watch over
the shop's moderated item names and the ratings' notes — every funnel heard
once, a delete, nothing from its own module's reviews nor from an unmarked
store, a failed notice retried, the delivery in the write's trace —,
`watch_graph_test.go` says what the graph shows of it — its feeds, a
product's watch, a product with none, the refusals —,
`watch_internal_test.go` a store's watches while one runs and none without,
and `watch_own_test.go` the `atelier`, whose watch hears its own sketches
(`kit.OwnStores`) and never its own stamps, a module's that hears its own,
and the refusal on a topic's subscription;
`module_mount_test.go` mounts it beside the shop: routes under
a prefix and at `/`, a file's section, the variable, `kit.Set`, the
secret's name in its store, the start and stop order, a mount's binding and
every refusal; `module_internal_test.go` the refusal of a foreign
declaration and of a product's password policy on a module's store — the
module's own taken —, and a module's source and a library's served from
their own Go module (a declaration that says it is `github.com/kitsunium/sdk/pkg`'s);
`introspect_internal_test.go` the source endpoint's read — a regular file
within the bound, never a link at its name, nor one swapped in between the
look and the open —, and `introspect_unix_internal_test.go` a named pipe
swapped in, refused at once;
`module_database_test.go` a module on the fake
database — `kit.Keeps(module)`, its migrations under `<module>_migrations`,
and the refusals.
`secret_test.go` runs apps named `vault` (variables `VAULT_<NAME>`) over a
provided and a generated secret: env and `_FILE`, rotation on a manual clock
(wait for the loop's `nextRun`, then its timer, before moving the clock),
the next run a model showed left as it was by the next rotation,
pinning, file stores across starts, the default beside the data;
`secretcmd_internal_test.go` the
`secrets` command.
`database_test.go` runs databases on `fakedb_internal_test.go`'s `FakeDB` (a
`database/sql` driver and an engine with no network: the SDK checker's ping,
the migrator's statements, the product's statements recorded, and the SDK's
document tables — transactions serialised, each on a copy of the data,
savepoints copies of that copy; its failures quote the URL on purpose); the
engine modules run the same against PostgreSQL, MySQL and SQLite.
`transact_test.go` runs `kit.Transact` on memory, files and the fake
database (`eachBackend`, a `till` declared afresh for each): commit,
rollback, a savepoint, a panic, a command's transaction and
`NoTransaction`, the held publish and mail, a mistake refused at the call,
one database, the writer turn, the span; `store_sql_test.go` a store on the
fake database — its index keys hashed, its history, a workflow's journal
there —, a workflow's hooks and a watch's notice after the commit;
`storetest_test.go` the conformance suite, `kit/storetest` — revisions
included —, on the three (the fake database keeps the versions' table);
`transact_warn_internal_test.go` the two-databases warning.
`privacy_test.go` declares `vigie`, a desk of reports and people's customers
and invoices (classified fields, a retention, `HeldUntil`,
`DeleteOnErasure`); `privacy_rights_test.go` runs references, export,
erasure, holds and what an erasure leaves in the files,
`privacy_retention_test.go` the retention on a manual clock
(`retention_internal_test.go` its wait), `retention_product_test.go`
the `lounge`, whose product keeps its stores' retention,
`privacy_view_test.go` the model, the modules' port, redaction everywhere,
the start's problems and the Privacy page, `privacy_app_test.go` where kit's
own stores are placed and an app's copy; `classify_internal_test.go` the
grammar and the walks, `classify_warn_internal_test.go` the name heuristic
— per type, what it warns of and what it does not —, a promoted field
warned of once and two that print alike apart, a secret that reads like
personal data, `privacy_internal_test.go`
the terminal, secrets at any depth and the chain,
`privacycmd_internal_test.go` the command and what kit cannot read.
`history_test.go` declares `keeper` (holders: a subject that keeps three
former values, a public nickname ten, a password under a policy);
`history_rights_test.go` runs holds, erasure, export, the Studio's
`former` route and the model on it, and the policy once on the SDK's own
hashing (seconds under `-race`); `passwords_internal_test.go` runs the
policy on a fast hashing that counts (`useFakeHashing`), and
`passwords_writes_internal_test.go`, `passwords_warn_internal_test.go` what
goes into its field, a bad declaration and the rate-limit warning.
`revisions_test.go` declares the `press` — pages that keep three versions,
a workflow that publishes them, a command that retitles them, a secret
token — and runs it on memory and files: versions kept, pruned, stamped;
the same record and a transition of the state making none; secrets;
`Diff`; `Restore`; a unique index; a rollback; the declaration's bounds and
files that keep versions; `revisions_privacy_test.go` the `folk`'s members
and handles — sealed at rest, a person's erasure and the retention's, a
hold, an export, a field's history beside the versions —,
`revisions_view_test.go` the Studio's routes and the model, and
`revisions_internal_test.go` `privacy seal` over versions kept in clear.
`counter_product_test.go` is the product of the commands' and queries' tests:
the `staff`'s badge (auth data that is a `kit.Principal`), the `counter`'s
orders — `place-order` allowed by a policy and exposed, `cancel-order`
authorized by a rule and keyed, `reindex` queued, the `my-orders` and
`order` queries, the `summaries` read model its projection keeps — and the `lab`, whose handlers a test holds, fails or
counts (`labGate`); `command_test.go`, `key_test.go`, `expose_test.go`,
`queued_test.go` (a manual clock drives the retries) and
`command_graph_test.go` run it — `expose_test.go` also declares the
`doors`, an exposure of every kind that says who may run it, and the
`walls`, every one the start refuses —; `command_module_test.go` mounts a module's
ledger — a command it exposes, a query — under a prefix, and
`command_module_internal_test.go` refuses a product's exposure and builders
on a module's operations.
`profiles_scope_test.go` runs the default and fail-safe commands, the scopes'
keys, a singleton and a socket per scope, and a daemon waiting for its
activities, a role's socket under its binary's name, a handler that stops its app, a daemon with no store unwarned of memory; `startup_bench_test.go` measures a warm `Main` of a default
command (p50/p99, `BENCH.md`: about 0.17 ms, p50 0.14 ms, p99 around 1 ms under load).

`seal_test.go` declares the `clinic` — patients with every kind of member,
accounts keyed by their subject, memos about nobody, letters and notices on
topics (one subscription refusing every letter, for a dead letter), a queued
reminder — on disk: nothing rests in clear, everything reads back after a
restart, an erasure reaches a copy of the files taken before it, the Studio
shows `[sealed]`, the model says what is sealed, data-key's rotation
re-wraps; `seal_internal_test.go` the document walk, the padding, a box
moved, a key that reads a sealed member, a record's own key, a plain member
and `privacy seal`, the password policy on sealed hashes, a dead letter of
a person erased, the placement checks, a held record's key, a person's last
record, a field that loses its class; `seal_bench_internal_test.go`
(`BenchmarkSealing`) what sealing adds to a write and a read.
`bench_test.go` adds the dev
tools' bench (secrets in and out, logs, a CPU burner, a memory hoarder, a gate
to wait on, a declared and a hand-written loop); `startBench` runs it beside
the shop, and `subscribe`/`awaitEvent` read the live stream.

## Learned conventions

- **A generic engine is compiled once per record shape.** Every record type
  a store is declared over instantiates the whole generic chain beneath it —
  `StoreService`, `docEngine`, `sqlEngine`, the SDK's `docstore.Store` and
  `SQLStore`, `historied`, `sealedEngine` —, and the test binary declares
  dozens: the kit_test compile reached 5 GB when a sealing store's documents
  were `sealedDoc[T]`. A type the engines see beneath a store stays
  non-generic where it can (`sealedDoc`, `openedDoc`, `openedAs[T]`).

- **An in-package test says so in its name**: a test file that reaches kit's
  unexported names is `package kit` and ends in `_internal_test.go`; every
  other one is `package kit_test`, which sees kit as a product does (41 and
  65 files).
