// What a running product reports: its runtime state, loops, lifecycle
// components, live events, spans, traces and settings.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// Process phases, in the order a process goes through them.
const (
	PhaseStarting string = core.PhaseStarting
	PhaseServing  string = core.PhaseServing
	PhaseDraining string = core.PhaseDraining
	PhaseStopped  string = core.PhaseStopped
	PhaseFailed   string = core.PhaseFailed
)

// Database states.
const (
	// DatabaseUnset: no URL, in dev. Its stores stay in the data directory
	// and nothing is opened.
	DatabaseUnset string = core.DatabaseUnset
	// DatabaseMemory: the app keeps its data in memory (kit.InMemory):
	// nothing is opened, no URL is read.
	DatabaseMemory string = core.DatabaseMemory
	// DatabaseOpen: the pool is open, its migrations ran, it answered its
	// check at start.
	DatabaseOpen string = core.DatabaseOpen
	// DatabaseFailed: the start could not open it, migrate it or reach it.
	DatabaseFailed string = core.DatabaseFailed
	// DatabaseClosed: not opened yet, or closed by the stop.
	DatabaseClosed string = core.DatabaseClosed
)

// Where a setting's value comes from.
const (
	SettingEnv     string = core.SettingEnv     // an environment variable
	SettingOption  string = core.SettingOption  // the product's code: an AppOption
	SettingDefault string = core.SettingDefault // kit's default for the environment
	SettingStore   string = core.SettingStore   // the environment's secret store (a secret)
	SettingKitDev  string = core.SettingKitDev  // kit dev set the variable, for this launch
	SettingFile    string = core.SettingFile    // a configuration file of the product (Detail)
)

// The settings kit reads, by the environment variable that sets each: kit's
// configuration reads them, the connectors and the architecture name them.
const (
	VarEnv          string = core.VarEnv
	VarAddr         string = core.VarAddr
	VarDataDir      string = core.VarDataDir
	VarStudio       string = core.VarStudio
	VarStudioRemote string = core.VarStudioRemote
	VarAnalyze      string = core.VarAnalyze
	VarTrustProxy   string = core.VarTrustProxy
	VarAllowedHosts string = core.VarAllowedHosts
	VarLogLevel     string = core.VarLogLevel
	VarSMTPURL      string = core.VarSMTPURL
	// VarRetention sets what the stores' retention loops do: on, dry-run
	// or off (ADR 0006).
	VarRetention string = core.VarRetention
	// VarDevForced is how kit dev tells the product which of the variables
	// above it set itself for this launch: their origin is kit dev.
	VarDevForced string = core.VarDevForced
)

// Boot steps, in the order kit takes them.
const (
	BootConfig       string = core.BootConfig       // the environment and the options, read
	BootDeclarations string = core.BootDeclarations // what the services declare, checked
	BootMount        string = core.BootMount        // the services bound to the app
	BootRoutes       string = core.BootRoutes       // the routes of endpoints and frontends
	BootData         string = core.BootData         // the data directory, opened
	BootHandler      string = core.BootHandler      // health, the Studio, protection, observation
	BootComponents   string = core.BootComponents   // the lifecycle components, started in order
	BootServing      string = core.BootServing      // ready: the phase is serving
)

// Component states.
const (
	ComponentPending  string = core.ComponentPending
	ComponentStarting string = core.ComponentStarting
	ComponentUp       string = core.ComponentUp
	ComponentStopping string = core.ComponentStopping
	ComponentDown     string = core.ComponentDown
	ComponentFailed   string = core.ComponentFailed
)

// Loop kinds.
const (
	LoopJob       string = core.LoopJob       // a job, fired by the scheduler
	LoopTimer     string = core.LoopTimer     // a workflow's own loop: its timers and guards, when due
	LoopConsumer  string = core.LoopConsumer  // a subscription's consumer, a queued command's, or a mailer's outbox
	LoopHTTP      string = core.LoopHTTP      // the HTTP server's accept loop
	LoopScheduler string = core.LoopScheduler // the scheduler's own loop, which fires the jobs
	LoopWake      string = core.LoopWake      // a declared loop's wake cycle (Service.Loop)
	LoopRoutine   string = core.LoopRoutine   // a hand-written loop (Service.Go)
	LoopRotation  string = core.LoopRotation  // a generated secret's rotation, on its schedule
	LoopRetention string = core.LoopRetention // a store's retention: erases and deletes what is due (ADR 0006)
)

// Loop provenances: who wrote the code that loops.
const (
	// ProvenanceLibrary loops are written by a library: net/http's accept
	// loop, the SDK scheduler, the SDK queue consumer.
	ProvenanceLibrary string = core.ProvenanceLibrary
	// ProvenanceKit loops are written by kit for a declaration: a workflow's
	// sweep, a declared loop's wake cycle.
	ProvenanceKit string = core.ProvenanceKit
	// ProvenanceProduct loops are written by hand in the product: Service.Go.
	ProvenanceProduct string = core.ProvenanceProduct
)

// Loop states.
const (
	LoopWaiting    string = core.LoopWaiting    // blocked until its next wake
	LoopRunning    string = core.LoopRunning    // doing a run
	LoopRestarting string = core.LoopRestarting // failed, waiting out its backoff
	LoopStopped    string = core.LoopStopped
)

