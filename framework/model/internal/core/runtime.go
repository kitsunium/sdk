// What a running product reports: its runtime state, loops, lifecycle
// components, live events, spans, traces and settings.

package core

import (
	"encoding/json"
	"time"
)

// Process phases, in the order a process goes through them.
const (
	PhaseStarting = "starting"
	PhaseServing  = "serving"
	PhaseDraining = "draining"
	PhaseStopped  = "stopped"
	PhaseFailed   = "failed"
)

// Database states.
const (
	// DatabaseUnset: no URL, in dev. Its stores stay in the data directory
	// and nothing is opened.
	DatabaseUnset = "unset"
	// DatabaseMemory: the app keeps its data in memory (kit.InMemory):
	// nothing is opened, no URL is read.
	DatabaseMemory = "memory"
	// DatabaseOpen: the pool is open, its migrations ran, it answered its
	// check at start.
	DatabaseOpen = "open"
	// DatabaseFailed: the start could not open it, migrate it or reach it.
	DatabaseFailed = "failed"
	// DatabaseClosed: not opened yet, or closed by the stop.
	DatabaseClosed = "closed"
)

// Where a setting's value comes from.
const (
	SettingEnv     = "env"     // an environment variable
	SettingOption  = "option"  // the product's code: an AppOption
	SettingDefault = "default" // kit's default for the environment
	SettingStore   = "store"   // the environment's secret store (a secret)
	SettingKitDev  = "kitdev"  // kit dev set the variable, for this launch
	SettingFile    = "file"    // a configuration file of the product (Detail)
)

// The settings kit reads, by the environment variable that sets each: kit's
// configuration reads them, the connectors and the architecture name them.
const (
	VarEnv          = "KIT_ENV"
	VarAddr         = "KIT_ADDR"
	VarDataDir      = "KIT_DATA_DIR"
	VarStudio       = "KIT_STUDIO"
	VarStudioRemote = "KIT_STUDIO_REMOTE"
	VarAnalyze      = "KIT_ANALYZE"
	VarTrustProxy   = "KIT_TRUST_PROXY"
	VarAllowedHosts = "KIT_ALLOWED_HOSTS"
	VarLogLevel     = "KIT_LOG_LEVEL"
	VarSMTPURL      = "KIT_SMTP_URL"
	// VarRetention sets what the stores' retention loops do: on, dry-run
	// or off (ADR 0006).
	VarRetention = "KIT_RETENTION"
	// VarDevForced is how kit dev tells the product which of the variables
	// above it set itself for this launch: their origin is kit dev.
	VarDevForced = "KIT_DEV_FORCED"
)

// Boot steps, in the order kit takes them.
const (
	BootConfig       = "config"       // the environment and the options, read
	BootDeclarations = "declarations" // what the services declare, checked
	BootMount        = "mount"        // the services bound to the app
	BootRoutes       = "routes"       // the routes of endpoints and frontends
	BootData         = "data"         // the data directory, opened
	BootHandler      = "handler"      // health, the Studio, protection, observation
	BootComponents   = "components"   // the lifecycle components, started in order
	BootServing      = "serving"      // ready: the phase is serving
)

// Component states.
const (
	ComponentPending  = "pending"
	ComponentStarting = "starting"
	ComponentUp       = "up"
	ComponentStopping = "stopping"
	ComponentDown     = "down"
	ComponentFailed   = "failed"
)

// Loop kinds.
const (
	LoopJob       = "job"       // a job, fired by the scheduler
	LoopTimer     = "timer"     // a workflow's own loop: its timers and guards, when due
	LoopConsumer  = "consumer"  // a subscription's consumer, a queued command's, or a mailer's outbox
	LoopHTTP      = "http"      // the HTTP server's accept loop
	LoopScheduler = "scheduler" // the scheduler's own loop, which fires the jobs
	LoopWake      = "wake"      // a declared loop's wake cycle (Service.Loop)
	LoopRoutine   = "goroutine" // a hand-written loop (Service.Go)
	LoopRotation  = "rotation"  // a generated secret's rotation, on its schedule
	LoopRetention = "retention" // a store's retention: erases and deletes what is due (ADR 0006)
)

