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
// Package kit — the architecture the graph draws: where each store keeps its
// data.
//
// Package kit — authentication: the handler that says who calls, and the user
// it names.
//
// Package kit — the build a graph describes: the modules and versions it was
// made of.
//
// Package kit — the catalog of the generic mechanics a node is built from.
//
// Package kit — the kit tag: the classes and words a field of personal data
// carries.
//
// Package kit — the plan of a Go type's classified members, computed once per
// type.
//
// Package kit — the walk of a value along its classification plan.
//
// Package kit — the warnings for fields whose name suggests personal data left
// unclassified.
//
// Package kit — CLI commands: a short command of the product that Main runs
// once, in the CLI profile, and whose status is the process's.
//
// Package kit — commands: an operation that changes something, with one
// handler.
//
// Package kit — the app's options and environments.
//
// Package kit — the config command: every setting, its value and where it
// comes from.
//
// Package kit — the consumer that drains a queue into its handler.
//
// Package kit — the daemon: what a running app knows of itself beyond its
// nodes.
//
// Package kit — the daemon's HTTP server as the Studio sees it: its loop,
// its counters, and the middleware every request goes through.
//
// Package kit — the daemon's loops: their states, the scheduler that fires
// the jobs, and the goroutine labels that name them.
//
// Package kit — databases: the SQL engines a store can be kept on.
//
// Package kit — the start's judgement of the declared databases.
//
// Package kit — the interfaces of databases: the engine a connector gives, and
// what kit asks of it.
//
// Package kit — the migrations a database runs, and their lock.
//
// Package kit — the placement of each store: which database keeps its data.
//
// Package kit — a database in a running app.
//
// Package kit — the databases as the graph shows them.
//
// Package kit — the decoding of a request into its typed value.
//
// Package kit — the dev tools' routes of the Studio, read-only.
//
// Package kit is the implementation of framework/kit, the framework in which
// every product is its own diagram. A product imports framework/kit, whose
// names alias and forward to this package's: here, the types carry the role
// their name says — StoreService, EndpointService, AppConfigurer — and the
// facade keeps the names a product writes — Store, Endpoint, AppOption.
//
// Package kit — endpoints: an HTTP route and the operation it serves.
//
// Package kit — the errors a product returns to its callers, and their wire
// mapping.
//
// Package kit — exposures: an operation an endpoint serves over HTTP.
//
// Package kit — what an exposure takes, and what it exposes.
//
// Package kit — the Go modules of the running binary, read from its build
// info.
//
// Package kit — the graph: the product as a diagram of what it mounts.
//
// Package kit — history: the former values of the fields that keep them.
//
// Package kit — the history's storage: former values kept beside each record.
//
// Package kit — reading a record's former values.
//
// Package kit — the history as the graph and the Studio show it.
//
// Package kit — what kit's own writes mean to the history.
//
// Package kit — legal holds: records kept whatever their retention says.
//
// Package kit — secondary indexes of a store.
//
// Package kit — the Studio's read API: the graph, the events, the sources.
//
// Package kit — what the source endpoint needs of the root it reads.
//
// Package kit — how the source endpoint opens a file on Unix.
//
// Package kit — how the source endpoint opens a file on Windows: the link
// itself, never its target.
//
// Package kit — jobs: scheduled work run by the daemon's loop.
//
// Package kit — the privacy journal: what kit did to personal data, and why.
//
// Package kit — idempotency keys and their leases.
//
// Package kit — the compile-time proof of which roles each declaration plays:
// the interfaces the app reaches its nodes and options through.
//
// Package kit — listeners: inbound ports that are not HTTP, a private socket
// speaking a versioned contract (ADR 0148).
//
// Package kit — the compile-time proof that a listener is a starter.
//
// Package kit — what a listener needs of the socket it accepts on.
//
// Package kit — the product's logger and the logs the Studio shows.
//
// Package kit — loops: work the daemon waits for, woken by time or by events.
//
// Package kit — mailers: a durable outbox and the transport that empties it.
//
// Package kit — what building an SMTP transport needs of its URL.
//
// Package kit — Main: the whole main function of a product.
//
// Package kit — the migrate command.
//
// Package kit — modules: a set of services a product mounts as one.
//
// Package kit — the start's checks of the modules an app mounts.
//
// Package kit — what a module keeps: its stores and its migrations.
//
// Package kit — the mount of a module: its prefix and its bindings.
//
// Package kit — nodes: what every declaration shares, and its source position.
//
// Package kit — the interfaces of nodes: what every declaration is, and the
// roles some play.
//
// Package kit — observation: the spans and events of a running product.
//
// Package kit — operations: a typed request answered by a typed response.
//
// Package kit — the password policy of a store's fields.
//
// Package kit — hashing and verifying passwords under their policy.
//
// Package kit — the warnings for endpoints that set passwords outside a
// policy.
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
// Package kit — a store's personal data as the graph shows it.
//
// Package kit — the keys that index personal data without revealing it.
//
// Package kit — the Studio's privacy pages: the journal, the holds, the
// register.
//
// Package kit — the privacy command.
//
// Package kit — what the privacy command prints.
//
// Package kit — `privacy seal`: a whole store sealed now.
//
// Package kit — the process sample: what the running process uses.
//
// Package kit — the Studio's profiler: the live heap and the goroutines,
// folded onto the graph.
//
// Package kit — queries: an operation that reads and changes nothing.
//
// Package kit — queued commands: a command run later, from its queue.
//
// Package kit — rate limits on an endpoint, per client.
//
// Package kit — records: the product's data as modules and the Studio reach
// it.
//
// Package kit — redaction: what a value shows once its secrets are hidden.
//
// Package kit — the register of processing: each store's line.
//
// Package kit — replacements: a test's substitute for an operation.
//
// Package kit — the HTTP request and response an endpoint can reach.
//
// Package kit — retention: how long a store keeps its personal data.
//
// Package kit — the retention of a store in a running app.
//
// Package kit — one pass of retention: what is due is erased or deleted.
//
// Package kit — record revisions: a store's previous versions of each record.
//
// Package kit — what changed between two versions, and a version written back.
//
// Package kit — where a store's versions are kept, engine by engine.
//
// Package kit — what the privacy rules ask of a record's versions.
//
// Package kit — a record's version written back.
//
// Package kit — what the Studio sees of a record's versions.
//
// Package kit — the revisions command: a version of a record written back.
//
// Package kit — binaries and their process roles: one executable, several
// Apps, and the versioned contracts between them (D22).
//
// Package kit — schemas: the JSON Schema of an operation's types.
//
// Package kit — scopes: the parts of a singleton's lock or of a socket's
// path that are only known at the start — the user, the executable, a
// configuration directory —, so one declaration keeps one process per user,
// per installed copy or per configuration.
//
// Package kit — the current user outside Windows: the process's UID, read
// from the kernel. os/user would ask libc through cgo — linking the program
// dynamically — or read /etc/passwd, for a number the kernel gives at once.
//
// Package kit — the current user on Windows: its SID, from os/user, which
// reads the process token there without cgo.
//
// Package kit — sealing at rest: members sealed where kit keeps them.
//
// Package kit — where the data keys are, against what they seal.
//
// Package kit — cryptographic erasure: a data key destroyed.
//
// Package kit — a document as it lies at rest, sealed member by member.
//
// Package kit — messages at rest: a queue's members sealed.
//
// Package kit — a store that seals: its records as they rest.
//
// Package kit — a store that seals, as kit's port sees it.
//
// Package kit — secrets: the values the product declares and the operator
// provides.
//
// Package kit — the secrets command.
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
// Package kit — the engines a store runs on: memory, files or a database.
//
// Package kit — stores on SQL: the SDK's document store over a database.
//
// Package kit — kit's own migrations on a database.
//
// Package kit — telemetry: the numbers a product reports, exported on a
// private socket when the deployment asks for them (ADR 0149).
//
// Package kit — topics and subscriptions: asynchronous messages of one type.
//
// Package kit — transactions: kit.Transact and its levels.
//
// Package kit — a transaction on a database, the database's own.
//
// Package kit — the data's writer turn, where there are no transactions.
//
// Package kit — a transaction writes one database: the dev warning.
//
// Package kit — watches: how a module hears the writes of a product's marked
// fields.
//
// Package kit — a watch in a running app: its queue and its consumer.
//
// Package kit — the sentences kit says, in every language it speaks.
//
// Package kit — workflows: a state machine over the entities of one store.
//
// Package kit — the ports the workflow engine reaches a store through.
package kit
