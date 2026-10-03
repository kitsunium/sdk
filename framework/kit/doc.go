//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /framework/kit .

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
package kit
