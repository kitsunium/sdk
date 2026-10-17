// Package app — who may call an operation: the principal, the rules and their
// checks.
//
// Package app — activities: what a daemon's idle stop waits for besides its
// connections, a notion of the product's own — sessions still open, work
// still queued.
//
// Package app — the App: the services a product mounts, served by one process.
//
// Package app — the static analysis a product can be given: the platform's
// analyzer, run in the background in dev (ADR 0147 §1).
//
// Package app — process profiles: what an app does with its process (ADR
// 0147 §5) — serve, run as a daemon, or run one CLI command — and the
// singleton lock.
//
// Package app — the architecture the graph draws: where each store keeps its
// data.
//
// Package app — authentication: the handler that says who calls, and the user
// it names.
//
// Package app — the build a graph describes: the modules and versions it was
// made of.
//
// Package app — the catalog of the generic mechanics a node is built from.
//
// Package app — the kit tag: the classes and words a field of personal data
// carries.
//
// Package app — the plan of a Go type's classified members, computed once per
// type.
//
// Package app — the walk of a value along its classification plan.
//
// Package app — the warnings for fields whose name suggests personal data left
// unclassified.
//
// Package app — CLI commands: a short command of the product that Main runs
// once, in the CLI profile, and whose status is the process's.
//
// Package app — commands: an operation that changes something, with one
// handler.
//
// Package app — the app's options and environments.
//
// Package app — the config command: every setting, its value and where it
// comes from.
//
// Package app — the consumer that drains a queue into its handler.
//
// Package app — the daemon: what a running app knows of itself beyond its
// nodes.
//
// Package app — the daemon's HTTP server as the Studio sees it: its loop,
// its counters, and the middleware every request goes through.
//
// Package app — the daemon's loops: their states, the scheduler that fires
// the jobs, and the goroutine labels that name them.
//
// Package app — databases: the SQL engines a store can be kept on.
//
// Package app — the start's judgement of the declared databases.
//
// Package app — the interfaces of databases: the engine a connector gives, and
// what kit asks of it.
//
// Package app — the migrations a database runs, and their lock.
//
// Package app — the placement of each store: which database keeps its data.
//
// Package app — a database in a running app.
//
// Package app — the databases as the graph shows them.
//
// Package app — the decoding of a request into its typed value.
//
// Package app — the dev tools' routes of the Studio, read-only.
//
// Package app is the implementation of framework/app, the framework in which
// every product is its own diagram. A product imports framework/app, whose
// names alias and forward to this package's: here, the types carry the role
// their name says — StoreService, EndpointService, AppConfigurer — and the
// facade keeps the names a product writes — Store, Endpoint, AppOption.
//
// Package app — endpoints: an HTTP route and the operation it serves.
//
// Package app — the errors a product returns to its callers, and their wire
// mapping.
//
// Package app — exposures: an operation an endpoint serves over HTTP.
//
// Package app — what an exposure takes, and what it exposes.
//
// Package app — the Go modules of the running binary, read from its build
// info.
//
// Package app — the graph: the product as a diagram of what it mounts.
//
// Package app — history: the former values of the fields that keep them.
//
// Package app — the history's storage: former values kept beside each record.
//
// Package app — reading a record's former values.
//
// Package app — the history as the graph and the Studio show it.
//
// Package app — what kit's own writes mean to the history.
//
// Package app — legal holds: records kept whatever their retention says.
//
// Package app — secondary indexes of a store.
//
// Package app — the Studio's read API: the graph, the events, the sources.
//
// Package app — what the source endpoint needs of the root it reads.
//
// Package app — how the source endpoint opens a file on Unix.
//
// Package app — how the source endpoint opens a file on Windows: the link
// itself, never its target.
//
// Package app — jobs: scheduled work run by the daemon's loop.
//
// Package app — the privacy journal: what kit did to personal data, and why.
//
// Package app — idempotency keys and their leases.
//
// Package app — the compile-time proof of which roles each declaration plays:
// the interfaces the app reaches its nodes and options through.
//
// Package app — listeners: inbound ports that are not HTTP, a private socket
// speaking a versioned contract (ADR 0148).
//
// Package app — the compile-time proof that a listener is a starter.
//
// Package app — what a listener needs of the socket it accepts on.
//
// Package app — the product's logger and the logs the Studio shows.
//
// Package app — loops: work the daemon waits for, woken by time or by events.
//
// Package app — mailers: a durable outbox and the transport that empties it.
//
// Package app — what building an SMTP transport needs of its URL.
//
// Package app — Main: the whole main function of a product.
//
// Package app — the migrate command.
//
// Package app — modules: a set of services a product mounts as one.
//
// Package app — the start's checks of the modules an app mounts.
//
// Package app — what a module keeps: its stores and its migrations.
//
// Package app — the mount of a module: its prefix and its bindings.
//
// Package app — nodes: what every declaration shares, and its source position.
//
// Package app — the interfaces of nodes: what every declaration is, and the
// roles some play.
//
// Package app — observation: the spans and events of a running product.
//
// Package app — operations: a typed request answered by a typed response.
//
// Package app — the password policy of a store's fields.
//
// Package app — hashing and verifying passwords under their policy.
//
// Package app — the warnings for endpoints that set passwords outside a
// policy.
//
// Package app — ports: an operation a service needs and does not implement.
//
// Package app — privacy: kit's own service for holds and the journal.
//
// Package app — erasure: a record's personal data removed as its retention
// would.
//
// Package app — export: a person's data as kit gives it back.
//
// Package app — a store's personal data as the graph shows it.
//
// Package app — the keys that index personal data without revealing it.
//
// Package app — the Studio's privacy pages: the journal, the holds, the
// register.
//
// Package app — the privacy command.
//
// Package app — what the privacy command prints.
//
// Package app — `privacy seal`: a whole store sealed now.
//
// Package app — the process sample: what the running process uses.
//
// Package app — the Studio's profiler: the live heap and the goroutines,
// folded onto the graph.
//
// Package app — queries: an operation that reads and changes nothing.
//
// Package app — queued commands: a command run later, from its queue.
//
// Package app — rate limits on an endpoint, per client.
//
// Package app — records: the product's data as modules and the Studio reach
// it.
//
// Package app — redaction: what a value shows once its secrets are hidden.
//
// Package app — the register of processing: each store's line.
//
// Package app — replacements: a test's substitute for an operation.
//
// Package app — the HTTP request and response an endpoint can reach.
//
// Package app — retention: how long a store keeps its personal data.
//
// Package app — the retention of a store in a running app.
//
// Package app — one pass of retention: what is due is erased or deleted.
//
// Package app — record revisions: a store's previous versions of each record.
//
// Package app — what changed between two versions, and a version written back.
//
// Package app — where a store's versions are kept, engine by engine.
//
// Package app — what the privacy rules ask of a record's versions.
//
// Package app — a record's version written back.
//
// Package app — what the Studio sees of a record's versions.
//
// Package app — the revisions command: a version of a record written back.
//
// Package app — binaries and their process roles: one executable, several
// Apps, and the versioned contracts between them (D22).
//
// Package app — schemas: the JSON Schema of an operation's types.
//
// Package app — scopes: the parts of a singleton's lock or of a socket's
// path that are only known at the start — the user, the executable, a
// configuration directory —, so one declaration keeps one process per user,
// per installed copy or per configuration.
//
// Package app — the current user outside Windows: the process's UID, read
// from the kernel. os/user would ask libc through cgo — linking the program
// dynamically — or read /etc/passwd, for a number the kernel gives at once.
//
// Package app — the current user on Windows: its SID, from os/user, which
// reads the process token there without cgo.
//
// Package app — sealing at rest: members sealed where kit keeps them.
//
// Package app — where the data keys are, against what they seal.
//
// Package app — cryptographic erasure: a data key destroyed.
//
// Package app — a document as it lies at rest, sealed member by member.
//
// Package app — messages at rest: a queue's members sealed.
//
// Package app — a store that seals: its records as they rest.
//
// Package app — a store that seals, as kit's port sees it.
//
// Package app — secrets: the values the product declares and the operator
// provides.
//
// Package app — the secrets command.
//
// Package app — where the secrets are kept, per environment.
//
// Package app — services: a bounded context and the building blocks it owns.
//
// Package app — settings: a value the environment gives a service.
//
// Package app — the interfaces of settings: the values a setting holds, and
// what kit reads of a declaration.
//
// Package app — frontends: static assets the product serves.
//
// Package app — stores: a typed, keyed collection of entities.
//
// Package app — the engines a store runs on: memory, files or a database.
//
// Package app — stores on SQL: the SDK's document store over a database.
//
// Package app — kit's own migrations on a database.
//
// Package app — telemetry: the numbers a product reports, exported on a
// private socket when the deployment asks for them (ADR 0149).
//
// Package app — topics and subscriptions: asynchronous messages of one type.
//
// Package app — transactions: app.Transact and its levels.
//
// Package app — a transaction on a database, the database's own.
//
// Package app — the data's writer turn, where there are no transactions.
//
// Package app — a transaction writes one database: the dev warning.
//
// Package app — watches: how a module hears the writes of a product's marked
// fields.
//
// Package app — a watch in a running app: its queue and its consumer.
//
// Package app — the sentences kit says: English from its own table, the
// languages after it from framework/app/studio's catalogues.
//
// Package app — workflows: a state machine over the entities of one store.
//
// Package app — the ports the workflow engine reaches a store through.
package app
