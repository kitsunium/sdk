// What a running product reports: its runtime state, loops, lifecycle
// components, live events, spans, traces and settings.

package core

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