// Loop provenances: who wrote the code that loops.
const (
	// ProvenanceLibrary loops are written by a library: net/http's accept
	// loop, the SDK scheduler, the SDK queue consumer.
	ProvenanceLibrary = "library"
	// ProvenanceKit loops are written by kit for a declaration: a workflow's
	// sweep, a declared loop's wake cycle.
	ProvenanceKit = "kit"
	// ProvenanceProduct loops are written by hand in the product: Service.Go.
	ProvenanceProduct = "product"
)

// Loop states.
const (
	LoopWaiting    = "waiting"    // blocked until its next wake
	LoopRunning    = "running"    // doing a run
	LoopRestarting = "restarting" // failed, waiting out its backoff
	LoopStopped    = "stopped"
)

// Span operations.
const (
	OpRequest     = "request"      // an HTTP request served by an endpoint
	OpCall        = "call"         // an in-process call of an endpoint, or of a port
	OpRead        = "read"         // a store read
	OpWrite       = "write"        // a store write
	OpPublish     = "publish"      // a topic publish
	OpDeliver     = "deliver"      // a subscription handling one message
	OpTransition  = "transition"   // a workflow transition
	OpRun         = "run"          // a job run, a loop run
	OpAuth        = "auth"         // the auth handler, in front of an endpoint
	OpSend        = "send"         // a mail put in the outbox
	OpDeliverMail = "deliver-mail" // the outbox handing one mail to the transport
	OpSecret      = "secret"       // a secret's value or keys used: Value, Seal, Open, Sign, Verify
	OpDispatch    = "dispatch"     // a command dispatched: its run, or its queuing for a queued one
	OpAsk         = "ask"          // a query asked
	OpHandle      = "handle"       // a queued command handled by its consumer, in the dispatcher's trace
	OpConnect     = "connect"      // a connection a listener accepted, handled until it closes
	OpCLI         = "cli"          // a short command-line command, run once
	// OpTransaction is a unit of work (kit.Transact, a command's, a
	// workflow's transition): its attrs say the database it belongs to —
	// "database", and "backend" as a store says it —, its "outcome", the
	// effects it held ("effects"), and "savepoint" for one nested in
	// another.
	OpTransaction = "transaction"
)

// The outcomes of a transaction, as its span's "outcome" attribute says them.
const (
	// OutcomeCommit is a transaction that committed: its effects left.
	OutcomeCommit = "commit"
	// OutcomeRollback is a transaction whose writes were undone: its effects
	// were dropped.
	OutcomeRollback = "rollback"
)

// Span statuses.
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// RuntimeMessage is what a running process says about itself: the daemon's own state
// and its internal loop.
type RuntimeMessage struct {
	// Phase is one of the Phase constants.
	Phase string `json:"phase"`
	// StartedAt is when the process started.
	StartedAt time.Time `json:"startedAt"`
	// Components are the lifecycle components, in start order.
	Components []ComponentMessage `json:"components"`
	// Loops are the recurring pieces of the internal loop.
	Loops []LoopMessage `json:"loops"`
	// History lists the phases the process went through, oldest first: the
	// daemon's own state machine, as it ran.
	History []PhaseChangeEvent `json:"history,omitempty"`
	// Process is the Go process, sampled when the graph was built.
	Process *ProcessSpec `json:"process,omitempty"`
	// HTTP is the product's HTTP server.
	HTTP *HTTPServer `json:"http,omitempty"`
	// Mocks are the replacements a test gave the app (kit.Replace).
	Mocks []MockMessage `json:"mocks,omitempty"`
	// Dev describes the build `kit dev` made of this process, when it did.
	Dev *DevBuildMessage `json:"dev,omitempty"`
	// Config is how the product was configured when it started: every
	// setting kit read, the value it uses — never a secret's — and where
	// that value came from.
	Config []SettingMessage `json:"config,omitempty"`
	// Boot is what kit did to start the product, step by step, as it ran;
	// the lifecycle components it then started are Components.
	Boot []BootStepMessage `json:"boot,omitempty"`
	// Databases are the databases the app declares, as this run found them:
	// each one's state, readiness, pool and migrations.
	Databases []DatabaseMessage `json:"databases,omitempty"`
}

