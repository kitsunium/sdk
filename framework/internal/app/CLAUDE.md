<!-- updated: 2026-10-18T00:00:00Z -->
# framework/internal/app — the implementation of framework/app

Declarations (`NewService`, the generic methods of `*Service`), the runtime
(`App`), and the product's self-description (`App.Graph`, the read-only
`/_app/api` surface). Moved from `kitsunium/platform/kit` into the SDK's
framework (ADR 0147), a part of the SDK module since ADR 0162, and named
`framework/internal/app` since ADR 0180 step 3 (`framework/internal/kit`
before it, its engines `<name>kit`): the platform keeps a deprecated facade of aliases
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

A product imports `framework/app`, never this package (Go refuses an
`internal/` import from outside the module). `framework/app` holds one file
per file of this package that exports something: type aliases, typed
constants and variables, and forwarders marked `//:` (and `//go:noinline`
where the implementation is). The SDK's convention (`pkg/v1/*` over
`internal/`) applies: here, a type's name says its role; the facade keeps the
name a product writes.

| Here | framework/app |
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
as it is imported (`RegisterConfigFormat`, `EnableServer`, `EnableStudio`, `EnableStores`)
are this package's only: the facade does not export them. `framework/app/facade_sync_external_test.go` fails when a
name is added to one side and not the other — its two tables are the list
above; `facade_external_test.go` proves the positions a declaration records
through the facade (`callerFrame` skips the facade's frames:
`facadePackage` in `node.go`). A facade signature never names a generic alias
of the facade — `Bind` takes `*iapp.PortService[Req, Resp]`, not
`*Port[Req, Resp]` —: an instance of a generic alias read from export data
races in go/types (golang/go#79035) for every tool that type-checks a
product's packages at once; `facade_generics_external_test.go` pins it. A
facade doc link to a member is `[Type].Member` (ADR 0138).

## Files

| File | Holds |
|---|---|
| `doc.go` | this package's short documentation; the long one — a product's building blocks, the diagram, the app's databases, modules, and what `Main` understands — is the facade's (`framework/app/doc.go`) |
| `app_compliance.go`, `*_interface.go` | the interface assertions (`var _ I = (*T)(nil)`) and the package's interfaces, each beside the file that uses it (`database_interface.go`, `expose_interface.go`, `node_interface.go`, `setting_interface.go`, `introspect_interface.go`, `mailer_interface.go`, `listener_interface.go`) |
| `service.go`, `node.go` | registration, node IDs, declaration positions (`callerPos`, `funcInfo`; a declaration records only its call's return addresses — `site`, resolved to file, line and package at the first read, `pos.file()`…, the facade's frames skipped then —, so a start that describes nothing pays one `runtime.Callers` per declaration; a node's body — where its function starts — is `bodyOf`, the function's code address resolved through `runtime.FuncForPC` at the first read, not at the declaration (#276, `TestABodyIsResolvedWhenRead`; an endpoint's still reads `funcInfo` at once, which names it); `pos.pkg`, the declaring package — `packageOf` reads it from the runtime's function name), the node-in-context marker |
| `module.go`, `mount.go`, `module_check.go`, `gomodule.go`, `module_database.go` | modules (ADR 0008): `NewModule` adopts its services (`adopt`, `rename`: qualified IDs, settings, problems), `Requires`, `qualifiedKey`/`flatKey`/`variableOf` (a module's `<module>.<name>`, `<module>-<name>` in a store, `<APP>_<MODULE>_<NAME>`); `Mount`, `Prefix`, `Only` (#274 Q3, ADR 0178's decisions of 15 October: a mount that holds only in the environments it names — `mountEnv`, <PRODUCT>_ENV or `app.Env` lower-cased as `resolveEnv` names it; `mountsIn` drops the others before `mounts`, a dropped mount's `Bind` is left out of `portRule` by `heldBinds` (`bindDecl.limited`/`only`); `With` resolves for the environment it reads, `resolve` again through `remount` when the configuration names another, kit's own services kept first; `onlyProblems` refuses an `Only` naming none, a name outside `envNameGrammar`, and a module a dropped mount leaves out that a mounted module requires — `requiredOutside`, start diagnostics, no error code; the graph's `Module.Only` is `onlyOf` — the environments every mount of the module names, sorted, nil when one holds everywhere —, given only to a module the app mounts itself, as `Module.Mount`), a `*Module` as an `AppOption` — `mounts` resolves them into the app's modules and services, in start order (`orderModules`: the required ones first, needs before those who need them), `prefixOf` serves a module's routes under its prefix, `mountModules` sets `Module.Prefix` while the app runs; `moduleProblems`, every refusal with both declarations named (`foreignBuilders`: an operation's builders called from outside the module's Go module; `foreignPolicies`: a password policy put on a module's store from outside it); `goModuleOf` (the build's Go module of a package), `goModuleSource` (a position outside the product's Go module — a module's, a library's — relative to its Go module's root, which `moduleRoots` keeps for the source endpoint), `moduleBuild` in `build.go`; a module on the product's databases (`module_database.go`): `(*Module).keep`, `migrations` — `app.Migrations`' value, both a `DatabaseOption` and a `ModulePart` —, `migrationSets(d)` (the modules' sets a database keeps, under `<module>_migrations`, then the product's), `moduleKeepers`, `moduleMigrationProblems` |
| `operation.go` | `Operation[Req, Resp]`: what `Fallback`, `Bind` and `Replace` take — an endpoint, a port (and, ADR 0005, a command or a query); unexported methods (`operation`, `perform` — its in-process run —, `edgeKind`), so only kit implements it. The `pipeline` every operation runs, whoever calls it: the validate tags, the policies around the steps of its kind (`steps`) and the handler or a test's replacement (`call`), a panic answered as an internal error (`panicked`); `inProcess`, an in-process run in its span — its `callSpec` names the app, the node, the edge and the span —, `admitted` requiring the caller's user when the operation asks for one; a request's run is one `outcome` (`endpoint.go`) |
| `command.go`, `query.go` | commands and queries (ADR 0005), internal until exposed — how one service reaches another: `Service.Command`, `Service.Query`, `Dispatch`, `Ask` (`inProcess`: a span of op `dispatch` or `ask`, its payloads in dev), the builders `Allow`, `Authorize`, `Key`, `Expose` (its `ExposeOption`s), `app.Queued`; a command's `steps` inside its policies — its key, then its transaction (`transacted`, unless `app.NoTransaction()`), and in it its authorization and its handler —; `drawn`, the pipeline in the order a dispatch runs it (`transactionMechanic`); no component for a command that is not queued |
| `access.go` | `app.Principal`; `Allow` — a permission as data, the SDK's `authz.Check` over the auth data's attributes — and `Authorize` — a rule that needs the data —, both last before the handler; `refusal`: the one 403 whatever the cause, a NotFound (a rule hiding what exists) and an Unauthenticated kept, a fault logged |
| `keys.go` | a command's key: the SDK's memory lock, one locker per command and app (`lockerOf`, on the app's clock), a lease renewed while its run lasts (`lock.Keepalive`), the keys a run holds carried in its context — a run taking its own key again is `CodeCommandReentrant`, never a deadlock |
| `queued.go` | queued commands: the queue (the table `kit_queue__<service>__<name>__command` on the database that keeps the command's service — ADR 0178 C1 —, else `<data>/<service>/commands/<name>`, memory without a data directory; its step in the pipeline says where it waits — `queueWhere`, `queuePlace`: `sql`, `file`, `memory` — and the attempts), the envelope (trace, user, the dispatching node, key, input — decoded as encoding/json decodes), the dispatch's checks as its caller (`accept`: validate, authorize, key — a key waiting or running is deduplicated), the put — held until the commit inside a transaction (`joined` in it when its queue is on the transaction's database), its key given back by a rollback —, the consumer (one dispatch per worker: `BatchSize` 1), the handling (a `handle` span in the dispatcher's trace, no edge; the Studio's fault, then the policies around the key, the transaction and the handler), the dead letters on the node |
| `expose.go` | `Expose`: an endpoint over an operation — named after it (`app.Name` for a second), its authentication the operation's, the rest of the pipeline the operation's own, 202 for a queued command, a declared `dispatches` or `asks` edge (`EndpointInfo.Exposes`); `ExposeConfigurer` (every `EndpointConfigurer`, and the markers `app.Anyone`, `app.AnyUser` — `exposureOption`, which an endpoint does not take —, `EndpointInfo.Access`); `check` and `accessProblem`, at the start once every builder ran: who may run the operation — its `Allow` or `Authorize` (`guarded`), else one marker that agrees with its `authMode` —, both markers refused where they are given |
| `port.go` | ports (`Service.Port`, `Port.Call`: a span on the port, a test's replacement, then the bound operation's own `perform`), `Fallback`, `Bind`; `resolvePorts` — the start's one rule, shared with the analyzer: `Bind` (the last of a port wins), else the one `Service.Implement` among the mounted services, else the fallback; every problem at once; `wire` keeps the decision for the run (`App.wired`), `unmount` forgets it; `describe` draws the binding as a declared edge |
| `replace.go` | `app.Replace`: `replacements` (refused outside a test binary — `testBinary`, `testing.Testing`: what the linker set when go test built the binary, which no run-time state of a product can turn on — a flag named test.v, a call to testing.Init —, `TestATestBinaryIsWhatTheLinkerSays` —, nil, or of a service not mounted), `replaced` (the handler an operation's run swaps in: counted, marked `mock: replace`, a panic answered as a handler's), `mockList` (the Studio's mocks in dev and the replacements) |
| `endpoint.go`, `decode.go`, `schema.go` | typed endpoints — for what HTTP itself is about, a webhook —, and `Service.Implement` — the one endpoint with no route (`routeless`, `model.ExposePrivate`), marked with the port it implements, named by its `app.Name`, else its handler's name, else its port's; strict request binding (path, query, header, cookie), `model.Schema` from the SDK's `codec/jsonshape` (what encoding/json writes: members, omission, null — a member with omitempty/omitzero is never null; a request's path/query/header/cookie fields named by their tag, `json:"-"` ones found with `reflect.VisibleFields`); a JSON body is read by the SDK's `codec/strictjson/httpbody`, its refusals matched against `codec/strictjson`'s sentinels (`readJSON`, which words its refusals the way kit always did — never the body, a JSON Pointer at most) |
| `auth.go` | the auth handler node, `Auth`/`AuthOptional` (an `OperationOption`: an endpoint's, a command's, a query's), the principal in ctx (`UserID`, `AuthData`, `WithUser`), app-level auth checks — one handler whenever an endpoint, a command or a query asks for a user; the warning that every `Allow` refuses when the auth data is no `app.Principal` |
| `request.go` | per-request helpers: `SetCookie`/`ClearCookie` (hardened), `ClientIP`, `UserAgent`, `Log`; the app's clock for product code — `Now`, and `NewTicker`/`After` for loops written by hand, so a test's manual clock drives them; `responseState` |
| `store.go`, `store_core.go`, `store_document.go`, `index.go`, `store_engine.go` | kit's store — a store that remembers runs on `historied` (history.go) — — a port (ADR 0004); `app.ReadModel()` marks it derived from others (ADR 0005: `StoreInfo.ReadModel`, nothing else changes): `Store[T]` is the typed facade over `storeCore` (store_core.go, ADR 0179 §2: the store without its entity type — `entityType` wraps the product's key once, `entityOf` —, compiled once; the facade encodes the entity, calls the core, decodes what comes back: `as`, `asAll`, `changing`), which holds a `plug.StoreEngine` over documents (`plug.DocumentValue`: the JSON as it rests and the entity decoded once beside it), opened at start through the store subsystem `framework/app/store` plugs in (ADR 0179 step 3, store_engine.go: `engineConfig`, a store's key, decode and indexes as functions of a document, `storeConfig` the `plug.StoreConfig` an engine opens with — its versions, its `Held(ctx, key)`, its stamp `stampOf`, `frameworkTransaction`, kit's transaction as an engine takes part in it (`plug.Transaction`: `inLocal`, `recordUndo`, `hold`, `retell`) —, `openLocalEngine` and `openEngine`, refused with `CodeSubsystemMissing` when nothing set `plug.Stores`, `storesMissing`) as the app places the store — the SDK's document store in memory or under the data directory (`framework/internal/app/storeengine`'s `fileEngine`: one snapshot `<service>/<name>.json` plus one small file per write in `<name>.json.d/`, folded back; a write costs one entity; in a transaction it records what a write replaced and holds its hooks until the commit), or over SQL on a database (store_sql.go, storeengine's `sqlEngine`) —; every call takes the caller's context; an engine's refusals are kit's kinds (`plug.Refusal`, `plug.Refused`), which `said` words for every engine — on a database `sqlStoreRefusal` too —, and a refusal kit's core makes itself is one too (store_document.go: `undecodable`, `unencodable`, `frameworkRefusal`, `errVersionsNotKept` — static data); the core imports neither the SDK's `docstore` nor its `sql` (`framework/app`'s initgraph test), made a node, observed, its refusals said in kit's words (`said`: NotFound, Conflict, Invalid, Unavailable, kit's failure codes; a key quoted only where kit always quoted one; `WriteUnconfirmed` is a warning, the write stands); write modes (`upsert`, `insertOnly`, `replaceOnly`); four funnels every write goes through — `write`, `modify`, `remove`, and a workflow's `transitionWrite` —, which take the data's writer turn for a store in files or memory (`turn`; none for a store `apart` — a cache, `InMemory` on the store, and kit's store of data keys (`dataKeysRole`) —, which `outside` keeps out of every transaction and `placementOf` never puts on SQLite) and tell the store's watches once kit confirms a write (`notify`, `feeds`: `watch.go`; held until the commit in a transaction) — a store no watch hears pays one atomic load; indexes declared and checked by kit (`Unique`, `Index`), kept by the SDK (`Lookup`, `Find`, `Filter`); `onDelete` and `onWrite` hooks, kit's lists, called by the store's announcements outside every lock; `plug.Folder`, a sibling interface the file engine implements, folds a store's files after an erasure (ADR 0006); `Store.open` in the data directory and `openSQL` on a database put a store whose entity has a member kit seals — or any product store at rest of an app that keeps data keys: `opensSealed` — behind the sealing engine (`seal_store.go`), `sealsAtRest` says when; `started`, kit's own store of keys finishing a re-wrap at start, wherever it lives; the store's privacy hooks — its `privacy` options (`declarePrivacy`), `removeUnlessHeld` in `Delete`, the subject index as one more spec at start (`frameworkIndexes`), `privacyInfo` in the description, `redacted` for the data browser; its revisions (`app.Revisions`, `declareRevisions`, revisions*.go) — files that keep versions a store no longer declares refuse the start, `STORE_VERSIONS` |
| `store_sql.go`, `store_sql_migrate.go` | stores on a database (ADR 0004, step 2): the store subsystem's engine over SQL (storeengine's `sqlEngine`, ADR 0179 step 3), every call on the context `sqlCall` gives it — `callContext` (transact_sql.go): the level's transaction on its database, else the pool, within `<name>-timeout`; a read of kit's own store always on the pool —, a write's hooks told again when kit's transaction rolls back (`plug.Transaction.Retell`); `startSQL` and `openSQL` (`sqlTable`, `openSQLEngine` of documents as an `engineConfig` keys them — a store's, a sealing store's records as they rest, its sealed members strings kept as written, a history's, a workflow's journal —; the index keys hashed — `referenceKeys.indexKey`, a third HKDF subkey —, the rows filed again when their fingerprint changed, `reindexOf` (`plug.Reindexer`), a sealing store's refused while a record does not open; `sqlOpenRefusal`); `storeCore.onDatabase`, the database a running store is on (`running` sets it), which its writes take no writer turn on and its history writes in one transaction; `databaseOf`, `storesOnDatabase`; the tables (`tableName` is `model.TableName`: `<service>__<store>`, `__history`, `<service>__<workflow>__workflow`; `frameworkTables`, `tableProblems`); kit's own set (`setFramework`, `app_migrations`): each table's migration the store subsystem's (`tableMigration`, `plug.StoreDriverConfig.TableMigration`: the SDK's document store's tables, a store's versions') or, for a consumer's queue's, the queue subsystem's (`plug.QueueDriverConfig.TableMigration`, ADR 0179 step 4), its registry `app_tables` (`tableRecord`: a table's version and its index fingerprint, a store engine of its own on the database, `registryConfig`), `resolveFrameworkSet` — a new table at the next generation (`gen<<32 \| nameDigest(name)`, FNV-32a) —, `applyFrameworkSet` (retried past an out-of-order refusal, `plug.RefusalOutOfOrder`), `planFrameworkSet` |
| `transact.go`, `transact_sql.go`, `transact_turn.go`, `transact_warn.go` | `app.Transact` (ADR 0004): a transaction (`txn`) and its levels (`unit`, in the context: `unitOf`, `withoutUnit`), `beginTransaction`/`finish` (a span of op `transaction`: database, backend, outcome, effects, savepoint), the one database (`joinCall`, `claimLocal`, `spanRefusal` — `CodeTransactionSpan`), the effects held until the commit (`hold`, `heldEffect`, `let`; `inside` for a hook that stays in the process; the two-phase release of ADR 0178 C1 — `join`, in `unit.end` before the outermost scope's commit, publishes each effect whose `queue` is the transaction's database with its `joined` function on the scope's context, within `<name>-timeout`, and the first refusal is the transaction's outcome: rolled back, and `Transact` returns it; `sent` keeps `let` from releasing it again; every other effect is released after the commit as before, a refusal then logged and the commit standing; each released with `releasing`'s context — the values of where it was made, the life of the outermost level that commits it, never the life of a keyed run that ended before the commit, issue #275), what runs when it ends whatever its outcome, before them (`atEnd`: the data keys' stripes), the rollback (`undoStep`s newest first, the effects dropped, `retell`); on a database each level's transaction or savepoint parked on a goroutine inside the SDK's `Transact` (`sqlScope`, `openScope`, `close`); the data's writer turn (`writerTurn`: a transaction alone, the writes of none shared, `turnWait` 10s; `sharesTurn`; `undoneCounts`, the writes rollbacks wrote back per store, which `loopReads` compares); in dev, the warning of a command or a `app.Transact` block whose code writes two databases (`transactionWarnings`) |
| `topic.go` | fan-out over one SDK queue per subscription — a publish in a transaction encoded at the call and held until the commit, one held effect per subscription tagged with the database its queue lives on (`send`, `SubscriptionWorker.queueDB`), then the loops' wake as an `inside` effect —; `MaxDeliveries` and `Parallelism` are `DeliveryOption`s — a subscription's, a queued command's; trace context in the envelope; a publish wakes declared loops; consumers are woken by the SDK queue itself (ADR 0104 there), so `pollInterval` (5 s) only bounds what another process publishes |
| `consumer.go` | a queue's consumer, whoever's: `openConsumerQueue` — a subscription's, a watch's, a queued command's queue: on the open database that keeps the consumer's service (`queueDatabase`, database_place.go, by a store's rules — service, module, default database; never without a data directory or in memory), the SDK's SQL broker (`openSQLQueue`, which the mailer's outbox shares; `sqlQueueConfig` places it on the store subsystem's `plug.Database`) over `queueTable` — `app_queue__<service>__<name>`, a command's ending `__command`, a mailer's outbox `__outbox` — whose migration, the queue subsystem's (`plug.QueueDriverConfig.TableMigration`), is in kit's set (`frameworkTable.queue`, store_sql.go); else `openQueue`, a file or a memory queue —, every one a `plug.Broker` the queue subsystem opens (`openQueueWith`, queue_plug.go) —, `consumerPolicy` (its lease, retry delay doubling up to `loopBackoffMax`, attempts), `warnFileQueueLeft` (`consumer.file-queue-left`: a file queue a consumer moved to its database left holding a message — `fileQueueHolds`, a `.msg` or `.dead` file in the file broker's `ready/`, `inflight/` or `dead/`, which it keeps once empty —, which nothing consumes now — the ADR's `data copy` moves it) — `consumer.supervise`, a consumer run under the SDK's supervisor so a failed queue comes back after kit's backoff (ADR 0112), and `consumer.deliver`, the one delivery a subscription and a watch run: the trace restored, a span on the consumer from where the message came over a delivers edge, the loop told, a panic the handler's failure; `pollInterval` |
| `workflow.go`, `workflow_ports.go`, `workflow_reads.go` (`loopReads`), `workflow_records.go` (the SQL journal's records over the store engine: `instanceConfig`, `instancesOf`, `instanceDoc`), `workflow_plug.go` | state machine declared by kit — events (`On`), timers (`After`, `At`), guards (`When`), hooks, `stateField`, `stateType` — and run by the SDK's engine (`statemachine`) through the workflow subsystem (ADR 0179 step 5: `plug.Workflows`, which `framework/app/workflow` sets; `workflow_plug.go` — `workflowPackage`, `workflowsMissing`, `workflowsLinked`, `workflowFailed`, the engine's failures told by `plug.WorkflowFailure` through the driver's `Failed`): the engine is instantiated once, over `plug.WorkflowEntityValue` and the state's spelling (Q3) — `spell` (`fmt.Sprint`, remembered in `spelled` so a spelling reads back as the facade's state, `stateNamed`), `entityValue` and `entityOf` (the entity's state field set from the spelling the engine set), `spec` (a transition, its guard or instant over the facade's entity), `enterHook`; `check` refuses two declared states spelled alike, `workflow.state-spelled-twice` —; `config` (kit's own OnTransition hook first: the Studio's transition and census events), `said`/`refusal` (the engine's errors in kit's words, unchanged), `report` (OnTransition failures → problems; panics → log with stack), `observe` (a span per transition the loop fires), `onLoop` (the Runtime view); the store's writes and deletions reach the engine (`changed`/`forget` → `Changed`/`Deleted`) through `Store.watch`, whose hooks have an identity: a failed start takes its own back, and `stop` removes the workflow's own, never another's. A transition is one transaction (ADR 0004): `inTransaction` around `Start` and `Fire` — the writer turn taken before the entity —, one begun at `observe` for the loop's; the `OnTransition` hooks held until the commit (`afterCommit` reports a held hook's failure). `workflowPort` is the engine's port over `Store` (`plug.WorkflowStore`; `transitionWrite`: a key taken or an entity gone is an answer; `loopReads`: a replacement the loop decided on a read a rollback undid is refused, `Unavailable`); `workflowJournal` keeps the records in `<svc>/<name>.workflow.json`, in the format kit always wrote, and `sqlJournal` in `<svc>__<workflow>__workflow` when the store lives on a database — both `plug.WorkflowJournal`s, neither generic: the records spell the states, a state no longer declared dropped at load (`declaredStates`, `recordOf`) |
| `mail_plug.go` | ADR 0179 step 6 — the core's side of the mail subsystem: `mailPackage`, `mailsMissing` (`CodeSubsystemMissing`), `mailsLinked`, `validateMail` (the SDK's verdict through `plug.Mail`'s `Validate`), `mailRefused` (the spool's and the URL's refusals by kind, `plug.MailRefusal`) |
| `queue_plug.go` | ADR 0179 step 4 — the core's side of the queue subsystem: `openQueueWith` opens every queue through `plug.Queues` (`queuesMissing`, `CodeSubsystemMissing`, without it), the app's clock handed as a `plug.Clock` (`pluggedClock`), `doNotRetry` and `notRetryable` (the queue's mark on a delivery no retry helps), `deliveryHandler`, `queuesLinked`; `App.wrapQueue`, a test's `func(queue.Broker) queue.Broker` held as `any`, applied by queueengine (`plug.QueueConfig.Wrap`) |
| `mailer.go`, `mailer_outbox.go` | outbound mail: `Send` → the SDK's mail spool (in a transaction, the outbox ID minted at the call and the mail held until the commit, then `SendWithID` — `sendHeld`; `joined` in the transaction, before its commit, when the outbox is on the transaction's database, `annotate` reading what the Send's context carried through `mailMeta`) (`spool.New`, `pkg/v1/app/mail/spool`: queue, retries, lease, duplicates, dead letters; the outbox on the database that keeps the mailer's service — ADR 0178 C7 item 7, `openOutbox`: the SQL broker over `app_queue__<service>__<mailer>__outbox` behind the queue subsystem's sealed outbox (queueengine), which seals each mail under data-key itself bound to the mailer — the sealing kit's, `sealOutbox`, handed in `plug.QueueConfig`'s `Seal` and `Unseal` — and opens it at delivery, in a dead letter and in `Messages`, and seals one `PutMessage` and `PutDeadLetter` write, for `data copy` — a box that no longer opens is handed on and dead-lettered as undecodable —, `outboxPolicy`; the queue subsystem's broker handed to the mail subsystem as `plug.MailSpoolConfig`'s `Outbox` and the database's scope as its `Database`, which `mailengine` turns into the spool's `Broker` (`queueengine.BrokerOf`) and `Deferrer` (`storeengine.SQLOf`), so the mailbox hears of a joined mail after the commit and nothing after a rollback; `queueUnder`, `sealsAtRest` — a mailer on a database mounts app.Privacy —; else `<data>/<service>/outbox/<mailer>` or memory, unsealed) — the spool opened by the mail subsystem since ADR 0179 step 6 (`plug.Mail`'s `Open` with a `plug.MailSpoolConfig`, held as a `plug.MailSpool`; the core imports no `mail/spool`) → `outboxTransport.deliver` (kit's span and loop run per attempt, told the attempt as a `plug.MailAttemptValue`) → the subsystem's `plug.MailSink` (SMTP — `ParseSMTP` then `NewSMTP` —, or `Capture` in dev, a `plug.MailCaptureSource`); the SDK's verdict on a mail (`validateMail`) and on a sender (`senderProblem`, judged at the start in `mailerProblems`, no longer at the declaration) and the spool's refusals (`mailRefused`) through the subsystem — the core names the SDK's mail types, which `Send` takes, and nothing of its spool or transports; the Studio's `mailbox` is kept from the spool's events (`observe`); the SMTP URL is the mail connector's secret `smtp-url` (`frameworkSecret`), read again before every delivery — `current` rebuilds the transport when it changed; a failed attempt is a warning (`failed`), whether it is retried being the spool's to say |
| `mailer_bounce.go` | issue #489, a mail given up: the spool dead-letters a mail after its last attempt, at once on a permanent refusal (an SMTP 5xx, RFC 5321 §4.2.1) and once an attempt fails past the mailer's max-age (SDK #486); `mailengine` reads why from the failure's trail (`plug.MailEventValue`'s `GivenUp`, `MailGiveUp`) and its SMTP reply (`Reply`, a `*textproto.Error`); `observe` records it in the mailbox — `model.MailSummary`'s `GivenUp` (`model.MailRejected`, `MailExpired`, `MailExhausted`, `givenUpName`) and `Reply`, the reply's code, never its text — then, outside the lock and on a goroutine of its own (`hooks`, which `stop` waits for once the loop returned and before the spool closes) — the spool's observer holds its own lock while it tells, which a hook's `Send` would take again —, `bounced` hands a `BounceEvent` (the facade's `app.Bounce`) to the mailer's hook in a context of the app on the mailer's node (`withNode`, never cancelled by the stop) continuing the Send's trace: `OnBounce`'s, a panic logged, else `logBounce` — an error naming why, the reply's code, never its words. Each mailer declares two settings in its service (`declareSettings`, `declareNodeSetting` — `nodeSettingName`, which `trashSettingName` shares; a name no setting may take, `node.setting-name`): `<mailer>-max-age`, `MailMaxAge`'s default or `defaultMailMaxAge`, 5 days (RFC 5321 §4.5.4.1), negative refused (`mailer.max-age-negative`), 0 no age; `<mailer>-retry-permanent`, false — handed to the spool as `plug.MailSpoolConfig`'s `MaxAge` and `RetryPermanent` (`maxAge`, `retriesPermanent`) |
| `datacopy.go`, `datacopy_interface.go`, `datacopy_engine.go`, `datacopy_store.go`, `datacopy_journal.go`, `datacopy_queue.go`, `datacopy_digest.go`, `datacopy_verify.go` | `Main`'s `data copy --to NAME` (ADR 0178 C7 item 15): the data directory copied into a database, with the product stopped (`dataReady`) — every database but the target opened, the stores started (those the target keeps start under the data directory, the target not open yet: the copy's source), then the target opened and migrated as the start opens it; every table the target keeps (`dataCopyParts`): each store's (`copyParts` — the store's own engine on the data directory read through `plug.DocumentSource` behind the history and the sealing, its documents a `plug.Documents` (`copyDocs`), the target engine opened as `startSQL` opens it, `openCopyTarget`, written through `plug.DocumentSink` (`copySink`); its versions and trash with the SDK's `VersionWriterContext.PutDocument` behind storeengine's, a store without versions inserted as it rests; its history's table, `historyPart`), each workflow journal's (`journalPart`, the `<workflow>.workflow.json` file read as documents by key), and each queue's whose file queue holds a message (`queueParts`: `plug.MessageMover` and the broker's dead letters on the file queue and on the table (`copyQueue`), both opened by the queue subsystem — a leased message written waiting, visible at its lease's deadline —; a mailer's outbox sealed as its own is, `sealOutbox`, each mail sealed on its way in); refused when a table holds a row (`refuseRows`, `DATA_COPY_TARGET_NOT_EMPTY`); one transaction on the target (`plug.Database.Transact`) in which every table is copied and then verified — each store's running SHA-256 over its records in key order (`storeDigest`, `recordDigests`: the document's `SHA-256(store ‖ key ‖ bytes)`, each version's `SHA-256(store ‖ key ‖ number ‖ instant ‖ metadata ‖ bytes)`, the trash's instant, every part length-prefixed, the JSON compacted as both engines keep it), a mismatch walked again to name the first key that differs (`firstDivergence`, `DATA_COPY_DIVERGED`), three records of each store that seals read back through kit, opened (`sampleOpened`); each queue's messages as a sorted multiset of `SHA-256(payload ‖ dead ‖ deliveries ‖ reason ‖ code)`, a mail's payload opened —; a difference rolls the transaction back. Once committed, each file queue copied is moved under `<data>/.data-copy/<instant>/` (`moveQueuesAside`), so `consumer.file-queue-left` is no longer said; the stores' files stay |
| `secret.go`, `secretstore.go`, `secretcmd.go` | declared secrets (`Service.Secret`: provided — `Optional` when the product can do without it: absent is no problem, `Present` says whether it is set now, and its run reads through `firstFound`, where the start looks, at every use —, or `Generated` with `RotateEvery`/`KeepVersions`, never optional; `Value`, `Present`, `Seal`/`Open`, `Sign`/`Verify` in `secret` spans; a rotation loop on the app's clock, whose rotator keeps what `inUse` says a version still needs and runs `afterRotation` after — kit's own `data-key`'s: `frameworkOwn`, read from `<APP>_<NAME>` since ADR 0180 step 4, kept as `app-<name>` since its step 5b (`kit-<name>` read until copied, `renamed_secrets.go`), `envName`/`keptName`/`variable`); where the environment keeps them (`<PRODUCT>_SECRETS`: unset = beside the data, `<data>/.secrets/store` + `key`, or memory; `env`; `memory`; `file:<dir>` + `<PRODUCT>_SECRETS_KEY`) — the variable `<APP>_<NAME>` (or `_FILE`) always first, the framework's own under the same prefix (`frameworkEnv`, the product's environment store) and `app-` in the store; `Main`'s `secrets list\|set\|rotate` (`list` says `operator, optional` and `absent`, and kit's own after the product's: `app-data-key`, `app-index-key` when the app uses it — `usesIndexKey`: personal data, sealing, an indexed store on a database; never rotated, `set` only where none is kept —, `app-smtp-url`; a former `kit-…` name named is refused naming the present one, `noSecretNamed`) |
| `database.go`, `database_place.go`, `database_check.go`, `database_floor.go`, `database_run.go`, `database_migrate.go`, `database_view.go`, `migratecmd.go` | the floors (ADR 0178 C2, `database_floor.go`): a MySQL server is read once with `SELECT VERSION()` at its start (`checkServerFloor`, in `bringUp` after the check) and refused below MySQL 8.0.1 / MariaDB 10.6 (`CodeDatabaseVersion`, `DATABASE_VERSION`, naming the server and version, never the rest of its answer); a SQLite database's `<name>-timeout` under 500 ms, or a URL busy timeout (`_busy_timeout`, `_timeout` winning as in the driver) at or over it, is a start error (`sqliteProblem`, `database.sqlite-timeout`, `database.sqlite-busy-timeout`), the URL never rewritten; an engine that is a `LockWaiter` is opened with `OpenWaiting` and `lockWait(T) = T − max(250ms, T/10)` (`openPool`). Then databases (ADR 0004, steps 1 and 2) — declarations, placement, problems, the run, migrations, what the graph sees, the command: `Database(name, engine, …)` — an `AppOption`, its position recorded — with `Keeps` (a `Keepable`: a service, a store, a module) and `Migrations` (kit's own `MigrationValue`s since ADR 0179 step 10 — `migration.go` —, handed to the store subsystem as `plug.MigrationValue`s; its value is also a `ModulePart`); the public `Engine` (`Dialect` — kit's own type since ADR 0179 step 3, `DialectPostgres`, `DialectMySQL`, `DialectSQLite`, which the store subsystem maps onto the SDK's —, Describe → `DatabaseURL`, Open with a `current` URL read again before each connection) that the engine modules (`connectors/*`, own Go modules) implement; `placementOf` (a store kept by name, then its service, then its module, then the default database — the one without `Keeps` —, which never takes kit's own `app.privacy` stores: `app.Keeps(app.Privacy)` places them, `asKept`; `InMemory` wins; a store `apart` — kit's data keys — is never on SQLite, whose one writer a transaction may hold while a key is written: it stays in the data directory; the store lives on its database once open, in dev without a URL in the data directory); `databaseProblems` (all at once, at the `app.Database`: name, two defaults, kept twice, unmounted, memory store kept by name, derived name taken, two of kit's tables under one name; then the URL — `<APP>_<NAME>_URL`, `_FILE`, `<name>-url` in the store, `<data>/<name>.sqlite` for SQLite —: missing outside dev, unreadable, invalid, TLS left to the driver outside dev; warnings in dev); the settings kit declares for each (`<name>-max-open`…, `settingBase.database`, a `check`); the component `database:<name>` after the secrets and before the stores (`startDatabase`: open, pool, kit's scope on it — `tune` asks the store subsystem's `Database` (`plug.DatabaseConfig`: the pool, the dialect, the tuning, the timeouts), the SDK's transaction manager, its check (`plug.Checker`) and its migrator (`plug.MigratorProvider`, `migratorOf`) behind it — the queues' SQL broker (step 4) and a mailer's spool (step 6) reach the database's transaction manager through `storeengine.SQLOf`, in queueengine and mailengine, never through the core —, check, `migrateAtStart` — kit's set first, then the modules' and the product's; `start` or `manual` —), a readiness check never a liveness one, `runtime.databases` and `GET /_app/api/databases` (dev); `Main`'s `migrate [status\|up\|down SET VERSION]` — kit's set first, never down. Nothing a driver says leaves these files: errors are `dbFailure` (kit's words, the SDK's verdict, ≤ 120 runes) |
| `migration.go` | ADR 0179 step 10 — kit's own migration, which the facade publishes as `app.Migration` so that it imports none of the SDK's sql: `MigrationValue` (`Version`, `Name`, `Up`, `Down`, the SDK's shape), `MigrationStep` over `MigrationExecutor` (in `database_interface.go`: `ExecContext`, `QueryContext`, `QueryRowContext`, the standard library's types), `Statements` (each statement in order, the driver's error as it is), `Irreversible` (`CodeMigrationIrreversible`), and `pluggedMigrations`, which `Migrations` hands the store subsystem as `plug.MigrationValue`s — storeengine runs them on the SDK's migrator, nil steps refused there as before |
| `policy.go` | issue #483, the policies in front of an operation's handler as declared settings: `RateLimit`, `RateLimitPerClient`, `Timeout` and `Bulkhead` (constructed in `endpoint.go` and `ratelimit.go`) are each a `policy` over a `policyGuard` (`rateLimitGuard`, `clientLimitGuard`, `timeoutGuard`, `bulkheadGuard`) that declares, when the endpoint, the exposure, the implementation, the command or the query is declared (`declarePolicies`, after `Service.add`), its settings in the operation's service — `<service>-<operation>-<knob>` (`policySettingName`, `settingWords`: lower case, a capital after a lower-case letter or a digit a new word, any other character a dash), the code's value the default: `-rate`, `-burst`, `-rate-wait`; `-client-rate`, `-client-burst`; `-timeout`; `-bulkhead`, `-bulkhead-queue`, `-bulkhead-wait` —, each judged at the start (`policy.rate`, `policy.at-least-one`, `policy.not-negative`, `policy.positive`, `policy.wait`: the code's default included, where the SDK would refuse every call), a second policy of a kind or two operations spelled alike refused at the declaration (`policy.setting-twice`); the graph draws the values the app runs with (`mechanic`, `mechanicsOf`); the SDK runner is built from the values the start resolved, on the app's clock, at the operation's first run in that start (`runnerFor`, keyed by the `settingValues` map the start stored: a restart begins with full buckets); `MaxWait` (a `WaitConfigurer`, the SDK's `RateLimiterConfig.MaxWait` and `BulkheadWaitConfig.MaxWait`) and `BulkheadQueue` (a `BulkheadConfigurer`), the bulkhead always the SDK's `NewBulkheadWait`, which is `NewBulkhead` when nothing may wait |
| `setting.go`, `configcmd.go` | declared settings (`Service.Setting[T]`: text, bool, int, int64, float64, duration, list; `Required`; `Get` reads the app's resolved value, the default before) resolved at the config step (`resolveSettings`): default < `config/config.<ext>` < `config/<env>.<ext>` (embedded, `ConfigFiles`, read through the SDK's `config.FSSource`; only the JSON, YAML and TOML codecs are imported — `codec/json`, `codec/yaml`, `codec/toml` — so no other format's dependency reaches a product) < `<APP>_<NAME>` (read by name, parsed per type) < `app.Set`; every problem at once, never a value; a secret's name in a file is refused; a stray `<APP>_*` variable is a warning (Kubernetes service links excepted); `runtime.config` gets `service`/`key`/`type`/`detail`; `Main`'s `config` prints it all; a workflow timer may wait a duration setting (`Delay`), which must then be positive (ADR 0003) |
| `loop.go` | declared loops (`Service.Loop`: wakes, one goroutine, app clock), hand-written loops (`Service.Go`: run by the SDK's `lifecycle.Supervisor` on the app's clock and kit's backoff, its events drawn on the loop's state — `Routine.observe`), topic wake hooks |
| `jobs_plug.go` | ADR 0179 step 7 — the core's side of the jobs subsystem: `jobsPackage`, `jobsMissing` (`CodeSubsystemMissing`), `jobsLinked`, `openScheduler` (the scheduler `plug.Jobs` opens, each job added with its loop and its schedule read through the hook; `idleScheduler`, which fires nothing and waits for its end, for an app that declares no job — so the scheduler's component and loop, and the workflows' timers that start with it, need no subsystem) |
| `job.go`, `static.go` | scheduled jobs — the declaration keeps what the product wrote (a period, a cron expression), read into a schedule at the start through the jobs subsystem (`scheduleOf`), and judged there by the SDK's rules (`check`: `job.period`, `job.cron`), never at the declaration, which a package's init may run before the subsystem's; frontends, served by the SDK's `net/static` — plugged in by `framework/app/server` (`plug.NewStaticFiles`, `serverengine`) (a single-page application: a route with no extension is the page, a missing file with one is a 404, no directory listed, kit's CSP) — kit draws and observes each request. The Studio's own bundle stays on `introspect.go`'s `assets`, which serves it gzipped: the SDK's handler does not pre-compress yet |
| `app.go`, `config.go`, `main.go` | the components sorted into their groups (`componentNodes`: a command that is not queued is none), started in order, the serving ones last (`addServing`); `With` returns a copy (`appOptions.clone`: what an option changes in place — `app.Set`'s map, the bindings, the replacements, the mounts — is copied) and resolves the mounts: `a.own` is what `NewApp` lists, `a.services` every service the app runs, the modules' first; lifecycle, closed-by-default config (`envName`: <PRODUCT>_ENV as written, which names the environment's configuration file), `Main` (serve / graph / healthcheck — the SDK's `health.Ask`, the reason on stderr — / config / secrets / migrate / privacy); `Start` times each of its steps (`step`: config — which opens the secret stores when the app has a secret or a mailer —, declarations, mount, routes, data, handler, components, serving) and `settings`/`secretSettings` say what the config read and from where — `runtime.boot` and `runtime.config`, the Studio's boot sequence; secrets start first, before the stores; kit's listener on the SDK's server engine, plugged in by `framework/app/server` (`startHTTP`: `plug.NewHTTPServer`, `serverengine` — `server.New` + one `http` group with kit's bounds and ONE socket — `httpShards`: the engine would otherwise shard with SO_REUSEPORT, letting a second process bind a busy port and `:0` open several ports —, bound address from `State`; in dev the engine's goroutines inherit the `http` loop label because `startHTTP` runs `Start` under `pprof.Do`; `stopHTTP` gives `Shutdown` the `httpDrain` deadline, which the engine applies only in `Serve`) |
| `app_profiles.go` | the process profiles (ADR 0147 §5, V-A): `Profile` (`server`, the default; `daemon` — no HTTP, no `/_app/`, listeners, `IdleStop`; `cli` — the stores and databases only, one command run by `Main`), `IdleStop` (the `idle` component: stop once, for d, no listener connection was open and no `Service.Activity` said busy), `Singleton` (a `pkg/v1/app/lock` file lock in `ipc.RuntimeDir`, `CodeSingletonHeld`) and `SingletonPer` (the lock named after `ScopeKey`), `profileProblems` (`optionProblems`, `nodeProfileProblem`, `servesHTTP`), `profileState` — next to `App`, which holds it; `Stop(ctx)`, a product ending its own run from any context kit gave it (`profileState.asked`, `Run`'s reason `asked`); `product()`, the runtime directory's name — the binary's for a role of one, so two products never share one (`locks/`, sockets, telemetry). A CLI run (`cliRun`) starts only its stores — never the singleton, so `<binary> daemon status` runs while the daemon holds it (`TestACLICommandDoesNotTakeTheSingleton`) —: no Studio, no git (`describeBuild` asked as production), no data directory unless a store is mounted, no health probe, no HTTP handler, no telemetry, no announcement on its streams — `TestACLIRunStartsOnlyWhatItsCommandReads` |
| `cli.go` | `Service.CLI` (its one-line help kept in `doc`; `CommandLineConfigurer`s: `DefaultCommand` — `Main` runs it when the first argument names no command, and with none, never answering a usage error; two are a problem, `defaultProblems` —, `FailSafe` — its status always 0, a failure, a start that fails and a panic logged, nothing written to its streams), `Stdio`, `CLIFunc`, `runCLI` over `runCommand` (a span `cli`, `CodeCLIFailed` on a non-zero status, `exitSoftware` on a panic), `startFailed` (`startMessage`: a start refused for its declarations prints every problem, as `serve` does — a missing registration package names the import to add, ADR 0179 §6 —, any other its public text), `reservedCommands`, `usageCommands` — the product's commands listed after `Main`'s usage, the default marked |
| `scope.go`, `scope_user_{other,windows}.go` | `ScopeValue`: a part of a path known at the start — `PerUID` (the user's UID, its SID on Windows), `PerExecutable` (links resolved), `PerConfigDir`, `PerEnv(name, fallback)` (`~/` expanded) —; `ScopeKey`, 16 hex digits of their SHA-256, which a client computes as the daemon did; `CodeScopeUnknown` when one cannot be read; used by `SingletonPer` and `SocketPer`; `currentUser` per GOOS (`scope_user_other.go`: the kernel's UID, no cgo; `scope_user_windows.go`: the token's SID) |
| `activity.go` | `Service.Activity`: a function saying the product is busy (`ActivityHandler`, kept in `Service.activities`); the idle watch (`startIdle`) calls every mounted service's on each tick and restarts the idle time while one says true — a panic is busy, and logged |
| `listener.go`, `listener_interface.go`, `listener_compliance.go` | `Service.Listen`: an inbound port that is not HTTP — a private socket (`pkg/v1/proc/ipc`, ADR 0148; a named pipe on Windows) speaking a versioned contract —, a lifecycle component; each connection a goroutine and a `connect` span; the stop closes the listener, cancels the handlers and closes their connections; `Listener.Dial`, `SocketPath`, `SocketPer` (the socket's name carries `ScopeKey`), `AllowPeers`, `declarationProblem`; `SocketPathIn`/`DialIn`, the path and the dial a client of another process role computes from the declaration and the daemon's app name, and `SocketPathFor(app, service, name, scopes...)`, the same rule for a client that does not import the declaration (D22); a connection's close error is logged (`closeLogged`), `accepter` is what the accept loop needs of the socket, `var _ starter = (*Listener)(nil)` |
| `telemetry.go` | the telemetry port (ADR 0149): `Telemetry(path, gids…)` and `<PRODUCT>_TELEMETRY`/`<PRODUCT>_TELEMETRY_GIDS` (an exporter on a private socket, configured at the start only, never in a CLI run), `DesignDigest` for the handshake, `startTelemetry`/`stopTelemetry`, `report` (every span) and `reportPhase` |
| `roles.go` | `NewBinary`, `Binary.Role` (an App per process role, selected by its leading arguments), `Binary.Talks` (the one edge between two roles, a versioned contract — D22), `Binary.Main`; `describeBinary` draws the binary, its roles, the `contracts` edges and what this role `runs` (`roleInfo`); `exitConfig` and `exitUsage` are its exit statuses |
| `app_analysis.go` | `Analyzer`: the static analysis is injected — the framework links no analyzer; the platform's kit tool gives one in dev |
| `observe.go` | spans → hub (a command of the product's CLI, with no Studio, opens no SDK span: no identifier minted — `traces`): counters, per-entry traces (and those through a node: a delivery never roots one), SSE fan-out; pprof labels, span users and payloads; in dev, the product line that made a call (`callSite`: `code.filepath`/`code.lineno`, first frame outside kit, the SDK and the runtime); only a 5xx makes a span an error |
| `renamed_rest.go` | ADR 0180 step 5a, the names at rest moved: `moveAtRest` (dev's `.kit/data`, the data directory's `kit.privacy`, `movable`/`moveDir`), `renamePlan`/`formerTablesOn`/`renameTables` (the store subsystem's `FormerTables`/`RenameTables`), `tableRegistry.rekey`, `renameTaken`/`renameFailed` (`CodeRenameTarget`, `CodeRenameFailed`) |
| `renamed_labels.go` | ADR 0180 step 5c, the labels of the references: `referenceLabels`, `presentLabels` (`app/v1 …`, every reference made), `formerLabels` (`kit/v1 …`, read for the window), a person's and a record's two data keys (`derivedKeys.personRefs`, `ownRefs`, `subjectRefs`) |
| `renamed_refs.go` | ADR 0180 step 5c-2, the pass that seals again what a former release sealed: the `relabel` component after the stores (`addRelabelComponent`, through `storeHooks.addRelabel`), the holds filed again under their records' present references (`relabelHolds`, `relabelHold`: the new one written before the former goes, a hold whose key no longer opens left where it is filed; `dropFormerHold`, `presentHolds` — one hold per record), each store's records, former values and versions moved from the former keys to the present ones (`storeCore.relabel`, `relabelOne` under the keys' stripes held shared, `underAny`; a record in the trash keeps its versions) |
| `renamed_secrets.go` | ADR 0180 step 5b, the secrets and data-key's label: `ownSecretPrefix` (`app-`), `formerSecretPrefix` (`kit-`, read, never written), `sealDomain`/`formerSealDomain`, `reservedPrefixOf` (a product's secret, setting or database takes neither prefix), `moveFormerSecrets` (`kit-index-key` and `kit-smtp-url` copied once, at `openSecrets`; `copyNeeded`, `copyFormer`), `formerDataKeyring` (`kit-data-key`, the root `app-data-key` replaces), `retireFormerDataKey` (its versions retired on its rotation's schedule, after a clean pass), `formerOwnSecret` (the secrets command names the present name of a former one) |
| `renamed.go` | ADR 0180 §3 and Q12, the runtime names: the framework's name as the running product says it (`frameworkName`, `app`: the probes' checks, the loops it writes, the settings it owns, the actor of what it does on its own) and its former one (`formerName`, still a reserved module name while what a former release kept may be met under it — ADR 0180 step 5 —, until the minor that re-seals the labels); the tag key `app` (`tagKey`) and the former `kit` (`formerTagKey`); the route space `/_app/` (`reservedPrefix`, `healthPrefix`) and the former `/_kit/` (`formerReserved`, `reservedPath`); the operator's variables by their stem (`varEnv`…, `operatorStems`), read as `<PRODUCT>_<STEM>` (`productVariable`, `App.variable`, `ownEnv` in `config.go`) and reserved in the product's namespace (`reservedVariable`, `reservedVariables` in `setting.go`: a setting, secret or database URL read from one is refused, `setting.reserved`/`secret.reserved`); the refusals of an old name at the start, every one at once and carrying `CodeRenamed` (0.4.2.85): a `KIT_<STEM>` variable set (`renamedVariables`, `renamedRefusal` — in `resolve`, so every command, and in the healthcheck; none for a product whose prefix is `KIT`), a field still tagged `kit:"…"` (`renamedTag`, gathered by `structRules` into the plan's `renamed` and its problems, `classify.tag-renamed`; `App.renamedTags` for the refusal's `DiagnosticsError.Renamed`, "<old> -> <new>", sorted); `DiagnosticsError.Unwrap` (`CodeSubsystemMissing`, `CodeRenamed`, both joined) |
| `graph.go`, `introspect.go`, `introspect_{unix,windows,other}.go` | `App.Graph`; the three probes under `/_app/health/` and the reserved `/_app/` (`mountHealth`, the core's; since ADR 0180 step 4 the former `/_kit/health/{live,ready,startup}` answer the same, each logged once a run as deprecated — `deprecatedProbe` —, and the rest of `/_kit/` stays reserved, for two minors); `EnableStudio`, which stores `mountStudio` — the one place a `studioHost` is made — for a serving app in dev to mount the Studio's routes `framework/app/studio` plugs in (`plug.Studio`, `studioengine`, ADR 0180 step 1); the source endpoint's read, which stays beside the app's roots (`openSourceRoot`) and is reached only through the host: `readSource` — the name's `Lstat`, then `readSeen`: one open with `sourceOpen`'s flags, the handle's `Stat` the same file (`os.SameFile`), regular and within the bound, read from that handle; `sourceOpen` is `O_NONBLOCK` on Unix, where `os.Root` follows a link whatever the flags, and `FILE_FLAG_OPEN_REPARSE_POINT` on Windows, where it opens the link itself), the injected static analysis (`app_analysis.go`) |
| `daemon.go`, `daemon_http.go`, `daemon_loops.go` | `Runtime`: the signals that end `Run` (SDK `signal`), phase history, the run's config and boot steps, loops (provenance, live state), the scheduler runner (over a `plug.JobScheduler`: the jobs subsystem's, or the core's `idleScheduler`; it hears each fire as a `plug.JobResultValue`), the listener's connection counts — the engine's, `countAccepts` — and its request counters and middleware (`instrumentHTTP`), the process sample — `processSample` (`devtools.go`), from the sampler `framework/app/server` plugs in (`plug.SampleProcess`, `serverengine/sampler.go`: the SDK's `process.Self`), nil without it: kit imports neither `pkg/v1/proc/process` nor `runtime/metrics` (ADR 0172) |
| `architecture.go` | `Graph.Architecture`: C4 context and containers, derived from the declarations — which nodes keep data (`keepsData`: a queued command's queue too), and where (`onDisk`) |
| `devtools.go` | what the dev tools read — the process sample (`processSample`, nil with no sampler plugged in), the mailboxes (`mailboxes`) —, `goTracked`; their routes are `studioengine`'s since ADR 0180 step 1, and none acts on the product (D13, ADR 0147 §8) |
| `studio_host.go` | ADR 0180 step 1 — `studioHost`, the app as the Studio's routes read it (`plug.StudioHost`): the graph, the phase, the clock, the hub's events, traces and logs (`studioLogs`), a workflow's instances, a store's items redacted, a source file the graph names (`openSourceRoot`, `readSource`), where a function lies (`sourceOf`), the mailboxes, the store tools' reads — `studioDatabases`, `studioFormer`, `studioRevisions`, `studioTrash`, `privacyView` —, the guard's facts (`cfg.studio.remote`, `cfg.allowedHosts`), and kit's answers (`replyError`, `writeJSON`, `NotFound`, `Invalid`, `CodeProfileRead`); made only by `mountStudio`, so a program that does not import `framework/app/studio` links none of it nor what it alone reaches (`framework/app`'s initgraph test, `studioCode`) |
| `logs.go`, `redact.go` | the product's logger — the terminal gated at the app's level by the SDK's `logger.LevelGate`, the ring at every level in dev, and on the terminal an encoder kit wraps (`redactingEncoder`: names, URL credentials, a value logged whole as its redacted JSON) — and the hub's log ring; redaction of payloads and log attributes by the SDK's `redact` (`redactor`: its words, the `kit` tag's `secret`, and a field rule — `boundSecret` — for the classes `personal`, `special` and the `subject` option by the comma rule, a cookie or a secret-named header, errors by `describe`) |
| `classify.go`, `classify_plan.go`, `classify_walk.go`, `classify_judge.go`, `classify_warn.go` | ADR 0006's grammar — `parseTag` (a class — `public`, `personal`, `special`, `secret` — and the options `subject`, `moderated`, `plain`, `erased`, `history=N`; every mistake a problem), the schema's classes (`classify`), `sealedAtRest` (a sensitive member, or the subject, not plain: what kit seals at rest), `rules.byName` (a document's walk finds a field by its JSON name); the plan per type (`planOf`, cached: `rules` walked over values to any depth, `members` as JSON pointers with `*` for every element, the subject judged — a subject inside a list or a map is a `resting` problem, refused only where kit reads an identity, ADR 0178 C5); the walks (`clearSensitive`, `cleared`, `stampErased`, `clearPath`, `subjectOf`, `withoutSecrets` — as deep as a document goes, a recursive type's too), the start's check (`classify_judge.go`: `classificationProblems`, each type once, at the first declaration that uses it: `typeSource.dataTypes` on stores, endpoints, topics, auth handlers, commands and queries, each a `dataType` that says whether it `rests` — a store's entity, a topic's message, a queued command's input: there alone a subject in a list is refused, once per type; an endpoint's, a query's, a synchronous command's and an auth handler's types accept it, as personal data for redaction), a store's items redacted by type (`redacted`); the name heuristic (`classify_warn.go`: `looksPersonal` — a name that mentions personal data, on a type that can hold what it mentions, `holdingOf` —, and `secretLooksPersonal` — a field tagged secret that reads so and whose name says no credential, `credentialNames`: the migration from secret to personal —, each warning keyed by the field where it is declared, `declaredAt`, so a promoted field is warned of once per app) |
| `privacy.go`, `privacy_keys.go`, `privacy_erase.go`, `privacy_export.go`, `privacy_info.go` | kit's own service (`app.Privacy`; a per-app copy, `app.privacy`, mounted first by `mountPrivacyService` — reached through `mountPrivacy`, the store subsystem's hook (`store_enable.go`) — when the app keeps personal data or seals what it keeps on disk — its store of data keys and its `data-key` only then, `declareKeys`); the reference keys (`index-key`: `<PRODUCT>_INDEX_KEY` or `app-index-key` (`kit-index-key` copied, step 5b), made on first use, never rotated, warned of when it lives in memory alone while the data persists — `indexKeyProblems`, `privacy.index-key-memory`; HKDF subkeys, HMAC references — `derivedKeys`: `referenceKeys` under `presentLabels`, the former ones under `formerLabels`, read for the window, ADR 0180 step 5c) and the store's `app:subject` index (`frameworkIndexes`, `subjectKeys`); `Store.Erase` and `app.Erase` (`eraseRecord`, `deleteRecord` — what they wrote is folded even when the journal entry fails —, `eraseOnRequest` returning the data keys a record was sealed under, `sealedRefs`, `moveOwn` a held record's former values away from its person's key); `app.Export` (secret members left out at any depth); `privacyInfo` for the model, `privacyProblems` (errors always; the warnings when `explain`: dev, `config`, `privacy`), `privacySettings` |
| `retention.go`, `retention_loop.go`, `retention_run.go` | the store options (`EraseAfter`/`EraseAt`, `DeleteAfter`/`DeleteAt`, `Anonymise`, `HeldUntil`, `Purpose`, `DeleteOnErasure`, `RetentionByProduct` — the product keeps the store's retention: no loop, no warning of a missing retention or subject, no such gap in the register, refused beside kit's own —: one `privacyOption` type, checked for the entity type by `declarePrivacy`'s `privacyDecl`), `<PRODUCT>_RETENTION`; each store's loop (`retentionRun`: an agenda from the store's writes — `watch` —, a timer on the app's clock), started by the `retention` component after the daemon's loops; its runs (`sweep`, `retainOne` under the store's hold lock, `fold`) |
| `hold.go`, `journal.go` | legal holds in `kit.privacy/store/holds` (`Store.Hold`/`Release`, `app.HoldSubject`, `holdRefusal`: the `Conflict` `Delete` answers; `lockHolds`: placing a hold, and checking for one before an erasure or a deletion, one at a time per store); the privacy journal in `app.privacy/store/journal` (`journal` — an entry made in a transaction held until the commit —, `appendJournal`, `chainHash`, `verifyChain`; a journal a former release kept — its last entry without `refs` — closed as it is by the next entry, after a `continue` entry naming its last hash and starting a chain of its own, `continueJournal`, ADR 0180 step 5c-1; every entry this release writes says `refs: app/v1`; an entry's reason sealed under its month's key, `j:YYYY-MM`, never one key per entry — ADR 0178 §C3, `TestJournalEntriesShareTheirMonthsKey`) |
| `seal.go`, `seal_json.go`, `seal_store.go`, `seal_store_engine.go`, `seal_message.go`, `seal_erase.go`, `seal_check.go` | ADR 0006, step 3 — sealed at rest: the `sealer` (the SDK's `SubjectKeys` over kit's store of data keys, `keyStore`, wrapped under `data-key`'s keyring; `seal`/`open` a member's value, `"sealed:v1:"` and the SDK's box in base64url, `"sealed:v1:root:"` under data-key itself, padded to 16 bytes; `boxRef`; `sealLocks`, 64 stripes a write holds shared from its seal to its commit — in a transaction to its end, `heldToTheEnd` — and a destruction takes `alone`, by `TryLock`; kit's store of data keys is apart from every transaction), `a.sealing` made once per run, `dataKeyring` (the command line's, no loop), `dataKeysInUse` and `rewrapDataKeys` (rotation), `sealsAnything` (who gains kit's own service); a document walked as it rests (`objectMembers`, `sealWalk` over the members kit seals, `boxWalk` over every box, `setAt`); a store that seals (`seal_store.go`: how a record is opened and sealed; `seal_store_engine.go`: the port's calls; `sealedEngine`: a `plug.StoreEngine` of records as they rest inside, whatever the app places the store on, its documents to its caller opened — `docKey`, `keyAtRest`, `decodeAtRest` and the index functions see a record opened, `lenient` reads the key from what is not sealed, `refOf` the data key — its subject's, else its own, its own while a hold keeps it; a privacy journal entry's, its month's, `journalRef`: `"j:"` and the month of its instant in UTC, on the app's clock (ADR 0178 §C3) —, `sealKeyed` keeps in clear what the store's key reads and says so once (`clear`, a `concur/snapshot.Value` that `learn` adds to in one serialised `Update`, so two writes learning at once both keep what they found), `Write` seals a replacement under the writers' lock, `dropOwnKey` — at the commit in a transaction —, `refsAt`, `inClear`); messages (`sealedTopic`, `sealedQueued`: a topic's and a queued command's envelopes, bound to a random `ref`, the subject's key, the dispatcher's, else data-key); `shredPeople` (app.Erase's keys, a `Shred` span, `shred` journal entries, the keys' store folded), `shredIfLast` (a person's last record gone — erased, deleted by a retention, or by the product's `Delete`, `removeUnlessHeld` — once `lastOf` finds nothing kit keeps that needs the key: no record of theirs, no hold naming them, no former value nor version sealed under it, `sealsUnder`; `TestDeletingAPersonsLastRecordDestroysTheirKey`) — both at the commit in a transaction; `sealingProblems` (keys or data-key in memory with a store on disk refused, `<PRODUCT>_DATA_KEY` not 32 bytes refused, a database keeping the keys beside what they seal warned of — never SQLite, which keeps no keys (`placementOf`), so a store they seal lives beside app.Privacy there) |
| `history.go`, `history_engine.go`, `history_write.go`, `history_read.go`, `history_view.go` | ADR 0007 §1, a field's former values (`history=N`): `historied`, the engine a store that remembers — or has a password policy — runs on (`withHistory` at start: the history in `<svc>/<store>.history.json`, where the store keeps its data, reconciled with its records); every write goes through the engine's `Update` (`Write`: a Put whose key is new inserts, and updates when another writer inserted it meanwhile; a record the type no longer decodes is overwritten as before), the history written inside it, before the record (`updating`, `record`: only a field that changed; `pushed`: a head equal to the value replaced — a crash's duplicate — goes; `prune`: n per field, none while a hold keeps the record), put back when the engine refuses the record (`undo`); `Delete` removes the history after the record; `Store.Former` (a secret's without its value) and `formerAll` (read past a crash's duplicate); an erasure takes the former values of what it clears (`erasing`, `historyIntent`, `keepsErasable`); an export's former values (`exportFormer`); `historyInfo` for the model; `shownFormer` and `GET /_app/api/former` for the Studio; `toHistory`/`fromHistory` seal and open a former value (under the data key of the record it was part of, bound to the store, the record, the field and `former`), `sealFormer` (the privacy command) and `moveFormer` (a held record's, at its person's erasure) |
| `passwords.go`, `passwords_use.go`, `passwords_warn.go` | ADR 0007 §2, the password policy: `Store.Passwords` (`//go:noinline`; its field found by calling the function on a zero entity; a secret field, one policy each), `MinLength` (15 by default, 8 at least), `NotReused` (1–100, implies history=n), `NotCommon` (every policy's: the SDK's `password.IsCommon`, `isCommon`, before anything is hashed); `checkPasswords` refuses a write that puts anything but a PHC string into the field — a value left as it was passes; `Set`, `Change` and `Verify` in spans labelled `password`, on the SDK's `password` (`hashPassword`, `verifyPassword`, `needsRehash`: variables the tests replace); `verifyAll`, every former hash verified whichever matched, concurrently, at most `GOMAXPROCS` — the SDK's `concur/group.Collect`, whose bound is taken before each goroutine starts and which re-raises a verification's panic on the caller's goroutine; a missing key verified against `dummyHash`, made before the first read; a weaker hash stored again at a login, recording nothing; `passwordWarnings`, in dev, the endpoints that set or change a password with no rate limit — found on the runtime's edges and the static analysis's |
| `revisions.go`, `revisions_engine.go`, `revisions_kept.go` (the engines' `plug.VersionKeeper`, which the core's wrapping engines hand on: `keptVersions`, `rewriteKept`, `errVersionsNotKept`), `revisions_diff.go`, `revisions_restore.go`, `revisions_privacy.go`, `revisions_view.go` | ADR 0007 §3, a record's revisions (`app.Revisions(n)`, 1–100): the SDK's document store keeps them with each record, in its own write (`versionsOn`: `Versions`, the app's clock, `Held` — `versionsHeld`, kit's holds and the store's `HeldUntil`; `<table>___vs` on a database, one more table of kit's set, `tableMigration`); every engine writes stamped (`stampOf`: `by`, `command` — `withCommand`, set where a command's pipeline begins —, `InPlace` — `withInPlace`); `plug.VersionKeeper`, the sibling interface that reads and rewrites them as they rest (the store subsystem's engines, and the sealing and history engines, which hand them on); `Revisions`, `Revision` (secrets zeroed, `CodeRevisionDecode`), `Diff` (`jsonpatch.Diff`, a secret's values replaced by `secretTokens` and taken out), `Restore` (the store's own update: `keptOnRestore` — the secrets, the workflows' `states` —, `restorable`, `copyMember`); a sealing store's writes (`keptUpdate`: a record that means what it meant kept as it rests, `sameRest`, or sealed again in place, `sealInPlace`); a transition of the state alone in place (`stateOnly`, workflow_ports.go); a local rollback's write back by nobody, pruning nothing (`undoing`), then `forget`; ADR 0006 — `eraseVersions` (in `clearRecord` and `clearMembers`), `versionsErasable`, `moveVersions` (in `moveOwn`), `sealVersions` (`privacy seal`), `exportVersions` —; the Studio's `GET /_app/api/revisions` (`revisionsShown`, `shownEdits`), a read — restoring is `revisionscmd.go`'s |
| `trash.go`, `trash_run.go`, `trash_engine.go` (the engines' `plug.Trasher`, `trashOf`) | ADR 0178 C4, the trash of a store with `Revisions(n)`: `declareTrash` (its setting `<store>-trash`, `trashSettingName` — the store's name lower-cased, `_` and `.` as dashes —, 30 days by default, `trashDefault`; a negative one refused at start, `store.trash-negative`; a name no setting may take, `store.trash-name`), `trashLength`, `trashes`; `Untrash` (`untrashLabel`) and `Trashed` (`TombstoneEvent`: key, deleted at, `purgeAt`); the funnels — `discard` (`Store.Delete`'s, through `removeUnlessHeld`: to the trash, or `remove` when the setting is 0), `trashRecord`, `untrash`, `purgeTrashed` (the person's key after, `shredIfLast`) —; `eraseTrashed`, a person's erasure purging theirs from the trash and journaling each as a deletion; `trashedValue`, `trashedSubject`; `plug.Trasher`, the sibling interface the store subsystem's engines, `historied` and `sealedEngine` implement (a rollback of a trash restores, of an untrash trashes again; a purge drops the record's own data key, `dropOwnKeyAfter`, and its former values); `trash_run.go`: the `trash` component (`addTrash`), each store's agenda and loop (`trashRun`: what is past due purged at start, the rest at its instant on the app's clock — never a sweep —, a loop of kind `retention`, `<store> trash`; `arm` arms its timer again when the clock moved while it armed it, so a manual clock moved meanwhile never puts the instant past the timer; each agenda entry, `trashEntry`, names its tombstone by the instant it was trashed, so `purgeScheduled` empties only that one — read and purged in one transaction on a database — and `forget` drops only that entry; `Untrash` leaves the agenda as it is, a rollback putting the record back; a purge that fails at start is logged and retried, never a refused start) |
| `trash_view.go` | ADR 0178 C4 amended on 2026-10-15, the Studio's read of a store's trash: `GET /_app/api/trash?store=&offset=&limit=` (`studioTrash`, read through `studioHost.Trash` by `studioengine`'s route behind its guard), `trashOf` (`trashSource` — `CodeTrashList`: an unknown store a `NotFound`, `TRASH_STORE_UNKNOWN`; one that keeps no trash, `keepsTrash`, an `Invalid`, `TRASH_NOT_KEPT`), `trashPage` (`limit` 1 to `maxTrashed` 1000, `defaultTrashed` 200; `offset` from 0), `trashShown` (a `model.StoreTrash` in key order: `Total`, the page's `TrashedRecord`s — key, `At`, `PurgeAt` as `Trashed` computes it, never the value — and `Next`, the offset of the page after; an offset, never a key cursor, so a hidden key is never quoted), `keyShown` (a key is hidden — `Hidden` — when the type holds a personal, special or secret member and the trashed record, those members cleared, no longer gives the key, or no longer opens) |
| `revisions_weight.go` | ADR 0007 §5, what a store's versions weigh: `plug.VersionWeighter`, a sibling of the store port the engines implement — the store subsystem's file engine on the SDK's `Store.VersionWeight` (a counter), its SQL engine on `SQLStore.VersionWeight` (one aggregate statement), `sealedEngine` and `historied` forwarding to the engine they wrap (`engineVersionWeight`; a sealing store weighs its versions sealed, as they rest) —, and `weighVersions`, which `historyInfo` calls on a running product to fill `StoreHistory.Versions`, `Trashed` and `VersionBytes`, nothing when the engine cannot say |
| `revisionscmd.go` | `Main`'s `revisions restore -store ID -key KEY -rev N [-field PATH]…` (ADR 0010, D13: the Studio shows this command, it restores nothing): `withData` as the privacy command does, the product stopped; `restoreVia`, one root span on the store that says who asked (`via: cli`), no edge |
| `records.go`, `register.go` | ADR 0008's port: `app.Mark`, `App.Fields`, `App.Records` (`Get` without secrets, `Keys`, `EraseFields`, `Delete`, journaled) and `app.RecordsOf(ctx, store)`, the same for the code the app runs — a module's watch; the register of processing (`register`, `recipientsOf` the graph's reads edges and their mailers) |
| `records_keys.go` | `Records.Keys` (ADR 0178 C6): `plug.Keyser`, a sibling of the store port the engines implement — the store subsystem's file engine on the SDK's `Store.Keys`, its SQL engine in chunks of 256, each one call under `<name>-timeout`, `sealedEngine` and `historied` forwarding to the engine they wrap (`engineKeys`) —, `Store.keys` (refusals worded by `said`), `recordKeys` — `listedBy` refuses with `Forbidden` and `CodeRecordsKeys` (`RECORDS_KEYS_REFUSED`) a caller whose node (`currentNode`) is not of a module one of whose watches the store feeds (`App.watchedBy`, the product's when its module is nil), then one span and ONE `JournalList` entry per listing, however many keys it yields —, `yieldKeys` (the keys, then a failure once, as the last yield) |
| `watchcmd.go` | `Main`'s `watch replay WATCH` (ADR 0178 C6), the catch-up on an operator's demand: `withData` as the revisions command does, the product stopped — `askLive` refuses a product that answers —; `watchNamed` refuses an ID that names no watch the app mounts with `NotFound` and `CodeWatchReplay` (`WATCH_UNKNOWN`, its public `no such watch: <id>`), and lists the mounted watches on stderr (`watchIDs`); `replayNow` opens the watch's queue as its start does (`openConsumerQueue`: its table on the database that keeps its service, else its directory under the data directory) and, for each feeding store, journals ONE `JournalList` entry (by `cli`, reason `watch replay <id>`) before `replayStore` queues one notice per key; the watch hears them at the next start. No confirmation: a replay destroys nothing, and a watch hears at least once — a second replay is heard twice, which its idempotent handler tolerates; a deletion is not replayed. Says `watch.replayed`, or `watch.replay-partial` with what it queued before a failure. `watch` is a reserved command name (`reservedCommands`) |
| `watch_catchup.go` | the watch's catch-up (ADR 0178 C6): `runningMark` — `<data>/<svc>/watches/<name>.running`, written by `start` (`markRunning`, 0600 in a 0700 directory) and taken off by a `stop` whose consumer ended (`unmark`) —; a start that finds it (the last run did not stop cleanly) queues, before its consumer starts, one notice of a write per key of every feeding store (`replay`, one `replayStore` per store over `feeder.keys`, `post` — the loop `watch replay` runs too), every feeding store, its own database's too — a write outside a transaction is not atomic with its notice on any —; a lost deletion is not caught up; a failed catch-up keeps the mark and says `watch.catch-up`, a mark that cannot be written or removed `watch.running-mark`; an app without a data directory keeps no mark |
| `watch.go`, `watch_run.go` | ADR 0008's watch: `Service.Watch` (a node of kind subscription — `SubscriptionOption`s, a problem for a mark that is none of `marks`, a nil handler), `app.Written`; `feeders`, the start's one rule shared with the analyzer — the app's stores (`productStores`: kit's own left out) whose type carries the mark (`markedFields`, `App.Fields`' too), but the watch's own module's, unless `app.OwnStores()` (`ownStores`, a `SubscriptionOption` a topic's subscription refuses) —; `start` opens its queue (`app_queue__<svc>__<name>` on the database that keeps its service, else `<data>/<svc>/queues/<name>`), puts it on each feeding store (`Store.feed`, which returns what takes exactly it off; both are one `Update` of the store's copy-on-write `feeds`, the SDK's `concur/snapshot.Value`, so two watches starting at once both stay) and starts its consumer (`BatchSize` 1, woken by `change`); `stop` takes it off, then ends the consumer; `Store.notify`, called by the four funnels once kit confirms a write — `write`, `modify`, `remove` in `store.go`, `transitionWrite` in `workflow_ports.go` —, which skips a watch that hears its own stores when the write is its own handler's (`ownWrite`: the handler's context is marked with its watch, `handling`), and `noticeOf`, the `watchNotice` (store, key, deleted, the write's traceparent) encoded at the write, held with the watch's queue database: `post` in the transaction when that is the transaction's database — a refusal, `WATCH_NOTICE_REFUSED`, fails it —, else `Watch.notice` once the write stands, with the caller's cancellation removed, a refusal said as `watch.lost`; `deliver`, the handler told `Written` with the store's marked fields, in a context marked as its own; `describe`: `SubscriptionInfo.Mark`, `OwnStores`, `Stores`, a declared `delivers` edge from each store, dead letters |
| `privacycmd.go`, `privacycmd_print.go`, `privacycmd_seal.go`, `privacycmd_rewrap.go`, `privacyapi.go` | `Main`'s `privacy register\|export\|erase\|holds\|journal\|retention\|seal\|rewrap` (`rewrap [-back]`: `rewrapNow`, every data key moved under `app-data-key` now, or back under `kit-data-key` for the release before ADR 0180 step 5b; `seal STORE…`: `sealAll`, the records and former values kept in clear sealed, the store folded; `seal -all`: every store `sealable` takes that seals at rest, `sealableStores`, in ID order) (one function per sub-command; `withData` opens the stores itself, after `dataReady` refused data kept in memory, a data directory that does not exist, and a product that answers); the Studio's Privacy page (`GET /_app/api/privacy`, `POST …/privacy/retention\|export\|erase`, each a control event) |
| `profile.go` | `moduleSource` and `sourceOf`, which say where a function of the product lies: what the Studio's profiler (`studioengine`: the SDK's `profiling` captures, decodes, folds and groups; its routes are there since ADR 0180 step 1) asks through `studioHost.Locate`; `CodeProfileRead` stays kit's (`studioHost.ProfileUnreadable`) |
| `plug/`, `serverengine/`, `studioengine/` | the opt-in subsystems out of kit's import graph: `plug` holds the hooks kit reads (stdlib and model only), `serverengine` and `studioengine` the SDK's server, static, process sampler (ADR 0172), sse and profiling behind them, which `framework/app/server` and `framework/app/studio` plug in as they are imported — a product that imports neither links and initialises none of them. Since ADR 0179 step 1 `plug` also holds the store subsystem's contract (`plug.Stores`, `StoreEngine` over `DocumentValue`s and its siblings, kit's `Database` scope and its migrations, the refusals' kinds, `Transaction`), which `storeengine/` implements over the SDK's `docstore` and `sql` since step 3 and `framework/app/store` plugs in: the core reads it and imports neither; since step 4 the queue subsystem's (`plug.Queues`, `Broker` and its siblings over `MessageValue`s, `QueueConfig`, `Clock`), which `queueengine/` implements over the SDK's `queue` and `framework/app/queue` plugs in: the core imports none of the SDK's queue; since step 5 the workflow subsystem's (`plug.Workflows`, `WorkflowEngine` over `WorkflowEntityValue`s, `WorkflowConfig`, the ports `WorkflowStore` and `WorkflowJournal` the core implements), which `workflowengine/` implements over the SDK's `statemachine` — instantiated once — and `framework/app/workflow` plugs in: the core imports none of the SDK's statemachine; since step 6 the mail subsystem's (`plug.Mail`, `MailSpool`, `MailSink` and its sibling `MailCaptureSource` over `MailValue`s — the SDK's mail values held as `any` —, `MailSpoolConfig`, the refusals' kinds), which `mailengine/` implements over the SDK's `mail/spool` and its SMTP and capture transports and `framework/app/mail` plugs in: the core imports no `mail/spool` and links none of the SDK's transports — it still imports `pkg/v1/app/mail` for the types `Mailer.Send` and `From` name — and `plug.HandleProvider`, which handed the spool the SDK's handles while it was in the core, is gone; since step 7 the jobs subsystem's (`plug.Jobs`, a job's schedule from its period or its cron expression as a `JobScheduleFunc`, `JobScheduler` over `JobEntryValue`s, `JobSchedulerConfig`, `JobResultValue`), which `jobengine/` implements over the SDK's `scheduler` and `framework/app/jobs` plugs in: the core imports none of the SDK's scheduler; since step 8 kit's catalogues after English (`plug.Words`, `WordsConfig.Open` handed kit's English table, `Translator` — `Languages`, `Keys`, `Render`), which `studioengine/` implements over the SDK's `i18n` with `studioengine/locales/fr.json` and `framework/app/studio` plugs in — the Studio, their only reader (Q6): the core imports none of the SDK's i18n; since ADR 0180 step 1 the Studio's (`plug.Studio`, `StudioDriverConfig`: `Env`, `Settings`, `Forced`, `DevBuild` — the tool's variables, `KIT_DEV_*`, `KIT_STUDIO`, `KIT_STUDIO_REMOTE`, `KIT_ANALYZE`, which only `studioengine` reads — and `Mount`, the Studio's routes over a `StudioHost` the core gives); `plug.OpenEventStream`, `plug.StudioProfiler` and their types are gone, the stream and the profiler being `studioengine`'s own |
| `subsystem.go` | the start's check of ADR 0179 §6: `subsystems`, one row per registration package (the stores', `framework/app/store`, linked when it set `plug.Stores` and kit's own store code — `storesLinked`, ADR 0179 steps 3 and 3b; the queues', `framework/app/queue`, linked when it set `plug.Queues` — `queuesLinked`, step 4 —, for a topic, a subscription — a watch is one —, and, through `needsQueue`, a queued command and a mailer whose outbox is on a database; the workflows', `framework/app/workflow`, linked when it set `plug.Workflows` — `workflowsLinked`, step 5 —, for a workflow; the mailers', `framework/app/mail`, linked when it set `plug.Mail` — `mailsLinked`, step 6 —, for a mailer, beside the queue row for one whose outbox is on a database; the jobs', `framework/app/jobs`, linked when it set `plug.Jobs` — `jobsLinked`, step 7 —, for a job); `subsystemProblems` — one `subsystem.missing` diagnostic per declaration whose subsystem is not linked (a store node, a database, a node that seals what it keeps at rest — a topic, a queued command or a mailer sealed on disk, under data keys kit keeps in a store of its own (`sealsAtRest`); kit's own privacy service apart), at its position, all at once —; `missingPackages`, the packages `bootDeclarations` puts in the `DiagnosticsError`'s `Missing`, whose `Unwrap` (`renamed.go`) is then the `CodeSubsystemMissing` error, `profile.server-missing` one of its cases. `subsystems_internal_test.go` takes the hooks out as a program that does not import the package sees them (`withoutStores`: `plug.Stores` and `storeCode`; `withoutQueues`: `plug.Queues`; `withoutWorkflows`: `plug.Workflows`; `withoutMail`: `plug.Mail` — `TestAMailerWithoutItsSubsystemIsRefused` crosses it with `withoutQueues`; `withoutJobs`: `plug.Jobs` — `TestAJobWithoutItsSubsystemIsRefused`, `TestADaemonWithoutJobsNeedsNoJobsSubsystem`, `TestAJobIsJudgedAtTheStartThroughItsSubsystem`); the suite's binary imports it (`subsystems_test.go`) |
| `store_enable.go` | ADR 0179 step 3b — kit's own store code, which names no SDK type and stays in the core (§3), reached only through `storeCode`, the value `EnableStores` sets and `framework/app/store` calls as it is imported (as `framework/app/server` calls `EnableServer`): `storeHooks` — `mountPrivacy` (`mountPrivacyService`), `addDatabases`, `addRetention`, `addTrash` (`addDatabaseComponents`, `addRetentionComponent`, `addTrashComponent`), `journal` (`appendToJournal`), `problems` (`storeCodeProblems`: `sealingProblems`, `indexKeyProblems`, `databaseProblems`), `privacyProblems` (without the subsystem, `storePrivacyProblems` still judges the classification of every type, `classificationProblems`, once) and Main's commands on the stores, `migrate`, `privacy`, `revisions`, `data` and `watch` (`storeCommand`). Each call site is a method of the same name that falls back to what an app without a store does — nothing to mount, start, warn of or serve —, and a command without the subsystem prints `subsystem.missing` and exits 78. A hook's signature names no type of the store family, and the app's own type names none either — kit's own stores are `privacyRun.own`, an `*ownStores` held as `any`, read by `ownStoresOf`, and `keepsDataKeys` answers whether a store of data keys is among them, from `data-key`, declared with it —: the linker keeps a type a reachable value names, with its methods. The Studio's routes on the stores (`studioengine`'s `mountStoreTools` since ADR 0180 step 1: databases, former values, revisions, trash, privacy) stay reached through the Studio alone, mounted only when `studioHost.Stores` — `stores()` — says `storeCode` is set. `framework/app`'s initgraph test holds it (`kitStoreCode`) |
| `errors.go`, `catalog.go` | the wire mapping (`describe`), the wire codes a caller branches on — `Wire*` (`WireNotFound = "not_found"`, the platform's `Code*` values byte-for-byte, renamed because `Code*` names a dotted-quad `errs.Code` in the SDK) —, mechanics catalog, `NewID` |
| `gen_codes.go` | the framework's codes (`0.4.2.*`), written by kit gen from `design/framework/app.yaml` (ADR 0164) |
| `words.go`, `locales/en.json` | kit's sentences (ADR 0179 step 8, Q6). English is kit's own table — `english`, a `sync.OnceValue` reading `locales/en.json` with `encoding/json` at the first sentence said (a start that says nothing pays nothing), each sentence compiled once in the SDK i18n's grammar (`compileSentence`: `{name}`, `{{` and `}}`; `render`), byte for byte what the SDK's i18n said (`TestKitsEnglishIsTheSDKsEnglish`); a sentence that does not compile is `CodeCatalogue`, `CATALOGUE_INVALID`. The languages after English are the Studio's: `translator` opens the catalogues `framework/app/studio` plugged in (`plug.Words`) once per hook value, at the first sentence said with it — read at every sentence, never frozen, so that a sentence said before the Studio's init does not keep kit English —, against the English table: a language that lacks a key or has one more is `CATALOGUE_INCOMPLETE` (`openOthers`), a panic at that sentence. Every diagnostic is a `phrase`, one pointer to a `saying` — `say(key, name, value, …)` writes the English at the call and keeps the key and the values (a value written by `fmt.Sprint` at the call, a phrase argument kept as a phrase), so the other languages are written when asked (`in`), a phrase argument in the same language; `listOf` lists the way each language does; `spoken` is `Message`, the English, and `Texts`, every language kit speaks — `{"en"}` alone without the Studio —, which the Studio shows in its reader's (`frameworkLanguages`) |
| `build.go`, `ratelimit.go` | what the binary was built from (the SDK's `buildinfo.ParseBuild` — `pkg/v1/proc/buildinfo`, not `proc/process`, whose `Self` would bring `runtime/metrics` and the spawn engine into every product's initialisation, ADR 0172 —, read once per process — `readBuild`, `parsedBuild`, `productionBuild`, `frameworkVersion` —, every app and every graph handed its own copy (`cloneBuild`, `TestEachAppOwnsItsBuild`); in dev, `git.Head` for a local module), kit dev's account of the build — read by `studioengine` (`plug.StudioDriverConfig.DevBuild`), nil without the Studio (ADR 0180 step 1); an app holds its build as a `buildDescription`, described at the start in dev and at the first read otherwise — the graph, the telemetry handshake —, so a CLI run or a daemon nobody asks parses no build information (#276, `TestAProductionBuildIsDescribedWhenRead`); `RateLimitPerClient`'s keyed limiter (`newClientLimiter`, `clientKey`) |

The framework's own failure codes are `0.4.2.*` — layer 4 is the framework
module, PP 2 this package (ADR 0147 §3) —, numbered in the order the
platform's `0x7F.1.*` codes had, all declared in `gen_codes.go` but
`CodeProfileRead` (`profile.go`), the profiles' (`app_profiles.go`, `cli.go`)
and the scopes' (`scope.go`).

What is a mechanism rather than framework moves to the SDK, wave by wave
(the platform's `docs/adr/0001-the-sdk-holds-the-mechanisms-kit-is-the-framework.md`
has the map): before writing something generic here, look for it there.

## Rules specific to this package

- A diagnostic is a sentence of the catalogues, never a format string:
  `s.problem(at, node, "key", "name", value…)`, `a.problem(node, say(…))`,
  `diagnosticOf(severity, node, source, phrase)`. A new one adds its key to
  `locales/en.json` AND `studioengine/locales/fr.json` (same placeholders);
  `TestKitSpeaksEveryLanguage` refuses a key the code says and the catalogues
  lack, or the reverse. English stays what the terminal, the logs, an error
  and the tests read, whatever a program imports; French is the Studio's
  (`framework/app/studio`, ADR 0179 Q6).
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
  A queue's consumer — a subscription's, a watch's, a queued command's — runs
  under the SDK's `lifecycle.Supervisor` (ADR 0112), as `Service.Go` does
  (`consumer.supervise`): its queue's `Consume` — the SDK's, run by queueengine — returns when its storage
  fails, and the consumer comes back after kit's backoff (1 s to 1 min), its
  loop showing the failure (`consumer.failed`) and counting the restart; a
  test makes a queue fail through `App.wrapQueue`, nil in a product's app
  (`consumer_supervised_internal_test.go`).
  A goroutine the framework starts on its own — a listener's accept loop, the
  idle watcher — goes through `goTracked`.

## Invariants of kit v2

- **Auth spans carry no edge.** The auth handler runs in a span on its own
  node, `From` = the endpoint it guards, `Edge` empty: the hub records no
  edge for it. Which endpoints ask for a user is drawn by their pipeline
  (`auth` mechanic first) and `EndpointInfo.Auth`. What the handler does — a
  session read — is drawn from the auth node like any code.
- **A 429 says when to retry; a caller that left is no failure.** `App.reply`
  sets `Retry-After` on a 429 or a 503 whose error carries the SDK's
  `retry_after_seconds` (`resilience.RetryAfter`: the rate limiters' next
  token), in whole seconds rounded up (`retryAfter`, `ceilSeconds`). An
  endpoint's run that returns a cancellation while the request's context is
  cancelled — the caller left, and a `Timeout` gives way to it — is
  `statusCallerLeft`, 499 (`callerLeft`, in `serve`): no 503 or 504, no
  error logged, the span not in error, an information line logged under
  `context.WithoutCancel` (the logger drops a record whose context is done),
  and the status line written to nobody, as `writeJSON` already lets a write
  to a departed client fail. A cancellation the operation made itself, its
  caller still there, keeps its 503.
- **Auth runs before decoding.** The serve path is: span → response state →
  authenticate → decode → validate → policies → handler. The pipeline lists
  `auth` first; changing the order changes the diagram.
- **An in-process call is not authenticated again.** `Endpoint.Call` carries
  the caller's principal (a context value); an `Auth()` endpoint called
  without one answers 401 before running. `WithUser` is the way in for jobs,
  loops and tests.
- **Credentials never leave the mailer.** `<PRODUCT>_SMTP_URL` is parsed by the
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
  app's time: timers are tested with `app.Clock` and a manual clock. A
  loop's next run is its state's own copy: `loopState.idle` takes the instant
  by value, since the graph and the event stream hand `NextRun` out by
  pointer and a loop writes its own variable again
  (`TestALoopsNextRunIsItsOwnCopy`).
- **A workflow never polls.** The SDK engine's loop runs at the start, at the
  next transition due on its agenda, and after a write of its store that can
  bring one forward (the store's `onWrite` → `changed` → the engine's `Changed`) —
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
- **One run at a time, a burst is one run.** Wakes during a run, or a
  backoff, are gathered (`loopRun.pending`, a one-token channel whose token
  is sent and taken with its wake, under `Loop.mu`) into one run after it,
  whose `WakeEvent` says every one — `Reasons`, `Topics`, each once, in the
  order they came —; its `Reason` is a nudge's when one is among them, the
  first's otherwise, so a nudge behind a publish is never held by a backoff
  (`TestALoopsWakeSaysEveryReason`). After a failure the next run waits 1s→1m — the SDK's
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
  goroutine: `begin` sets `app_node` on it (dev only), `end` puts back the
  labels of the context `begin` was given. `TestLabelsAreRestoredWhenASpanEnds`
  pins it, nested spans included.
- **Dev tools exist in dev only, and they only read.** Payloads, the log
  ring, the heap profile and every `/_app/api` route beyond the graph,
  events, traces, source, instances and items are mounted or switched on
  only with the Studio (`cfg.studio`) in a serving app.
  `TestDevToolsAreAbsentInProduction` walks every route;
  `TestTheStudioActsOnNothing` walks every former control — mocks, faults,
  `jobs/run`, `workflows/fire`, `loops/wake`, `commands/dispatch`,
  `queries/ask`, `privacy/*`, `profile/cpu` — and wants a 404 in dev (D13).
- **Every Studio route** goes through `api(…)` in `studioengine`'s `mount` —
  behind its `guard` (ADR 0180 step 1). `TestDevToolsRefuseStrangers`
  walks every route; `studioengine`'s `TestStudioAnswersThisMachineOnly` the
  guard itself.
- **The runtime names carry no trace of the tool** (ADR 0180 §3, step 4).
  The operator's variables are the product's — `<PRODUCT>_ADDR`,
  `<PRODUCT>_DATA_DIR`…, `productVariable` — and reserved in its namespace;
  the probes answer under `/_app/health/`, the tag key is `app:"…"`, the
  graph says `app` (provenance, port owner, `appdev`, the build's field),
  the checks and the loops the framework writes are `app`, the pprof labels
  `app_node`/`app_loop`, a captured mail's header `X-Attachments`, the
  telemetry handshake `app-telemetry/v1`. An old variable or tag key is
  refused, never ignored (`renamed.go`, `TestAnOldVariableIsRefusedNamingTheNewOne`,
  `TestAFieldStillTaggedKitIsRefused`); an old probe path answers, logged
  as deprecated (`TestTheFormerProbePathsAnswerLoggedAsDeprecated`).
- **Nothing a product links names the tool, held by a guard** (ADR 0180 §6,
  step 6). `framework/app`'s `TestAProductCarriesNoTraceOfTheTool` reads a
  whole product's release binary — the symbols, so an identifier of this
  package is never named after the tool (`frameworkTable`, `frameworkSet`,
  `frameworkOwn`, `frameworkEnv`…) —, and `TestTheFrameworksLiteralsNameNoTool`
  every string literal here outside `studioengine` and every key and sentence
  of `locales/en.json`: a sentence quotes the present tag key (`app:"…"`), a
  catalogue key says what it refuses (`secret.own-prefix`,
  `module.own-service`), the diff's secret token is `\u0000app-secret:`. The
  former names of Q12's window and at rest — `formerName`, `formerPrefix`,
  `formerReserved`, and the refusals that name them — are the guard's
  allow-list, each with the step that removes it; a new one is refused.
- **The names at rest (ADR 0180 step 5a).** What a former release kept
  under kit's name is moved, once, never copied and never replacing
  (`renamed_rest.go`): `.kit/data` → `.app/data` in dev and `kit.privacy`
  → `app.privacy` in the data directory by rename(2) at resolve, both judged
  before either runs and the first undone when the second fails
  (`moveAtRest`); on a database `kit_migrations`, `kit_tables`,
  `kit_queue__…` and `kit_privacy__…` (with their `___ix`/`___vs`, and
  PostgreSQL's indexes named after them) → `app_…`, by the store
  subsystem's `RenameTables` (`renamePlan`, each `frameworkTable`'s `former`;
  the move a migration of `kit_migrations` at `renameVersion`, under its
  lock, the history copied into `app_migrations`, `kit_migrations` dropped
  once the move is recorded), then the registry's rows re-keyed
  (`tableRegistry.rekey`). A present name already taken refuses the start
  (`CodeRenameTarget`, 0.4.2.86) and nothing moves; a move that did not
  complete is `CodeRenameFailed` (0.4.2.87), the former names left
  readable and the next start resuming. A database that migrates manually
  refuses to start while a table is under its former name; `migrate
  status` says which, `migrate up` moves them. What a sealed value is
  bound to does not move: the privacy service's stores seal under
  `kit.privacy`'s ids (`sealedAs`), and a journal's entry a former release
  wrote names `kit.privacy/…` (`ownService`), until step 5c-2 re-seals them
  with the `kit/v1`/`kit:` labels of the references. Held by
  `connectors/sqlite`'s `testdata/former` (a former release's database and
  data directory, read back byte for byte), the PostgreSQL and MySQL
  suites' `former_test.go`, and `renamed_rest_test.go` for dev's directory.
- **The secrets and data-key's label (ADR 0180 step 5b).** The framework's
  own secrets are kept as `app-…` (`ownSecretPrefix`); `kit-…`
  (`formerSecretPrefix`) is read, never written, and both prefixes are
  reserved to the framework for a product's secret, setting and database
  (`reservedPrefixOf`). `renamed_secrets.go`: `openSecrets` copies
  `kit-index-key` and `kit-smtp-url` to their present names once
  (`moveFormerSecrets` — both judged before either is written; an index key
  under both names with two values is `CodeRenameTarget`, a copy the store
  refuses `CodeRenameFailed`; the SMTP URL copied again when the former is
  the newer), and `frameworkSecret` reads the former name while no copy is made.
  `data-key` is a new secret, `app-data-key`: its name is bound into what
  its keyring wraps, so nothing is copied — the sealer is given
  `kit-data-key` as the root it replaces (`formerDataKeyring`,
  `secret.SubjectKeysConfig.Former`), and the start's re-wrap pass
  (`rewrapDataKeys`) moves every data key under the new root, one
  compare-and-swap each (`RewrapReport.FromFormer`), resumed by the next
  start when stopped. A box sealed under data-key itself is sealed under
  `sealDomain` (`app/v1 sealed at rest`) alone and opened under it, then
  under `formerSealDomain` by data-key's keyring and by `kit-data-key`'s
  (`openRoot`). A subject has one wrapped key, under one root: an erasure
  destroys it and folds the store, so nothing either root opens is left in
  what the product keeps. A copy outside it — a backup, a database's own
  history — opens until the root version that wrapped it is pruned (ADR
  0142's bound), under `app-data-key` as under `kit-data-key`.
  `kit-data-key` no longer rotates; after a clean pass that found nothing
  under it, each of its versions older than data-key's Keep × RotateEvery is
  pruned — a fresh version written first when all are
  (`retireFormerDataKey`, `formerRetirement`) —, so that bound holds for it
  too. Nothing else a former release kept is written or deleted.
  `privacy rewrap [-back]` (`privacycmd_rewrap.go`) runs
  the pass now, or moves every data key back under `kit-data-key` for the
  release before step 5b. Held by `connectors/sqlite`'s
  `testdata/former-keys` (written by the release before this step, its
  secrets in the file store beside the data, a notice about nobody under
  `kit-data-key` itself) in `former_keys_move_test.go`, the PostgreSQL and
  MySQL suites' `former_keys_test.go`, `former_keys_erase_test.go` (a person
  erased after the move opens under neither root, nor after `rewrap -back`),
  `renamed_secrets_erase_internal_test.go` (a backup from before the move
  stops opening an erased person once `kit-data-key` is retired),
  `renamed_secrets_internal_test.go`
  (a move stopped after each of its writes, the label a box under data-key
  is sealed with, the former secrets read until copied) and the SDK's
  `subjectformer_external_test.go`.
- **The journal closed and continued (ADR 0180 step 5c-1).** On the owner's
  decision of 2026-10-18 the references and the journal take new labels
  for the future; the journal's chain hashes the references it records, so
  it is never rewritten. `journal.go`: `loadChain` reads whether the
  journal's last entry is a former release's (no `refs`: `privacyRun.formerJournal`); the next entry
  the app journals is preceded by a `model.JournalContinue` entry
  (`continueJournal`) naming that last entry's hash (`Follows`), dated as
  the entry it precedes, its own hash over nothing and itself — the
  durable mark of the close, written whole or not at all, never twice.
  Every entry after it names its record and subject under `presentLabels`
  (`refs: app/v1`) and seals its reason bound to the journal's own id,
  `app.privacy/store/journal`; the former's
  reasons open where they were sealed, bound to `kit.privacy/store/journal`
  (`openMember`, `sealer.openIn`: the present binding, then the former).
  `verifyChain` checks each journal's chain and the link (`linkOf`) — a `continue`
  entry whose `Follows` is not the hash before it, or a `Follows` on any
  other entry, breaks the chain there — and lists where a journal closed
  (`model.JournalCheck.Closed`); `privacy journal -verify` says it. Held by
  `journal_continue_internal_test.go` (the chain across a closed journal and
  every way of changing it; a former journal continued once, after a
  restart too; the former reasons open only where they were sealed; a close
  interrupted at either write resumed) and `connectors/sqlite`'s
  `testdata/former-journal` in `former_journal_test.go`, with the PostgreSQL
  and MySQL suites' `former_journal_test.go`.
- **The references at rest, sealed again (ADR 0180 step 5c-2).** Every
  reference is made under `presentLabels`: a data key's name, the subject
  index (`app:subject`, rebuilt from the records when a store opens), a
  hold's filing and its subject, a database's index rows (`indexKeyID`'s
  `app:fingerprint`: their fingerprint changed, so they are filed again when
  the store opens); the privacy service's own stores seal bound to their own
  ids, `app.privacy/…`, and read a box bound to `kit.privacy/…` after
  (`openMember`). What a former release sealed is moved by `renamed_refs.go`'s
  pass at every start, once the stores are open: the holds first, then each
  record a box of which names its person's former key or its own former one
  — written again (an `Update` that changes nothing), its former values and
  versions moved key by key (`moveFormer`, `moveVersions`), under the
  stripes of every key involved held shared (`sealer.shared`). The mark of
  the progress is what rests: a box names its key, a hold its filing. What
  it cannot reach — a queued message, a dead letter, the versions of a
  record in the trash (the SDK rewrites no deleted document's), a former
  value or version under the former key of a person the record no longer
  names — reads where it lies, by the key its box names, so the former keys
  stay, never written under. An erasure destroys both of a person's keys
  (`shredPeople`, `shredIfLast`, `lastOf` over both subjects and both keys,
  `sealer.alone` over several stripes, each once), and a record gone both
  of its own (`dropOwnKey`, `sealedRefs`); `moveOwn` moves from either
  person key. A hold is found under its record's present reference, then
  its former one (`holdOf`), placed and lifted replacing a former one
  (`dropFormerHold`). The module name `kit` and `kit.privacy` stay reserved:
  step 5a's move still meets them, and `kit.privacy`'s ids are read. Held by
  `renamed_refs_internal_test.go` (a pass refused at each of its writes,
  resumed at once and after a restart, ending where an uninterrupted one
  does; a hold filed twice listed once; an erasure during a move — the pass
  paused by the test hook `privacyRun.relabelOpened` between opening a
  former value and sealing it again, the erasure parked on the history's
  writer or on the stripes (`sealer.onWait`) — and one before the pass,
  leaving no key) and `connectors/sqlite`'s `testdata/former-refs` in
  `former_refs_test.go`, with the PostgreSQL and MySQL suites'
  `former_refs_test.go` (the fixture copied by `data copy`).
- **The core reads none of the tool's variables.** `KIT_DEV_*`,
  `KIT_STUDIO`, `KIT_STUDIO_REMOTE` and `KIT_ANALYZE` are read by
  `studioengine` through `plug.Studio` (`studioEnv`, `devForced`,
  `devBuildOf`, `studioSettings` in `config.go` and `build.go` fall back
  to a run in which none is set); without `framework/app/studio` the
  settings carry no row of them and the graph no dev build (ADR 0180 step 1).
- **What the Studio shows is redacted.** A payload or a log attribute goes
  through `redactor` (`redact.go`, the SDK's `redact`); an error in a log attribute is `describe(err)`'s text;
  kit's own log lines (`a.log`) never reach the ring — only `app.Log`'s
  (`a.productLog()`).
- **A port calls what the start bound.** One rule, the analyzer's too
  (`resolvePorts` on both sides): `app.Bind`, else the one `Service.Implement`
  among the mounted services, else the port's `app.Fallback`. The declarations
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
- **The journal seals under a month's key, and no erasure reaches it.**
  An entry's reason is sealed under `j:` and its month in UTC
  (`journalRef`), the month read on the app's clock, never the wall clock:
  twelve keys a year, whatever the number of entries. An entry sealed
  before under its own key (`r:`) names that key in its box and keeps
  opening; nothing rewrites it (`TestAnEntrySealedUnderItsOwnKeyStillOpens`).
  The journal names people by reference only, so a person's erasure
  destroys their own key and the keys of their records, never a month's
  (`TestAnErasureLeavesTheJournalsKey`); destroying a month's key is the
  journal's retention, which ADR 0178 defers.
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
- **What the product deletes from a store with revisions goes to its trash,
  and what privacy deletes never does** (ADR 0178 C4). `Store.Delete` trashes
  for `<store>-trash` — 30 days, 0 off —: gone from every read, unique keys
  free, versions kept, `Untrash` brings it back as it was; the person's data
  key stays while the trash holds their record (`versionsSealUnder` reads the
  trash too). A retention's deletion, `Records.Delete`, `DeleteOnErasure` and
  an erasure delete at once, as before; `app.Erase` purges the person's
  records from the trash first, each journaled as a deletion; a trashed
  record past its retention's instant is purged then. Nothing sweeps the
  trash: what is past due goes at start, the rest at its instant on the
  app's clock. A trash and its purge are not journaled — the product's
  deletion never was —; the key a purge destroys is (`shred`). Trashed
  records are not exported; the Studio lists the trash — keys and instants,
  never a value, a key that reads a personal, special or secret member
  hidden — and restores nothing (`trash_view.go`, ADR 0178 C4 amended on
  2026-10-15)
  (`TestAnErasureLeavesNothingInTheTrash`,
  `TestATrashedRecordKeepsItsPersonsKeyUntilItsPurge`,
  `TestATrashedRecordLeavesAtItsRetention`, `TestATrashOfZeroDeletesAtOnce`,
  `TestADeletionKeepsTheVersionsInTheTrash`, and the conformance suite's
  `trash` case on every engine).
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
  with the user the operation asks for: `app.Anyone()` (never for an
  operation that requires a user), `app.AnyUser()` (only for one that
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
  (`TestACommandIsATransaction`); `app.NoTransaction()` opts it out.
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
  context outside the transaction that carries the values of where the
  effect was made and the life of the outermost level, so a keyed command
  dispatched inside another's transaction, whose run and key end before the
  commit, does not hand its publishes and its workflow's `OnTransition`
  hooks a cancelled context; its lease's loss still cancels its own handler
  while it runs —, and a rollback drops it (`TestEffectsWaitForTheCommit`,
  `key_transaction_test.go`).
- **The writer turn is a write's outermost lock.** A transaction on the data
  directory or in memory holds it alone from its first write; a write of
  none shares it. It is taken before the hold lock, and by `Fire` and
  `Start` before the engine's entity lock; a wait is bounded by `turnWait`,
  never forever (`TestDataDirectoryTransactionsTakeTurns`). A cache
  (`InMemory` on a store) takes none, and is in no transaction.
- **A loop's transition is never decided on a rolled back write.** The
  engine's loop reads an entity to check its guard outside any transaction,
  before the writer turn, and a local transaction's writes land before its
  commit: `loopReads` remembers, per key, the count of writes a rollback
  wrote back of that entity (`undoneCounts`: per store, 64 slots of a key's
  FNV-1a hash, slot 0 for a write whose funnel named no key — `undoKeyed`
  marks it, `undoStep.key` keeps it, `txn.counted` counts it) as the read
  began, and the replacement that follows, once its transaction holds the
  turn, refuses it as `Unavailable` when another transaction's rollback of
  that entity moved it — the loop tries again on what the store holds. A
  savepoint of the transition's own transaction does not count (`txn.undid`);
  a read is forgotten by its replacement, a read that finds nothing, the
  entity's deletion (`forget`) and the workflow's stop
  (`TestALoopTransitionNeverWritesBackARolledBackWrite`,
  `TestALoopTransitionStandsOverAnotherEntitysRollback`,
  `TestALoopsReadsAreForgotten`, the storetest case `hooks-after-the-commit`).
- **A watch that hears its own stores never hears itself.** With
  `app.OwnStores()`, a watch is fed by its own module's stores too; its
  handler runs in a context marked with its watch (`handling`), and a funnel
  tells it of no write made in that context (`ownWrite`) — a write the
  handler causes later, elsewhere, is heard
  (`TestAWatchHearsItsOwnStoresNeverItsOwnWrites`).
- **Marks cost nothing without a watch.** A store's `feeds` are nil unless a
  running watch put itself on it; a write loads that pointer — one atomic
  load, `snapshot.Value.Load` — and nothing more
  (`TestAStoreFeedsAWatchOnlyWhileItRuns`,
  `TestMarksCostNothingWithoutAWatch`).

## Hooks for the runtime half

| Hook | Where | Contract |
|---|---|---|
| `func (l *Loop) Nudge()` | `loop.go` | a product's own wake — never the Studio's (D13); reason `manual`; skips a failure backoff |
| `mailbox` (`mails(limit)`, `mail(id)`) | `mailer.go` | implemented by `*Mailer`; newest first; SMTP keeps summaries only |
| `a.frameworkLoop(...)` | `loop.go` | registers a loop entry with provenance and library, filling only what `a.loop` left empty |

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
| `49`–`57` | the framework's own signals and refusals, typed at the move (`JOB_OVERLAPPED`, `HISTORY_MOVED`, `NOT_SOURCE`, `PASSWORD_MOVED`, `NOTHING_TO_ERASE`, `KEY_REKEYED`, `MIGRATE_REFUSED`, `CATALOGUE_INCOMPLETE` and `CATALOGUE_INVALID` — kit's English table, ADR 0179 step 8 —, `RETENTION_PANICKED`) |
| `58`–`59` | process profiles (`CodeSingletonHeld` in `app_profiles.go`, `CodeCLIFailed` in `cli.go`) |
| `60` | scopes (`CodeScopeUnknown` in `scope.go`) |
| `61`–`65` | sealing at rest (`CodeSealKey`, `CodeSealWrite`, `CodeSealOpen`, `CodeSealRewrap`, `CodeSealShred`) |
| `66` | transactions (`CodeTransactionSpan`: a transaction wrote a store of a second database) |
| `67`–`69` | revisions (`CodeRevisionRead`, `CodeRevisionWrite`: an erasure's, a hold's or a rollback's rewrite of the versions, `CodeRevisionDecode`: a version the type no longer decodes) |
| `70`–`75` | the signals kit ends one of its own steps with and catches itself, typed when `make guard` made rule 2 a gate — no caller receives one: `SEAL_ERASED` (`errErased`), `SEAL_KEY_MOVED` (`errNotCurrent`), `RESEAL_IN_PLACE` (`errReseal`), `RESEAL_MOVED` (`errRestMoved`), `WORKFLOW_NOT_IN_PLACE` (`errNotInPlace`), and `CodeTransactionPanic` with two reasons, `TRANSACTION_PANICKED` (`errTransactionPanicked`) and `TRANSACTION_HELD_PANICKED` (`errHeldPanicked`, `transact_sql.go`). `TestKitSignalsAreTyped` (`signals_internal_test.go`) pins each code and reason: `errs.New` answers a malformed declaration with a validation error, not a panic |
| `78` | the trash (`CodeTrash`: a store's trash cannot be read or emptied, `TRASH`) |
| `82` | the Studio's trash list (`CodeTrashList`, in `trash_view.go`: no such store, `TRASH_STORE_UNKNOWN`, a `NotFound`; a store without revisions, `TRASH_NOT_KEPT`, an `Invalid`) |
| `79`–`80` | data copy (`CodeDataCopy`: refused — no such database, data in memory, a product running, a table holding rows —, or done but a file queue not moved aside; `CodeDataCopyDiverged`: the fingerprints differ, the copy rolled back, in `datacopy.go`) |
| `81` | `watch replay` naming no watch (`CodeWatchReplay`, in `watchcmd.go`) |
| `83` | a subsystem not imported (`CodeSubsystemMissing`, in `subsystem.go`, ADR 0179 §6): the start's `DiagnosticsError` unwraps to it when its `Missing` names a package — reason `SUBSYSTEM_NOT_IMPORTED`, public "a declaration needs a subsystem this program does not import", the packages in its private text and its `packages` field — when a declaration's subsystem is not linked or a server lacks `framework/app/server` |
| `84` | an irreversible migration's Down (`CodeMigrationIrreversible`, in `migration.go`, ADR 0179 step 10): `Irreversible`, reason `MIGRATION_IRREVERSIBLE`, which `<product> migrate down` below its version stops at |
| `85` | an old runtime name met at the start (`CodeRenamed`, in `renamed.go`, ADR 0180 §3, Q12): a `KIT_<STEM>` variable of the operator's set, a field still tagged `kit:"…"` — the `DiagnosticsError` names every one and the name that replaces it in `Renamed`, and unwraps to it — reason `OLD_NAME`, public "a name the framework no longer reads was met at the start: use the name that replaces it", the pairs in its private text and its `renamed` field; for two minors, then the old names are forgotten |

## Test

`go test -race ./app/` — the product under test is `product_test.go` (a shop,
an audit service, and `members`: indexed accounts and sessions, the `session`
auth handler, a mailer, a generated secret, a declared loop and a
hand-written one; the shop's `quote` port falls back to its list price, and
the members implement it); `port_test.go` binds and refuses ports, `implement_test.go` implements them,
`replace_test.go` replaces endpoints and ports (`replace_internal_test.go`:
the refusal outside a test);
`start`/`startManual` run it in memory, on a free port, in dev, optionally on
a manual clock, with mail always captured (`<PRODUCT>_SMTP_URL` forced empty). A
test moves a manual clock once the loop it drives has armed its timer —
`armed` counts the app's waits: moved before, the clock puts the timer past
the instant it moves to. A wake the digest loop must hold for its backoff
comes during a run held at its gate, so that the loop arms one timer
(`loop_internal_test.go`: what a declared loop's wait arms); its entry is
read once it counts the run `waitWakes` saw start (`digestRan`).
A test that counts the loops' goroutines (`nocommand_test.go`'s `daemonOf`)
reads them once the daemon has settled (`daemonSettled`): every loop that
owns a goroutine shows one under its name, none running, runnable or on a
lock. `Start` returns once a loop's goroutine is started, not once it has run
and taken its label, so a count read right after it depended on the
scheduler — two of four on a Windows runner. Every loop's goroutine is
labelled with its loop's name: the mail outbox's was labelled with none and
a secret's rotation with the secret's id.
`mailer_test.go` brings an in-test SMTP relay and a refused port.
`mailer_bounce_test.go` brings a relay that answers a reply of its choice to
every RCPT, on a manual clock that moves only between attempts: a 550 given
up at the first attempt as rejected, its code in the mailbox and its words
only in the hook's `Reply`; a 451 retried until an attempt past
`mail-max-age` gives it up as expired; `mail-retry-permanent` retrying a 550
until it is exhausted; the framework's hook logging, a product's hook that
panics logged and the outbox going on; the settings' defaults, `MailMaxAge`,
and a negative age refused declared or configured.
`policy_test.go` declares an endpoint behind every policy and a query
limited per client: each of the nine settings defaulting to the code's value,
overridden by `config/<env>.json` and the variable with its provenance, the
graph drawing the values run; a key no policy declares refused, one per
setting; a value a policy cannot run with stopping the start, from the
configuration or the code; a policy twice refused; on a manual clock, the
429's `Retry-After` rounded up and a configured burst, `MaxWait` holding a
call for its token, a bulkhead's queue holding, refusing when full and when
the wait ran out, a configured timeout; a caller that leaves logged as 499,
no error. Every new test runs `-race -count=100`.
`module_test.go` declares the `reviews` module — two services, one named
like it, two stores, a setting, a generated secret, a port with a fallback
and a watch of `app.Moderated` —, which requires `ratings`: qualified names,
and what the graph says of the modules; `watch_test.go` runs its watch over
the shop's moderated item names and the ratings' notes — every funnel heard
once, a delete, nothing from its own module's reviews nor from an unmarked
store, a failed notice retried, the delivery in the write's trace —,
`watch_graph_test.go` says what the graph shows of it — its feeds, a
product's watch, a product with none, the refusals —,
`watch_internal_test.go` a store's watches while one runs and none without,
and `watch_own_test.go` the `atelier`, whose watch hears its own sketches
(`app.OwnStores`) and never its own stamps, a module's that hears its own,
and the refusal on a topic's subscription;
`records_keys_internal_test.go` runs `Records.Keys` and the catch-up on
files and the fake database (`keysEngines`), and on the same forum
`watchcmd_main_internal_test.go` runs `watch replay` through `Main` — lost
notices heard at the next start, two replays heard twice and journaled
twice, a name that is no watch refused with 1 and queuing nothing, a
malformed line 2 — and `watchcmd_internal_test.go` what it prints and the
`CodeWatchReplay` refusal;
`module_mount_test.go` mounts it beside the shop: routes under
a prefix and at `/`, a file's section, the variable, `app.Set`, the
secret's name in its store, the start and stop order, a mount's binding and
every refusal; `module_only_test.go` `app.Only` — a mount in the environments
it names and not in another, <PRODUCT>_ENV lower-cased and `app.Env` over it, the
mounts resolved again at a start in another environment, a dropped mount's
prefix and binding not given, its three refusals, and the graph's
`Module.Only` (`TestTheGraphSaysWhereAModuleIsMounted`);
`module_internal_test.go` the refusal of a foreign
declaration and of a product's password policy on a module's store — the
module's own taken —, and a module's source and a library's served from
their own Go module (`pkg/v1/clock`'s, a Go module of its own since ADR 0171);
`introspect_internal_test.go` the source endpoint's read — a regular file
within the bound, never a link at its name, nor one swapped in between the
look and the open —, and `introspect_unix_internal_test.go` a named pipe
swapped in, refused at once;
`module_database_test.go` a module on the fake
database — `app.Keeps(module)`, its migrations under `<module>_migrations`,
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
document tables, their versions and the trash's row numbered 0 — transactions serialised, each on a copy of the data,
savepoints copies of that copy; its failures quote the URL on purpose); the
engine modules run the same against PostgreSQL, MySQL and SQLite.
`transact_test.go` runs `app.Transact` on memory, files and the fake
database (`eachBackend`, a `till` declared afresh for each): commit,
rollback, a savepoint, a panic, a command's transaction and
`NoTransaction`, the held publish and mail, a mistake refused at the call,
one database, the writer turn, the span; `store_sql_test.go` a store on the
fake database — its index keys hashed, its history, a workflow's journal
there —, a workflow's hooks and a watch's notice after the commit;
`storetest_test.go` the conformance suite, `app/storetest` — revisions
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
`trash_internal_test.go` purges only the tombstone the agenda named; `trash_test.go` and `trash_versions_test.go` run the trash with the `folk`
on memory, files and the fake database, on a manual clock: a person's
erasure empties theirs, a trashed record keeps its person's key until its
purge, a retention's instant comes first, `<store>-trash` at 0, the
setting, an untrash rolled back keeping its purge, and the versions a deletion keeps — the one test that reads only
the API before the trash. The trash's contract itself is
`app/storetest`'s `trash` case. `trash_view_test.go` lists the trash as the
Studio does — the folk's keys in order, a page at a time, the `bin`'s passes
keyed by their subject hidden, and the refusals —, and
`trash_view_internal_test.go` the refusals' `CodeTrashList`.
`counter_product_test.go` is the product of the commands' and queries' tests:
the `staff`'s badge (auth data that is a `app.Principal`), the `counter`'s
orders — `place-order` allowed by a policy and exposed, `cancel-order`
authorized by a rule and keyed, `reindex` queued, the `my-orders` and
`order` queries, the `summaries` read model its projection keeps — and the `lab`, whose handlers a test holds, fails or
counts (`labGate`); `command_test.go`, `key_test.go`, `expose_test.go`,
`queued_test.go` (a manual clock drives the retries) and
`command_graph_test.go` run it — `key_transaction_test.go` declares the `desk`, whose transactional `decide` dispatches the keyed `issue`: its effects at the outer commit, not before, dropped by a rollback, eight decisions on one key, and a lost lease that still ends the keyed run; `expose_test.go` also declares the
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
moved, a key that reads a sealed member — and thirty-two writes learning
such members at once, none lost (`TestConcurrentLearnsKeepEveryMember`) —, a
record's own key, a plain member
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
  a store was declared over instantiated the whole generic chain beneath it —
  `StoreService`, the document and SQL engines, the SDK's `docstore.Store`
  and `SQLStore`, `historied`, `sealedEngine` —, and the test binary declares
  dozens: the kit_test compile reached 5 GB when a sealing store's documents
  were `sealedDoc[T]`, and the smallest product linked 0.9 MB of it (ADR
  0179). Since ADR 0179's step 2 the engines are one, over documents
  (`plug.DocumentValue`; since step 3 the SDK's document store is
  instantiated once, at storeengine's `docRecord`), and so are the retention, the trash
  and the privacy declaration; what stays generic is the typed facade —
  `StoreService[T]`'s exported methods and declarations, a few lines each —
  and the wrappers made once at a declaration (`entityOf`,
  `privacyFuncsOf`, an index's key function). A method of the store that
  names no `T` belongs on `storeCore`; an entity crosses it as an `any`
  holding a `T`, never a `*T` — `addressable` is the settable copy
  reflection changes.

- **An in-package test says so in its name**: a test file that reaches the
  package's unexported names is `package app` and ends in `_internal_test.go`;
  every other one is `package app_test`, which sees the package as a product
  does (41 and 65 files). In an external test the package is `app`, so a
  local variable never takes that name: the running app is `a` (or `prod`
  where `a` is taken), as ADR 0180 step 3 renamed every one.
