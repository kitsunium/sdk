<!-- updated: 2026-10-17T22:00:00Z -->
# framework/internal/app/plug — where kit's opt-in subsystems plug in

The hooks kit reads and a subsystem package sets as it is imported: the HTTP
engine, the frontends' file server and the process sampler (`serverengine`, set
by `framework/app/server`), the Studio — its routes, the tool's variables and
kit's catalogues after English (`studioengine`, set by `framework/app/studio`) —, the stores and the databases
(`storeengine`, set by `framework/app/store`), the queues (`queueengine`, set by
`framework/app/queue`), the workflows (`workflowengine`, set by
`framework/app/workflow`), the mailers (`mailengine`, set by
`framework/app/mail`), the jobs (`jobengine`, set by `framework/app/jobs`). kit
imports none of the SDK
packages behind them — `pkg/v1/net/server`, `net/static`, `net/sse`,
`profiling`, `proc/process` (and through it `runtime/metrics` and the spawn
engine, ADR 0172), `data/docstore`, `data/sql`, `data/queue`, `app/statemachine`, `app/mail/spool`, `app/scheduler` and `app/i18n` (ADR 0179) —, so a product
that does not import a subsystem neither links nor initialises them;
`framework/app`'s initgraph tests hold the graph to it.

It is also the contract between kit's core and the subsystems ADR 0179 takes
out of it, one file per subsystem (Q7), as `database/sql/driver` is between
`database/sql` and a driver. The store's (`store.go`, `store_interface.go`,
`store_refusal.go`) is written since step 1, used by the core's engines since
step 2, and implemented by `storeengine` since step 3: the core opens every
engine with a `StoreConfig` — a version's hold asked with the write's context
(`Held`), the write's stamp read from its context (`Stamp`), the core's
transaction an engine takes part in (`Transaction`), a call's context on a
database (`Call`) —, opens kit's scope on a database with a `DatabaseConfig`
and runs its migrations through it, and matches an engine's refusals by kind
(`Refusal`, `Refused`): the SDK's sentinels stay in `storeengine`. The queue's
(`queue.go`) is written and implemented by `queueengine` since step 4: the core
opens every queue with a `QueueConfig` — its place (a directory, a
`Database` the store subsystem opened, memory), its policy, the app's
`Clock`, a mailer's sealing — and holds a `Broker` it publishes to, consumes
(`ConsumeConfig`, `DeliveryValue`) and reads the dead letters of; a data copy
reads and writes it as it lies (`MessageMover`). The workflow's
(`workflow.go`, `workflow_interface.go`) is written and implemented by
`workflowengine` since step 5: the core declares every workflow with a
`WorkflowConfig` — its transitions (`TransitionSpec`), its hooks
(`EnterHookSpec`, `OnTransition`), its ports, the app's `Clock`, kit's
brackets — over `WorkflowEntityValue`s, the product's entity held as `any` and
its state spelled, so that the SDK's statemachine is instantiated once
(Q3); it holds a `WorkflowEngine`, implements the `WorkflowStore` and
`WorkflowJournal` ports the engine runs on, and tells the engine's failures
apart by kind (`WorkflowFailure`, through the driver's `Failed`). The
mail's (`mail.go`, `mail_interface.go`) is written and implemented by
`mailengine` since step 6: the core opens every mailer's outbox with a
`MailSpoolConfig` — kit's attempt (`Transport`, told a `MailAttemptValue`),
the app's `Clock`, what kit hears of each mail (`MailEventValue`, a dead
letter's `GivenUp` and the SMTP `Reply` read by `mailengine`), its
retries, its age and whether a permanent refusal is retried (`MaxAge`,
`RetryPermanent`), and where it keeps them: the queue subsystem's `Broker` on a
`Database`, a directory, or memory —, holds a `MailSpool`, hands its mail to
a `MailSink` the driver builds (SMTP through `ParseSMTP` and `NewSMTP`,
or `Capture`, a `MailCaptureSource`), asks the SDK's verdict on a mail
(`Validate`) and tells the spool's refusals apart by kind (`MailRefusal`,
through `Refused`); a mail crosses as a `MailValue`, the SDK's mail value
held as `any`, which the core names and plug does not. The jobs'
(`jobs.go`, `jobs_interface.go`) is written and implemented by `jobengine`
since step 7: the core reads every job's schedule at the start — never at
the declaration, which a package's init may run before the subsystem's —
from its period (`Every`) or its cron expression (`Cron`) as a
`JobScheduleFunc`, opens the app's scheduler with a `JobSchedulerConfig` —
the app's `Clock`, and what kit hears of each fire (`OnResult`, a
`JobResultValue`) —, and holds a `JobScheduler` it adds each job to
(`JobEntryValue`) and runs. kit's catalogues after English (`words.go`,
`words_interface.go`) are written and implemented by `studioengine` since step 8
(Q6): the core keeps English as its own table and, once `framework/app/studio`
set `Words`, opens the other languages once per hook value with
`WordsConfig.Open`, handing it that table — the language each falls back to
—, and holds a `Translator` it asks the languages (`Languages`), their keys
(`Keys`, which the core checks against English) and a sentence (`Render`).
The Studio's (`studio.go`, `studio_interface.go`) is written and
implemented by `studioengine` since ADR 0180 step 1: the core reads none of the
tool's variables — it asks `Env` what they ask of the Studio (a
`StudioEnvValue`, its zero value a run in which none is set), `Settings`
for their rows given what the run resolved (`StudioStateValue`), `Forced`
for what kit dev set and `DevBuild` for what it built — and holds none of
the Studio's routes: a serving app in dev hands `Mount` its mux and a
`StudioHost`, the app as the routes read it, which the core implements
(`studio_host.go`). The event stream and the profiler, behind hooks of
their own until then (`OpenEventStream`, `StudioProfiler`), are
`studioengine`'s own, under its routes.