// DatabaseMessage is one database the app declares (kit.Database), as the running
// process found it. Its URL is never shown: where it was found, the engine,
// the address and the TLS mode are.
type DatabaseMessage struct {
	// Name is the database's, as the app declares it.
	Name string `json:"name"`
	// Engine is the engine it runs on: "postgres", "mysql", "sqlite".
	Engine string `json:"engine"`
	// State is one of the Database constants.
	State string `json:"state"`
	// Ready says whether it answered its last check; CheckedAt says when.
	Ready     bool       `json:"ready"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	// Problem is the public text of its last failure: a start that failed,
	// a check it did not answer.
	Problem string `json:"problem,omitempty"`
	// URLFrom is where its URL was found — a Setting constant: env, store,
	// or default for a SQLite file beside the data —, empty when it is set
	// nowhere.
	URLFrom string `json:"urlFrom,omitempty"`
	// TLS is the TLS mode its URL writes: "verify-full"; "none" when it is
	// written off; empty when the URL leaves it to the driver, or when the
	// database is a file.
	TLS string `json:"tls,omitempty"`
	// Pool is database/sql's own count of its connections, sampled when the
	// graph was built.
	Pool *Pool `json:"pool,omitempty"`
	// Migrations are its sets of migrations, kit's first, as the start —
	// or the last sample — found them.
	Migrations []MigrationSetMessage `json:"migrations,omitempty"`
}

// Pool is a database's connection pool, as database/sql counts it.
// It is sampled when the runtime state is read; its counters never move a
// revision.
type Pool struct {
	// MaxOpen is its ceiling.
	MaxOpen int `json:"maxOpen"`
	// Open is how many connections are open: InUse and Idle.
	Open  int `json:"open"`
	InUse int `json:"inUse"`
	Idle  int `json:"idle"`
	// Waits counts the calls that waited for a connection, and WaitMs how
	// long they waited in all.
	Waits  int64   `json:"waits"`
	WaitMs float64 `json:"waitMs"`
	// Closed counts the connections closed for being idle too many, idle
	// too long, or too old.
	Closed int64 `json:"closed,omitempty"`
}

// MigrationSetMessage is one set of migrations on a database — kit's own, the
// product's (kit.Migrations), a module's — with its own version table, and
// so its own lock.
type MigrationSetMessage struct {
	// Name is the set's origin: "kit", "product", or a module's name.
	Name string `json:"name"`
	// Table is its version table: "kit_migrations", "schema_migrations".
	Table string `json:"table"`
	// Applied are the migrations the database records, and Pending those
	// this build carries that it does not, oldest first.
	Applied []MigrationMessage `json:"applied,omitempty"`
	Pending []MigrationMessage `json:"pending,omitempty"`
	// Problem is why the set could not be read or applied.
	Problem string `json:"problem,omitempty"`
}

// MigrationMessage is one versioned migration.
// Its Version orders it among the others; Name says what it does.
type MigrationMessage struct {
	// Version orders it: a UTC timestamp by convention, 20260910143000.
	Version uint64 `json:"version"`
	// Name says what it does; empty for a version this build does not carry.
	Name string `json:"name,omitempty"`
}

// SettingMessage is one configuration value kit read when the product started:
// kit's own, or one a service of the product declares.
type SettingMessage struct {
	// Name is the environment variable that sets it: "KIT_ADDR",
	// "TODO_BASE_URL".
	Name string `json:"name"`
	// Value is what kit uses. A secret's is left out: Secret says so.
	Value string `json:"value,omitempty"`
	// From is one of the Setting constants.
	From string `json:"from"`
	// Service declared it; empty for kit's own.
	Service string `json:"service,omitempty"`
	// Key is a declared setting's name, its key in a configuration file:
	// "base-url". A module's is qualified with the module's name,
	// "moderation.tdb-url": kit.Set's key, and in a file "tdb-url" under
	// "moderation:" (ADR 0008).
	Key string `json:"key,omitempty"`
	// Type is what a declared setting holds: text, bool, int, number,
	// duration or list.
	Type string `json:"type,omitempty"`
	// Detail is the configuration file that gave the value, when From is
	// SettingFile: "config/production.yaml".
	Detail string `json:"detail,omitempty"`
	// Secret: the value is withheld.
	Secret bool `json:"secret,omitempty"`
	// Option is the AppOption that sets it in code, when there is one:
	// "kit.Listen". Code wins over the environment.
	Option string `json:"option,omitempty"`
	// Flag is the kit dev flag that sets it in dev, when there is one:
	// "-addr".
	Flag string `json:"flag,omitempty"`
	// Database is the database the setting configures, when kit declares it
	// for one (kit.Database): its URL, its pool, its timeouts.
	Database string `json:"database,omitempty"`
}

// BootStepMessage is one step of the start, as it ran.
// It carries when it began, how long it took and, when it failed, its error.
type BootStepMessage struct {
	// Name is one of the Boot constants.
	Name string `json:"name"`
	// Count is how many it went through: settings, warnings, services,
	// routes, components.
	Count int `json:"count,omitempty"`
	// Value is what it used: the data directory, in dev.
	Value string `json:"value,omitempty"`
	// Begun is when it began.
	Begun time.Time `json:"begun"`
	// TookMs is how long it took.
	TookMs float64 `json:"tookMs"`
	// Error is the public message of a failure.
	Error string `json:"error,omitempty"`
}

// DevBuildMessage is one build of `kit dev`: when, how long, and why.
// It lets the Studio say what changed and how long the reload took.
type DevBuildMessage struct {
	// BuiltAt is when the build finished.
	BuiltAt *time.Time `json:"builtAt,omitempty"`
	// BuildMs is how long it took.
	BuildMs float64 `json:"buildMs,omitempty"`
	// Rebuilds counts the builds of this kit dev session, this one
	// included: 1 is the first start.
	Rebuilds int `json:"rebuilds,omitempty"`
	// Changed are the files whose change triggered this build, relative to
	// the module root, at most 20.
	Changed []string `json:"changed,omitempty"`
	// More counts the changed files beyond Changed.
	More int `json:"more,omitempty"`
}

// PhaseChangeEvent is one step of the daemon's life.
// It carries the phase entered, when it was entered, and why.
type PhaseChangeEvent struct {
	// Phase is the phase entered: one of the Phase constants.
	Phase string `json:"phase"`
	// At is when.
	At time.Time `json:"at"`
	// Reason says why, when it is not the natural next step: "SIGTERM",
	// "a component failed".
	Reason string `json:"reason,omitempty"`
}

// ProcessSpec is the Go process a product runs in.
// It carries the runtime's figures — goroutines, heap, GC, CPU — taken at At.
type ProcessSpec struct {
	// PID is the process ID.
	PID int `json:"pid"`
	// Go is the Go version the binary was built with.
	Go string `json:"go"`
	// OS and Arch are GOOS and GOARCH.
	OS   string `json:"os"`
	Arch string `json:"arch"`
	// MaxProcs is GOMAXPROCS; CPUs the logical CPUs the process sees.
	MaxProcs int `json:"maxProcs"`
	CPUs     int `json:"cpus"`
	// Goroutines is how many goroutines exist.
	Goroutines int `json:"goroutines"`
	// HeapBytes is the live heap; HeapObjects its object count.
	HeapBytes   uint64 `json:"heapBytes"`
	HeapObjects uint64 `json:"heapObjects"`
	// HeapGoalBytes is the heap size at which the next collection starts.
	HeapGoalBytes uint64 `json:"heapGoalBytes,omitempty"`
	// MemoryBytes is all the memory the Go runtime holds from the system:
	// the heap, the stacks, the runtime's own structures.
	MemoryBytes uint64 `json:"memoryBytes,omitempty"`
	// TotalAllocBytes is every byte ever allocated on the heap.
	TotalAllocBytes uint64 `json:"totalAllocBytes"`
	// GCCycles counts completed garbage collections.
	GCCycles uint64 `json:"gcCycles"`
	// LastGC is when the last collection ended; absent before the first.
	LastGC *time.Time `json:"lastGC,omitempty"`
	// GCPauseP99Ms is the 99th percentile stop-the-world pause.
	GCPauseP99Ms float64 `json:"gcPauseP99Ms"`
	// SchedLatencyP99Ms is the 99th percentile time a goroutine waited to run.
	SchedLatencyP99Ms float64 `json:"schedLatencyP99Ms"`
	// CPUSeconds is the CPU time the process consumed, user and system.
	CPUSeconds float64 `json:"cpuSeconds"`
	// UptimeMs is how long the process has run.
	UptimeMs float64 `json:"uptimeMs"`
	// At is when the sample was taken.
	At time.Time `json:"at"`
}

// HTTPServer is the product's HTTP server, and whose loop serves it.
// Its address is the one it listens on; its loop is the node that runs it.
type HTTPServer struct {
	// Library is the implementation: "sdk/v1/net/server" — the SDK's engine
	// accepting and draining the connections, net/http speaking the protocol.
	Library string `json:"library"`
	// Loop says how it serves: one accept loop, one goroutine per
	// connection, written by the library rather than the product.
	Loop string `json:"loop"`
	// Addr is the listening address.
	Addr string `json:"addr,omitempty"`
	// Middleware is the chain every request goes through before its route,
	// outermost first.
	Middleware []MechanicMessage `json:"middleware,omitempty"`
	// Timeouts are the server's timeouts, as Go durations: "readHeader",
	// "read", "write", "idle".
	Timeouts map[string]string `json:"timeouts,omitempty"`
	// Conns counts connections: "active" now, "total" ever accepted,
	// "rejected" by the listener's ceiling. Older graphs carry "new", "idle"
	// and "hijacked" too.
	Conns map[string]int64 `json:"conns,omitempty"`
	// InFlight is how many requests are being served.
	InFlight int64 `json:"inFlight"`
	// Served counts the requests served since start.
	Served int64 `json:"served"`
}

// ComponentMessage is one lifecycle component: brought up in order, taken down in
// reverse.
type ComponentMessage struct {
	// Name is the lifecycle name: "store:todos/store/todos", "http"…
	Name string `json:"name"`
	// Node is the node the component belongs to, when there is one.
	Node string `json:"node,omitempty"`
	// State is one of the Component constants.
	State string `json:"state"`
	// Begun is when the last start or stop call began.
	Begun *time.Time `json:"begun,omitempty"`
	// TookMs is how long that call took.
	TookMs float64 `json:"tookMs,omitempty"`
	// Error is the public message of a failure.
	Error string `json:"error,omitempty"`
}

// LoopMessage is one recurring piece of the daemon's internal loop: a job, a
// workflow's timer sweep, a subscription's consumer, the HTTP accept loop, a
// declared or hand-written loop.
type LoopMessage struct {
	// Name identifies the loop.
	Name string `json:"name"`
	// Node is the node the loop drives.
	Node string `json:"node,omitempty"`
	// Kind is one of the Loop constants.
	Kind string `json:"kind"`
	// Provenance is one of the Provenance constants.
	Provenance string `json:"provenance,omitempty"`
	// Library names the code that loops, for a library or kit loop:
	// "net/http", "sdk/v1/app/scheduler", "sdk/v1/data/queue",
	// "sdk/v1/app/mail/spool", "sdk/v1/app/statemachine", "kit".
	Library string `json:"library,omitempty"`
	// State is one of the Loop state constants.
	State string `json:"state,omitempty"`
	// LastWake is why the last run started: "interval", "topic",
	// "deadline", "manual", "start".
	LastWake string `json:"lastWake,omitempty"`
	// Restarts counts supervised restarts, for a hand-written loop.
	Restarts int64 `json:"restarts,omitempty"`
	// Schedule is human text: "every 10s", "*/5 * * * *", "on publish · poll 5s".
	Schedule string `json:"schedule"`
	// Runs counts completed runs.
	Runs int64 `json:"runs"`
	// Errors counts failed runs.
	Errors int64 `json:"errors"`
	// Missed counts due instants dropped because they had already passed.
	Missed int64 `json:"missed"`
	// Skipped counts fires skipped because the previous run was still going.
	Skipped int64 `json:"skipped"`
	// LastRun is when the last run started.
	LastRun *time.Time `json:"lastRun,omitempty"`
	// LastMs is how long the last run took.
	LastMs float64 `json:"lastMs,omitempty"`
	// NextRun is when the next run is due, when it is known.
	NextRun *time.Time `json:"nextRun,omitempty"`
	// LastError is the public message of the last failure.
	LastError string `json:"lastError,omitempty"`
}

// Event is one live event. Exactly one of the payload fields matching Type is
// set.
type Event struct {
	// Seq increases strictly on one stream; a reader drops what it has seen.
	Seq uint64 `json:"seq"`
	// Type selects the payload.
	Type EventType `json:"type"`
	// Time is when it happened.
	Time time.Time `json:"time"`
	// Revision is the graph revision, on hello and graph events.
	Revision string `json:"revision,omitempty"`
	// StartedAt is when the process's current run started, on hello: a new
	// value means the process restarted, and what the reader knew of the
	// previous run is stale.
	StartedAt *time.Time `json:"startedAt,omitempty"`
	// Phase is the process phase, on hello and lifecycle events.
	Phase string `json:"phase,omitempty"`

	Span       *SpanMessage        `json:"span,omitempty"`
	Transition *TransitionEvent    `json:"transition,omitempty"`
	Lifecycle  *ComponentMessage   `json:"lifecycle,omitempty"`
	Loop       *LoopMessage        `json:"loop,omitempty"`
	Census     *CensusMessage      `json:"census,omitempty"`
	Log        *LogRecordMessage   `json:"log,omitempty"`
	Mail       *MailSummaryMessage `json:"mail,omitempty"`
}

// SpanMessage is a finished unit of work, attributed to a node. Only allow-listed,
// wire-safe fields travel: no URL, no query string, no private error text.
type SpanMessage struct {
	// TraceID groups the spans of one causal chain (W3C, hex).
	TraceID string `json:"traceId"`
	// SpanID identifies the span (W3C, hex).
	SpanID string `json:"spanId"`
	// ParentID is the parent span, absent on a root.
	ParentID string `json:"parentId,omitempty"`
	// Node is where the work happened.
	Node string `json:"node"`
	// From is the node that caused it: the edge this span travelled.
	From string `json:"from,omitempty"`
	// Edge is the kind of the edge From → Node.
	Edge EdgeKind `json:"edge,omitempty"`
	// Label refines Edge, like the edge's own label.
	Label string `json:"label,omitempty"`
	// Op is one of the Op constants.
	Op string `json:"op"`
	// Name is a low-cardinality operation name: a route pattern, "Get".
	Name string `json:"name"`
	// Start is when the work began.
	Start time.Time `json:"start"`
	// Ms is how long it took.
	Ms float64 `json:"ms"`
	// Status is StatusError when the product failed — what kit answers with
	// a 5xx — and StatusOK otherwise, a refusal the caller has to fix (not
	// found, invalid, conflict, rate limited…) included.
	Status string `json:"status"`
	// Code is the error code, when the work ended in one: a failure's, or a
	// refusal's ("not_found").
	Code string `json:"code,omitempty"`
	// Error is the wire-safe public message of a failure; a refusal has none.
	Error string `json:"error,omitempty"`
	// Attrs are allow-listed attributes: "http.status", "instance"…
	Attrs map[string]string `json:"attrs,omitempty"`
	// User is the authenticated caller's ID, when there is one.
	User string `json:"user,omitempty"`
	// Payload is what went in and out, in dev only, with secrets redacted.
	Payload *Payload `json:"payload,omitempty"`
}

// Payload is the request and the response of a span, as JSON, in dev only.
// Every member whose name says it is a secret — password, token, secret,
// authorization, cookie… — or whose Go field is tagged kit:"secret" is
// replaced by "[redacted]", and each side is cut at 8 KiB.
type Payload struct {
	// Request is the decoded request, or the message delivered.
	Request json.RawMessage `json:"request,omitempty"`
	// Response is the response.
	Response json.RawMessage `json:"response,omitempty"`
	// Truncated is set when a side was cut.
	Truncated bool `json:"truncated,omitempty"`
}

// TransitionEvent is one workflow instance moving between states.
// It names the workflow, the entity, the event and the states it went from
// and to.
type TransitionEvent struct {
	// Workflow is the workflow node ID.
	Workflow string `json:"workflow"`
	// Instance is the entity key.
	Instance string `json:"instance"`
	// Event is the transition's event name.
	Event string `json:"event"`
	// From is the state left; empty on creation.
	From string `json:"from"`
	// To is the state entered.
	To string `json:"to"`
	// Trigger is one of the Trigger constants.
	Trigger string `json:"trigger"`
	// Caller is the node that fired it, when code did.
	Caller string `json:"caller,omitempty"`
}

// CensusMessage is a workflow's population per state.
// It counts the instances in each state when the census was taken.
type CensusMessage struct {
	// Workflow is the workflow node ID.
	Workflow string `json:"workflow"`
	// Counts maps each state to its number of instances.
	Counts map[string]int `json:"counts"`
}

// TraceMessage is the spans sharing one trace ID, root first.
// The root span is the one with no parent in the trace; the others follow it.
type TraceMessage struct {
	// TraceID identifies the trace.
	TraceID string `json:"traceId"`
	// Root is the node of the root span.
	Root string `json:"root"`
	// Name is the root span's name.
	Name string `json:"name"`
	// Start is when the root began.
	Start time.Time `json:"start"`
	// Ms spans from the first start to the last end.
	Ms float64 `json:"ms"`
	// Status is StatusError when any span failed.
	Status string `json:"status"`
	// Spans are in start order.
	Spans []SpanMessage `json:"spans"`
}

// SnippetMessage is source code served to a reader of the graph.
// Focus and FocusEnd mark the lines a reader asked for, inside the lines
// served.
type SnippetMessage struct {
	// File is relative to App.Root.
	File string `json:"file"`
	// StartLine is the 1-based line of Lines[0].
	StartLine int `json:"startLine"`
	// Focus is the first line of the range asked for.
	Focus int `json:"focus"`
	// FocusEnd is the last line of the range asked for.
	FocusEnd int `json:"focusEnd,omitempty"`
	// Lines are the file's lines around the focus, without terminators.
	Lines []string `json:"lines"`
	// Language is the highlighter language: "go", "javascript"…
	Language string `json:"language"`
}

// InstanceMessage is one entity's journey through a workflow.
// It carries the entity's key, its current state and the steps that brought
// it there.
type InstanceMessage struct {
	// ID is the entity key.
	ID string `json:"id"`
	// State is the current state.
	State string `json:"state"`
	// EnteredAt is when the instance entered its current state.
	EnteredAt time.Time `json:"enteredAt"`
	// History lists the transitions, oldest first, capped.
	History []StepEvent `json:"history,omitempty"`
}

// StepEvent is one transition in an instance's history.
// It carries the event, the states it went from and to, and when it fired.
type StepEvent struct {
	// Event is the transition's event name.
	Event string `json:"event"`
	// From is the state left.
	From string `json:"from"`
	// To is the state entered.
	To string `json:"to"`
	// At is when it happened.
	At time.Time `json:"at"`
	// Trigger is one of the Trigger constants.
	Trigger string `json:"trigger"`
	// Caller is the node that fired it, when code did.
	Caller string `json:"caller,omitempty"`
}

// LogRecordMessage is one log record written by product code through kit.Log, kept
// in dev so the Studio can show a request's logs beside its spans.
type LogRecordMessage struct {
	// Seq orders records; it only increases.
	Seq uint64 `json:"seq"`
	// Time is when it was written.
	Time time.Time `json:"time"`
	// Level is "debug", "info", "warn" or "error".
	Level string `json:"level"`
	// Message is the record's message.
	Message string `json:"message"`
	// Node is the node whose code wrote it.
	Node string `json:"node,omitempty"`
	// TraceID and SpanID correlate it with a span.
	TraceID string `json:"traceId,omitempty"`
	SpanID  string `json:"spanId,omitempty"`
	// Attrs are the record's attributes, as text, secrets redacted.
	Attrs map[string]string `json:"attrs,omitempty"`
}

// MockMessage is the replacement a test gave the app ([MockReplace]).
// It is set by a test only; the Studio shows it and never sets one (D13).
type MockMessage struct {
	// Node is the node it applies to: a port, a command or a query.
	Node string `json:"node"`
	// Mode is [MockReplace].
	Mode string `json:"mode"`
	// Hits counts the runs it changed.
	Hits int64 `json:"hits"`
}

// MailSummaryMessage is one mail as the outbox sees it.
// It carries no body: the Studio lists summaries and reads a whole mail on
// demand.
type MailSummaryMessage struct {
	// ID identifies the mail in the outbox.
	ID string `json:"id"`
	// Mailer is the mailer node.
	Mailer string `json:"mailer"`
	// From and To are addresses, rendered.
	From string   `json:"from"`
	To   []string `json:"to"`
	// Subject is the subject.
	Subject string `json:"subject"`
	// Status is one of the Mail constants.
	Status string `json:"status"`
	// Attempts counts delivery attempts.
	Attempts int `json:"attempts"`
	// Error is the public message of the last failure.
	Error string `json:"error,omitempty"`
	// QueuedAt is when it entered the outbox; SentAt when it left.
	QueuedAt time.Time  `json:"queuedAt"`
	SentAt   *time.Time `json:"sentAt,omitempty"`
	// TraceID is the trace that queued it.
	TraceID string `json:"traceId,omitempty"`
	// Node is the node that queued it.
	Node string `json:"node,omitempty"`
}
