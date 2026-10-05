// Package kit — who may call an operation: the principal, the rules and their
// checks.
//
// Package kit — activities: what a daemon's idle stop waits for besides its
// connections, a notion of the product's own — sessions still open, work
// still queued.
//
// Package kit — the App: the services a product mounts, served by one process.
//
// Package kit — the static analysis a product can be given: the platform's
// analyzer, run in the background in dev (ADR 0147 §1).
//
// Package kit — process profiles: what an app does with its process (ADR
// 0147 §5) — serve, run as a daemon, or run one CLI command — and the
// singleton lock.
//
// Package kit — authentication: the handler that says who calls, and the user
// it names.
//
// Package kit — the catalog of the generic mechanics a node is built from.
//
// Package kit — CLI commands: a short command of the product that Main runs
// once, in the CLI profile, and whose status is the process's.
//
// Package kit — commands: an operation that changes something, with one
// handler.
//
// Package kit — the app's options and environments.
//
// Package kit — databases: the SQL engines a store can be kept on.
//
// Package kit — the interfaces of databases: the engine a connector gives, and
// what kit asks of it.
//
// Package kit — the decoding of a request into its typed value.
//
// Package kit is a framework in which every product is its own diagram.
//
// A product is made of services. A service declares its building blocks as
// package-level values, through its methods:
//
//	var Service = kit.NewService("todos", "The todo list.")
//
//	var Todos = Service.Store("todos", func(t Todo) string { return t.ID })
//
//	var Changes = Service.Topic[ChangeEvent]("changes")
//
//	var Lifecycle = Service.Workflow("lifecycle", Todos, func(t *Todo) *Status { return &t.Status }).
//		Initial(Open).
//		On("complete", Open, Done).
//		After("auto-archive", 2*time.Minute, Done, Archived).
//		OnTransition(announce)
//
//	var Complete = Service.Command("complete", complete).
//		Expose("POST /todos/{id}/complete", kit.Anyone())
//
//	func complete(ctx context.Context, in ByID) (Todo, error) {
//		return Lifecycle.Fire(ctx, in.ID, "complete")
//	}
//
// An [App] mounts services and runs them as one process:
//
//	var App = kit.NewApp("todo", todos.Service, activity.Service)
//
//	func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
//
// # The diagram
//
// The values a product declares are also its description. [App].Graph
// returns the product as a [model.Graph]: every node with the position of its
// declaration and of the function it runs, the edges that exist by
// construction, the edges the running product observed — each with its
// counters — and, in dev, the edges the static analysis found in the code,
// each with its call sites. In dev the app serves it, live, in the Studio at
// /_kit/.
//
// # Building blocks
//
//   - [Service].Command and [Service].Query: the product's operations — a
//     command changes something and runs in one transaction, a query reads.
//     Each is internal until it is exposed: the code that needs it runs it
//     with [Command].Dispatch or [Query].Ask, which is how one service
//     reaches another. [Command].Expose and [Query].Expose give it a route,
//     which says who may run it: the operation's permission
//     ([Command].Allow) or rule ([Command].Authorize), or a marker that it is
//     open on purpose — [Anyone], [AnyUser]. The start refuses an exposure
//     that says none.
//   - [Service].Endpoint: a typed HTTP endpoint, for what HTTP itself is
//     about — a webhook a third party calls. The request is decoded
//     strictly — path, query and header fields by tag, the rest from a JSON
//     body — validated from its `validate` tags, then run through the
//     endpoint's mechanics ([RateLimit], [RateLimitPerClient], [Timeout],
//     [Bulkhead]). [Endpoint].Call runs it in-process; a port calls its
//     implementation ([Service].Implement) so.
//   - [Service].Store: a typed, keyed collection, persisted atomically when
//     the app has a data directory, or in a table of the database that
//     keeps it.
//   - [Transact]: one transaction — what it writes commits together, what
//     it publishes, mails or dispatches leaves at the commit.
//   - [Service].Topic and [Service].Subscribe: asynchronous messages,
//     delivered at least once to every subscription, retried, dead-lettered.
//   - [Service].Workflow: a state machine over a store — event transitions
//     fired by code, timer and guard transitions fired by the daemon's own
//     loop, hooks on entry and after every transition.
//   - [Service].Every and [Service].Cron: scheduled jobs.
//   - [Service].Static: a frontend.
//   - [Service].Listen: an inbound port that is not HTTP — a private socket on
//     this machine speaking a versioned contract (pkg/v1/proc/ipc).
//   - [Service].CLI: a short command-line command, run once by [App].Main.
//
// The app, the composition root, says where the data lives: [Database]
// declares a database on an engine module the product's main imports —
// github.com/kitsunium/sdk/framework/connectors/postgres and its siblings,
// the only code that imports a driver — and [Keeps] which stores it keeps. A
// service never names a database: its handle is its store, which runs on
// the SDK's document store over SQL there, a table per store.
//
// Declaring never fails: a mistake is recorded with its position and
// [App].Start reports every one at once ([DiagnosticsError]).
//
// # Modules
//
// A module ([NewModule]) is a Go module's services, which a product mounts as
// one — [App].With at its defaults, [Mount] with a [Prefix] or a [Bind]. What
// it declares is qualified with its name: its services are
// "<module>.<service>", its settings and secrets "<module>.<name>", its
// routes served under its prefix; a module it [Requires] is mounted with it,
// and the modules start first.
//
// # Processes
//
// An app has a process profile ([Profile]): a server (the default) serves
// HTTP; a daemon serves no HTTP, answers on its listeners and may stop when
// idle ([IdleStop]) — no connection open and no [Service].Activity busy —; a
// CLI run opens what one command reads, runs it and exits. A command may be
// the app's default ([DefaultCommand]: every argument that names no other
// command is its) and never fail its shell ([FailSafe]). [Singleton] keeps
// one process of an app per machine, [SingletonPer] one per user, executable
// or configuration ([PerUID], [PerExecutable], [PerConfigDir], [PerEnv]), and
// [SocketPer] names a listener's socket the same way ([SocketPathFor] computes it
// for a client). [Stop] ends the run of the app a context runs in. A binary
// ([NewBinary]) holds process roles — an app each, selected by the leading
// arguments — which talk only through a declared contract ([Binary].Talks).
//
// # Running
//
// [App].Main understands its commands — serve, graph, healthcheck, config,
// secrets, migrate — and the environment: KIT_ENV (dev serves the Studio; production is the default),
// KIT_ADDR or PORT, KIT_DATA_DIR, KIT_ALLOWED_HOSTS, KIT_LOG_LEVEL. The
// Studio's pages are the kit tool's, not the product's: in dev a server
// serves the read-only API the Studio reads, and no route that acts on the
// product exists. The mechanics underneath — lifecycle, health, scheduler, queue, tracing,
// logging, validation, resilience, identifiers — are the kitsunium SDK's.
//
// Package kit — endpoints: an HTTP route and the operation it serves.
//
// Package kit — the errors a product returns to its callers, and their wire
// mapping.
//
// Package kit — exposures: an operation an endpoint serves over HTTP, and
// who may run it.
//
// Package kit — reading a record's former values.
//
// Package kit — legal holds: records kept whatever their retention says.
//
// Package kit — secondary indexes of a store.
//
// Package kit — jobs: scheduled work run by the daemon's loop.
//
// Package kit — listeners: inbound ports that are not HTTP, a private socket
// speaking a versioned contract (ADR 0148).
//
// Package kit — loops: work the daemon waits for, woken by time or by events.
//
// Package kit — mailers: a durable outbox and the transport that empties it.
//
// Package kit — modules: a set of services a product mounts as one.
//
// Package kit — the mount of a module: its prefix and its bindings.
//
// Package kit — operations: a typed request answered by a typed response.
//
// Package kit — the password policy of a store's fields.
//
// Package kit — ports: an operation a service needs and does not implement.
//
// Package kit — privacy: kit's own service for holds and the journal.
//
// Package kit — erasure: a record's personal data removed as its retention
// would.
//
// Package kit — export: a person's data as kit gives it back.
//
// Package kit — CPU profiles of the running process, for the Studio.
//
// Package kit — queries: an operation that reads and changes nothing.
//
// Package kit — rate limits on an endpoint, per client.
//
// Package kit — records: the product's data as modules and the Studio reach
// it.
//
// Package kit — replacements: a test's substitute for an operation.
//
// Package kit — the HTTP request and response an endpoint can reach.
//
// Package kit — retention: how long a store keeps its personal data.
//
// Package kit — revisions: a record keeps its versions, diffed and
// restored.
//
// Package kit — binaries and their process roles: one executable, several
// Apps, and the versioned contracts between them (D22).
//
// Package kit — scopes: the parts of a singleton's lock or of a socket's
// path that are only known at the start — the user, the executable, a
// configuration directory —, so one declaration keeps one process per user,
// per installed copy or per configuration.
//
// Package kit — secrets: the values the product declares and the operator
// provides.
//
// Package kit — where the secrets are kept, per environment.
//
// Package kit — services: a bounded context and the building blocks it owns.
//
// Package kit — settings: a value the environment gives a service.
//
// Package kit — the interfaces of settings: the values a setting holds, and
// what kit reads of a declaration.
//
// Package kit — frontends: static assets the product serves.
//
// Package kit — stores: a typed, keyed collection of entities.
//
// Package kit — telemetry: the numbers a product reports, exported on a
// private socket when the deployment asks for them (ADR 0149).
//
// Package kit — topics and subscriptions: asynchronous messages of one type.
//
// Package kit — transactions: what a unit of work writes commits together,
// and what it sends leaves at its commit.
//
// Package kit — watches: how a module hears the writes of a product's marked
// fields.
//
// Package kit — workflows: a state machine over the entities of one store.
package kit