## Contents

| File | Holds |
|---|---|
| `jobs.go` | the jobs subsystem's contract (ADR 0179 step 7): the hook `Jobs` (`JobDriverConfig`: `Every` and `Cron` — a job's schedule from its period or its cron expression —, `Open` the app's scheduler); `JobScheduleFunc`, `JobSchedulerConfig`, `JobEntryValue`, `JobResultValue` |
| `jobs_interface.go` | the jobs subsystem's interface: `JobScheduler` (`Add`, `Run`), which `jobengine` implements over the SDK's scheduler and the core's `idleScheduler` for an app that declares no job |
| `mail.go` | the mail subsystem's contract (ADR 0179 step 6): the hook `Mail` (`MailDriverConfig`: `Open` a mailer's outbox, `Validate` a mail, `Capture`, `ParseSMTP`, `NewSMTP` — the transports —, `Refused` — the refusal of a kind); the enums `MailRefusal`, `MailEvent` and `MailGiveUp` (why a dead letter was given up: exhausted, rejected, expired — issue #489) and their values; `MailSpoolConfig` (its `MaxAge` and `RetryPermanent`, the mailer's settings), `MailValue`, `MailRelayValue`, `MailAttemptValue`, `MailEventValue` (its `Reply`, the SMTP reply as a `*textproto.Error`, and `GivenUp`) |
| `mail_interface.go` | the mail subsystem's interfaces: `MailSpool` (`Send`, `SendWithID`, `DeadLetters`, `Run`, `Close`) and `MailSink` (`Send`) with its sibling `MailCaptureSource` (`Composed`: the capture transport's last message), which `mailengine` implements |
| `plug.go` | the hooks (`NewHTTPServer`, `NewStaticFiles`, `SampleProcess`), what they take and return (`HTTPConfig`, `HTTPState`, `HTTPServer`), the pprof labels kit and the profiler share (`LabelNode`, `LabelLoop`) |
| `queue.go` | the queue subsystem's contract (ADR 0179 step 4): the hook `Queues` (`QueueDriverConfig`: `Open` a queue, `TableMigration` its table on a database, `DoNotRetry` and `NotRetryable` — the mark of a delivery no retry helps); `QueueConfig` (`Name`, `Dir`, `Database`, `Table`, `Policy`, `Clock`, a mailer's `Seal`, `Unseal` and `MaxPayload`, a test's `Wrap`), `QueuePolicyValue`, `ConsumeConfig` (with a consumer's `DrainTimeout`, `SettleTimeout`, `AutoExtend` and the `RetryIn` reader of a handler's own delay, issue #483 part 3), `MessageValue`, `DeliveryValue`, `LiveMessageValue`, `DeadLetterValue` |
| `queue_interface.go` | the queue subsystem's interfaces: `Broker` (`Publish`, `Consume`, `DeadLetters`) and its siblings `MessageMover` (`Messages`, `PutMessage`, `PutDeadLetter`: what a data copy reads and writes as it lies) and `LaterPublisher` (`PublishAt`, `PublishOnce`: a message for later, once per key); `Clock`, `Timer`, `Ticker` — the app's clock in standard-library types, a test's manual clock included |
| `store.go` | the store subsystem's values (ADR 0179 §2, §3): the hook `Stores` (`StoreDriverConfig`: `Open` a store's engine, `Database` kit's scope on a database's pool, `TableMigration` the migration of a store's table or its versions', `FormerTables` and `RenameTables` the framework's tables still under a former name and their move — ADR 0180 step 5); `RenameSpec`, `RenameValue`; `DocumentValue` (the JSON as it rests and the entity decoded, `any`), `EntryValue`, `WriteMode` (`WriteUpsert`, `WriteInsert`, `WriteReplace`), `IndexSpec`, `StoreConfig`, `StampValue`, `DatabaseConfig`, `TableSpec`, `MigrationValue` and its `Step`, `VersionValue`, `VersionWeightValue`, `TombstoneValue` — named with the role suffixes ktn-linter asks for, as the SDK's core docstore names its own |
| `store_interface.go` | the store subsystem's interfaces: `Database` (the transactions, savepoints and held functions of `app.Transact` on one database) and its siblings `Checker`, `MigratorProvider` (`Migrator`, `Executor`); `Transaction`, kit's transaction as an engine takes part in it, which the core implements; the engine `StoreEngine` over documents and its siblings `Folder`, `Keyser`, `EntrySource`, `Reindexer`, `VersionKeeper`, `VersionWeighter`, `Trasher`, `DocumentSource` (`Documents`, `DocumentSink`: what a data copy reads and writes) |
| `store_refusal.go` | an engine's refusals in kit's terms: the kinds (`Refusal`: `RefusalNotFound` … `RefusalOutOfOrder`, `RefusalRenameTarget`), `RefusalError` (the kind and the SDK's error inside), `Refuse`, `Refused` (the kind anywhere in an error's tree, as `errors.Is` reads one) |
| `studio.go` | the Studio's contract (ADR 0180 step 1): the hook `Studio` (`StudioDriverConfig`: `Env`, `Settings`, `Forced`, `DevBuild` — the tool's variables — and `Mount`, the Studio's routes) — nil without `framework/app/studio`, and kit reads none of the tool's variables and serves no route of the Studio; `StudioEnvValue` (`Studio` — on, off or empty —, `Remote`, `NoAnalysis`), `StudioStateValue` (`StudioOption`, `AnalyzeOption`, `Studio`, `Remote`, `Analyze`) |
| `studio_interface.go` | the Studio's interface: `StudioHost`, the running app as the Studio's routes read it and answer through it — the graph, the phase, the clock, the live events, traces, logs, instances, items, a source file, where a function lies, the mailbox, the store tools' reads, the guard's facts, and kit's answers (`Reply`, `Forbid`, `Fail`, `NotFound`, `Invalid`, `ProfileUnreadable`, `Debug`) —, which the core implements (`studioHost`) |
| `words.go` | kit's catalogues after English (ADR 0179 step 8, Q6): the hook `Words` (`WordsConfig`: `Open` the catalogues against kit's English table) — nil without `framework/app/studio`, and kit speaks English alone |
| `words_interface.go` | the catalogues' interface: `Translator` (`Languages`, `Keys`, `Render`), which `studioengine` implements over the SDK's i18n |
| `workflow.go` | the workflow subsystem's contract (ADR 0179 step 5): the hook `Workflows` (`WorkflowDriverConfig`: `Open` a workflow's engine, `Failed` — the engine's failure of a kind); `WorkflowCreateEvent`; the enums `WorkflowTrigger`, `WorkflowWake`, `WorkflowLoopEvent`, `WorkflowFailure` and their values; `WorkflowConfig`, `TransitionSpec`, `EnterHookSpec`, `WorkflowEntityValue`, `WorkflowChangeValue`, `WorkflowFiringValue`, `WorkflowRecordValue`, `WorkflowStepValue`, `WorkflowLoopEventValue` |
| `workflow_interface.go` | the workflow subsystem's interfaces: `WorkflowEngine` (`Start`, `Fire`, `Changed`, `Deleted`, `Census`, `Records`, `Run`), which `workflowengine` implements; `WorkflowStore` and `WorkflowJournal`, the ports the engine runs on, which the core implements |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It imports the standard library and `framework/model` only: kit and every subsystem import it. `framework/app`'s `TestTheCoreImportsOnlyTheSubsystemsStillInIt` holds its direct imports to it.
- A hook is set once, by its subsystem's `Enable`; kit reads it where the subsystem is needed and says so when it is nil.
- A type here names no entity type and no SDK type — a workflow's entity crosses as `any`, its state as its spelling; a mail as a `MailValue`, the SDK's mail value held as `any` —: what a product declares on its own type is wrapped into a function of a `DocumentValue` at the declaration; an SDK value crosses as plug's own (`VersionValue`, `MigrationValue` on `Executor`, whose methods are `database/sql`'s). No value of the SDK crosses behind a pointer the core does not name: `HandleProvider`, which did for the mail spool while it was in the core, went with step 6, and a subsystem that needs another's SDK handle asks that subsystem's package, typed (`storeengine.SQLOf`, `queueengine.BrokerOf`). The app's clock crosses as `Clock`, whose methods are the standard library's.
- An engine's refusal reaches the core as a `RefusalError`; a new SDK sentinel the core must tell apart is a new kind, never an import of the SDK by the core.

## Verify

```sh
cd framework && GOWORK=off go vet ./internal/app/plug/ && GOWORK=off go test ./app/ -run 'TheCoreImportsOnly|TheSmallestProduct|ImportsThe'
```