// Span operations.
const (
	OpRequest     string = core.OpRequest     // an HTTP request served by an endpoint
	OpCall        string = core.OpCall        // an in-process call of an endpoint, or of a port
	OpRead        string = core.OpRead        // a store read
	OpWrite       string = core.OpWrite       // a store write
	OpPublish     string = core.OpPublish     // a topic publish
	OpDeliver     string = core.OpDeliver     // a subscription handling one message
	OpTransition  string = core.OpTransition  // a workflow transition
	OpRun         string = core.OpRun         // a job run, a loop run
	OpAuth        string = core.OpAuth        // the auth handler, in front of an endpoint
	OpSend        string = core.OpSend        // a mail put in the outbox
	OpDeliverMail string = core.OpDeliverMail // the outbox handing one mail to the transport
	OpSecret      string = core.OpSecret      // a secret's value or keys used: Value, Seal, Open, Sign, Verify
	OpDispatch    string = core.OpDispatch    // a command dispatched: its run, or its queuing for a queued one
	OpAsk         string = core.OpAsk         // a query asked
	OpHandle      string = core.OpHandle      // a queued command handled by its consumer, in the dispatcher's trace
	OpConnect     string = core.OpConnect     // a connection a listener accepted, handled until it closes
	OpCLI         string = core.OpCLI         // a short command-line command, run once
)

// Span statuses.
const (
	StatusOK    string = core.StatusOK
	StatusError string = core.StatusError
)

type (
	// Runtime is what a running process says about itself: the daemon's own state
	// and its internal loop.
	Runtime = core.RuntimeMessage
)

type (
	// Database is one database the app declares (kit.Database), as the running
	// process found it. Its URL is never shown: where it was found, the engine,
	// the address and the TLS mode are.
	Database = core.DatabaseMessage
)

type (
	// Pool is a database's connection pool, as database/sql counts it.
	// It is sampled when the runtime state is read; its counters never move a
	// revision.
	Pool = core.Pool
)

type (
	// MigrationSet is one set of migrations on a database — kit's own, the
	// product's (kit.Migrations), a module's — with its own version table, and
	// so its own lock.
	MigrationSet = core.MigrationSetMessage
)

type (
	// Migration is one versioned migration.
	// Its Version orders it among the others; Name says what it does.
	Migration = core.MigrationMessage
)

type (
	// Setting is one configuration value kit read when the product started:
	// kit's own, or one a service of the product declares.
	Setting = core.SettingMessage
)

type (
	// BootStep is one step of the start, as it ran.
	// It carries when it began, how long it took and, when it failed, its error.
	BootStep = core.BootStepMessage
)

type (
	// DevBuild is one build of `kit dev`: when, how long, and why.
	// It lets the Studio say what changed and how long the reload took.
	DevBuild = core.DevBuildMessage
)

type (
	// PhaseChange is one step of the daemon's life.
	// It carries the phase entered, when it was entered, and why.
	PhaseChange = core.PhaseChangeEvent
)

type (
	// Process is the Go process a product runs in.
	// It carries the runtime's figures — goroutines, heap, GC, CPU — taken at At.
	Process = core.ProcessSpec
)

type (
	// HTTPServer is the product's HTTP server, and whose loop serves it.
	// Its address is the one it listens on; its loop is the node that runs it.
	HTTPServer = core.HTTPServer
)

type (
	// Component is one lifecycle component: brought up in order, taken down in
	// reverse.
	Component = core.ComponentMessage
)

type (
	// Loop is one recurring piece of the daemon's internal loop: a job, a
	// workflow's timer sweep, a subscription's consumer, the HTTP accept loop, a
	// declared or hand-written loop.
	Loop = core.LoopMessage
)

type (
	// Event is one live event. Exactly one of the payload fields matching Type is
	// set.
	Event = core.Event
)

type (
	// Span is a finished unit of work, attributed to a node. Only allow-listed,
	// wire-safe fields travel: no URL, no query string, no private error text.
	Span = core.SpanMessage
)

type (
	// Payload is the request and the response of a span, as JSON, in dev only.
	// Every member whose name says it is a secret — password, token, secret,
	// authorization, cookie… — or whose Go field is tagged kit:"secret" is
	// replaced by "[redacted]", and each side is cut at 8 KiB.
	Payload = core.Payload
)

type (
	// TransitionEvent is one workflow instance moving between states.
	// It names the workflow, the entity, the event and the states it went from
	// and to.
	TransitionEvent = core.TransitionEvent
)

type (
	// Census is a workflow's population per state.
	// It counts the instances in each state when the census was taken.
	Census = core.CensusMessage
)

type (
	// Trace is the spans sharing one trace ID, root first.
	// The root span is the one with no parent in the trace; the others follow it.
	Trace = core.TraceMessage
)

type (
	// Snippet is source code served to a reader of the graph.
	// Focus and FocusEnd mark the lines a reader asked for, inside the lines
	// served.
	Snippet = core.SnippetMessage
)

type (
	// Instance is one entity's journey through a workflow.
	// It carries the entity's key, its current state and the steps that brought
	// it there.
	Instance = core.InstanceMessage
)

type (
	// Step is one transition in an instance's history.
	// It carries the event, the states it went from and to, and when it fired.
	Step = core.StepEvent
)

type (
	// LogRecord is one log record written by product code through kit.Log, kept
	// in dev so the Studio can show a request's logs beside its spans.
	LogRecord = core.LogRecordMessage
)

type (
	// Mock is the replacement a test gave the app ([MockReplace]).
	// It is set by a test only; the Studio shows it and never sets one (D13).
	Mock = core.MockMessage
)
