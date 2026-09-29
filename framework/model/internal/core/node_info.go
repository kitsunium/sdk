// The kind-specific details of a node: an endpoint's, a port's, a store's, a
// workflow's, and the rest.

package core

import "time"

// Exposure says who may reach an endpoint.
const (
	// ExposePublic endpoints are routed on the product's HTTP listener.
	ExposePublic = "public"
	// ExposePrivate endpoints are reachable only in-process, through Call.
	ExposePrivate = "private"
)

// Authentication an endpoint asks for.
const (
	// AuthRequired endpoints run only for an authenticated caller; the others
	// are answered 401 before the handler runs.
	AuthRequired = "required"
	// AuthOptional endpoints run the auth handler when credentials are
	// present, and serve anonymous callers too.
	AuthOptional = "optional"
)

// Transition triggers.
const (
	// TriggerEvent is a transition fired by code, naming an event.
	TriggerEvent = "event"
	// TriggerTimer is a transition fired after a duration in a state, or at
	// an instant the entity carries.
	TriggerTimer = "timer"
	// TriggerGuard is a transition fired when a condition on the entity
	// holds, checked each time the entity is written.
	TriggerGuard = "guard"
	// TriggerCreate is the entry of a new instance into the initial state.
	TriggerCreate = "create"
)

// How the start chose what a port calls: kit.Bind, else the one
// Service.Implement among the services the app mounts, else the port's
// fallback — or a test replaced the port.
const (
	// ViaBind is the app's kit.Bind.
	ViaBind = "bind"
	// ViaImplement is the one Service.Implement among the mounted services.
	ViaImplement = "implement"
	// ViaFallback is the port's own kit.Fallback.
	ViaFallback = "fallback"
	// ViaReplace is a test's kit.Replace: a function answers in place of
	// whatever the port would call, and nothing is bound.
	ViaReplace = "replace"
)

// How a command is handled.
const (
	// ModeSync is handled on its caller's goroutine: Dispatch returns the
	// handler's result.
	ModeSync = "sync"
	// ModeQueued is sent to the background (kit.Queued): Dispatch returns
	// once the command's own queue accepted it, and a consumer handles it,
	// retried and dead-lettered.
	ModeQueued = "queued"
)

// Mail transports.
const (
	// TransportSMTP delivers to an SMTP relay.
	TransportSMTP = "smtp"
	// TransportCapture keeps every message for the Studio's mailbox and
	// delivers nothing: the transport of dev and tests.
	TransportCapture = "capture"
)

// How a secret is made.
const (
	// SecretProvided is given by the operator: an SMTP URL, an API token.
	SecretProvided = "provided"
	// SecretGenerated is made by kit and rotated on a schedule: a signing or
	// sealing key.
	SecretGenerated = "generated"
)

// Where a running app found a secret.
const (
	// SecretFromEnv is the environment: the variable, or the file its _FILE
	// form names.
	SecretFromEnv = "env"
	// SecretFromFile is kit's encrypted file store: KIT_SECRETS=file:<dir>,
	// or .kit/secrets/dev in dev.
	SecretFromFile = "file"
	// SecretFromMemory is a store in the process's memory: a test, or
	// KIT_SECRETS=memory. A generated secret is made again at every start.
	SecretFromMemory = "memory"
	// SecretFromStore is a store the product's code gave kit.
	SecretFromStore = "store"
)

// Loop styles.
const (
	// LoopDeclared is Service.Loop: kit owns the wait — the wake sources are
	// data, so the diagram draws them — and the product owns the work.
	LoopDeclared = "declared"
	// LoopGoroutine is Service.Go: the product wrote the whole loop; kit
	// starts it, stops it, restarts it after a failure, and reads its select
	// statements to draw what it waits on.
	LoopGoroutine = "goroutine"
)

// Wake source kinds of a declared loop, and why a loop of kit's ran.
const (
	WakeInterval = "interval" // a fixed period
	WakeDeadline = "deadline" // a time the product computes after every run
	WakeTopic    = "topic"    // any publish on a topic
	WakeStart    = "start"    // once, when the app starts
	WakeManual   = "manual"   // Loop.Nudge, or the Studio
	WakeChange   = "change"   // a write of the store a workflow runs over, or of one a watch hears
)

// Select case kinds.
const (
	SelectTimer   = "timer"   // a timer, ticker or time.After channel
	SelectDone    = "done"    // ctx.Done(): the loop's way out
	SelectReceive = "receive" // any other receive
	SelectSend    = "send"    // a send
	SelectDefault = "default" // the default case: the select never blocks
)

// What a field holds: the classes of a kit tag (ADR 0006).
const (
	// ClassPublic holds nothing personal and nothing secret: the question
	// was asked.
	ClassPublic = "public"
	// ClassPersonal holds data about an identifiable person: a name, an
	// address, a message a person wrote.
	ClassPersonal = "personal"
	// ClassSpecial holds the special categories of GDPR art. 9(1), or
	// criminal convictions (art. 10).
	ClassSpecial = "special"
	// ClassSecret holds a credential: a password's hash, a token's digest.
	ClassSecret = "secret"
)

// EndpointSpec describes an HTTP endpoint, or the implementation of a port.
// It carries the method and path, the request and response schemas, and its
// auth.
type EndpointSpec struct {
	// Method is the HTTP method, upper case; empty for an implementation.
	Method string `json:"method"`
	// Path is the route pattern, with {wildcards}; empty for an
	// implementation.
	Path string `json:"path"`
	// Expose is [ExposePublic] or [ExposePrivate].
	Expose string `json:"expose"`
	// Auth is [AuthRequired], [AuthOptional], or empty when the endpoint
	// ignores authentication.
	Auth string `json:"auth,omitempty"`
	// Request is the input type.
	Request *SchemaMessage `json:"request,omitempty"`
	// Response is the output type.
	Response *SchemaMessage `json:"response,omitempty"`
	// Pipeline lists the mechanics a request goes through before the
	// handler, in order.
	Pipeline []MechanicMessage `json:"pipeline,omitempty"`
	// Implements is the node ID of the port this endpoint implements
	// (Service.Implement): a private endpoint with no route, reached through
	// the port.
	Implements string `json:"implements,omitempty"`
	// Exposes is the node ID of the command this endpoint dispatches, or of
	// the query it asks (Command.Expose, Query.Expose): its authentication
	// is the operation's, and the rest of its pipeline — validation, key,
	// authorization — the operation's own.
	Exposes string `json:"exposes,omitempty"`
}

// PortSpec describes a port: an operation a service needs and another
// implements.
type PortSpec struct {
	// Request is the type the port is called with.
	Request *SchemaMessage `json:"request,omitempty"`
	// Response is the type it answers.
	Response *SchemaMessage `json:"response,omitempty"`
	// Fallback is the node ID of the operation the port declares for when
	// the app binds nothing else (kit.Fallback).
	Fallback string `json:"fallback,omitempty"`
	// Bound is the node ID of the operation the port calls, as the start
	// chose it — on a static graph, as the analysis reads the same rule;
	// empty when nothing is bound or a test replaced the port. The graph
	// draws it as a declared edge from the port.
	Bound string `json:"bound,omitempty"`
	// Via is how it was chosen: one of the Via constants.
	Via string `json:"via,omitempty"`
}

// CommandSpec describes a command: an operation that changes something.
// It carries its input and result schemas, its handler, its queue and who may
// dispatch it.
type CommandSpec struct {
	// Input is the type it is dispatched with.
	Input *SchemaMessage `json:"input,omitempty"`
	// Result is the type it answers; Empty for a queued command.
	Result *SchemaMessage `json:"result,omitempty"`
	// Mode is [ModeSync] or [ModeQueued].
	Mode string `json:"mode"`
	// Auth is [AuthRequired] — implied by a permission —, [AuthOptional],
	// or empty when the command ignores authentication.
	Auth string `json:"auth,omitempty"`
	// Pipeline lists the mechanics a dispatch goes through before the
	// handler, in the order they run.
	Pipeline []MechanicMessage `json:"pipeline,omitempty"`
	// Permissions are the permissions a caller needs (Allow).
	Permissions []PermissionMessage `json:"permissions,omitempty"`
	// Authorize locates the function of its Authorize: a rule that needs
	// the data.
	Authorize *SourceMessage `json:"authorize,omitempty"`
	// Key is set when the command names the entity it is about (Key): two
	// runs with one key never overlap.
	Key bool `json:"key,omitempty"`
	// MaxDeliveries is how many attempts a queued command gets before it is
	// dead-lettered.
	MaxDeliveries int `json:"maxDeliveries,omitempty"`
	// Parallelism is how many dispatches of a queued command are handled at
	// once.
	Parallelism int `json:"parallelism,omitempty"`
	// Queue is "memory" or "file": where a queued command waits.
	Queue string `json:"queue,omitempty"`
	// DeadLetters is how many dispatches of a queued command were
	// abandoned, on a runtime graph.
	DeadLetters *int `json:"deadLetters,omitempty"`
}

// QuerySpec describes a query: an operation that reads and changes
// nothing.
type QuerySpec struct {
	// Input is the type it is asked with.
	Input *SchemaMessage `json:"input,omitempty"`
	// Result is the type it answers.
	Result *SchemaMessage `json:"result,omitempty"`
	// Auth is [AuthRequired] — implied by a permission —, [AuthOptional],
	// or empty when the query ignores authentication.
	Auth string `json:"auth,omitempty"`
	// Pipeline lists the mechanics a question goes through before the
	// handler, in the order they run.
	Pipeline []MechanicMessage `json:"pipeline,omitempty"`
	// Permissions are the permissions a caller needs (Allow).
	Permissions []PermissionMessage `json:"permissions,omitempty"`
	// Authorize locates the function of its Authorize: a rule that needs
	// the data.
	Authorize *SourceMessage `json:"authorize,omitempty"`
}

// PermissionMessage is what a caller needs to dispatch a command or to ask a
// query: that the app's policy lets it do Action to Resource (Allow).
type PermissionMessage struct {
	// Action is what the caller does: "place".
	Action string `json:"action"`
	// Resource is the kind of thing it does it to: "order".
	Resource string `json:"resource"`
}

// StoreSpec describes a store.
// It carries the entity's schema, its backend, its indexes, its privacy and
// its history.
type StoreSpec struct {
	// Entity is the stored type.
	Entity *SchemaMessage `json:"entity,omitempty"`
	// Backend is where the store's data is: "memory" or "file" — and, once
	// kit keeps stores on SQL (ADR 0004, step 2), the engine of the database
	// that keeps it: "postgres", "mysql", "sqlite".
	Backend string `json:"backend"`
	// Location is where a file backend keeps its data, relative to the data
	// directory.
	Location string `json:"location,omitempty"`
	// Database names the database that keeps the store, as the app declares
	// it (kit.Database, kit.Keeps) — present even while the store's data
	// stays in the data directory: in dev without the database's URL, and
	// until kit keeps stores on SQL. Its container is
	// "container:database:<name>".
	Database string `json:"database,omitempty"`
	// Count is how many entities the store holds, on a runtime graph.
	Count *int `json:"count,omitempty"`
	// Indexes are the secondary indexes the store maintains, in declaration
	// order.
	Indexes []IndexSpec `json:"indexes,omitempty"`
	// Privacy says what the store does with personal data (ADR 0006): whom
	// its records are about, why it keeps them and for how long. Absent when
	// its entity holds nothing personal and it declares no retention.
	Privacy *StorePrivacyMessage `json:"privacy,omitempty"`
	// History says what the store remembers of its records (ADR 0007): the
	// fields that keep their former values, its password policies. Absent
	// when it remembers nothing and has no policy.
	History *StoreHistoryMessage `json:"history,omitempty"`
	// ReadModel is set on a store derived from others (kit.ReadModel):
	// written by projections — subscriptions —, read by queries.
	ReadModel bool `json:"readModel,omitempty"`
}

// StorePrivacyMessage is what a store keeps of people: whom each record is about,
// why, for how long, and — on a runtime graph — what is held and due.
type StorePrivacyMessage struct {
	// Subject is the JSON pointer of the field that says whom each record is
	// about: "/email". Empty when the entity has no subject field.
	Subject string `json:"subject,omitempty"`
	// Purpose is why the store keeps its records, as kit.Purpose says it.
	Purpose string `json:"purpose,omitempty"`
	// Erase says when kit clears a record's personal data; Delete when it
	// removes the record.
	Erase  *RetentionSpec `json:"erase,omitempty"`
	Delete *RetentionSpec `json:"delete,omitempty"`
	// HeldUntil holds each record until an instant it carries: the law's
	// own retention (kit.HeldUntil).
	HeldUntil *HeldUntilSpec `json:"heldUntil,omitempty"`
	// Anonymise locates the function that says what an erased record keeps,
	// generalised (kit.Anonymise).
	Anonymise *SourceMessage `json:"anonymise,omitempty"`
	// DeleteOnErasure: a person's erasure deletes the record instead of
	// clearing it (kit.DeleteOnErasure).
	DeleteOnErasure bool `json:"deleteOnErasure,omitempty"`

	// Held is how many records a hold keeps, on a runtime graph.
	Held *int `json:"held,omitempty"`
	// Due is when the next record is due to be erased or deleted, on a
	// runtime graph; absent when none is.
	Due *time.Time `json:"due,omitempty"`
	// Erased and Deleted count what this run erased and deleted, on a
	// runtime graph.
	Erased  *int `json:"erased,omitempty"`
	Deleted *int `json:"deleted,omitempty"`
}

// RetentionSpec is when kit erases or deletes a record: after a delay counted
// from an instant the record carries (After or Setting, and Since), or at an
// instant a function gives (At).
type RetentionSpec struct {
	// After is the delay, as a Go duration: the declared one, or the
	// setting's value when the graph describes a running app.
	After string `json:"after,omitempty"`
	// Setting names the setting of the store's service the delay is read
	// from, when the environment gives it.
	Setting string `json:"setting,omitempty"`
	// Since locates the function that gives the instant the delay counts
	// from: a record's closing, say.
	Since *SourceMessage `json:"since,omitempty"`
	// At locates the function that gives the instant itself.
	At *SourceMessage `json:"at,omitempty"`
}

// HeldUntilSpec is the hold a store declares on each of its records, until an
// instant the record carries.
type HeldUntilSpec struct {
	// At locates the function that gives the instant.
	At *SourceMessage `json:"at,omitempty"`
	// Reason is the legal ground the declaration gives: "commercial code:
	// 10 years".
	Reason string `json:"reason,omitempty"`
}

// IndexSpec is one secondary index of a store.
// Its name is unique in the store; Unique says whether two records may share
// a key.
type IndexSpec struct {
	// Name is how code asks for the index: Store.Lookup(ctx, name, key).
	Name string `json:"name"`
	// Unique is set when two entities may not share a key: a write that
	// would is refused with a conflict.
	Unique bool `json:"unique,omitempty"`
}

// TopicSpec describes a topic.
// It carries the message's schema; its subscriptions are the nodes it
// delivers to.
type TopicSpec struct {
	// Message is the published type.
	Message *SchemaMessage `json:"message,omitempty"`
	// Delivery is the guarantee: "at-least-once".
	Delivery string `json:"delivery"`
	// Broker is "memory" or "file".
	Broker string `json:"broker,omitempty"`
}

// SubscriptionSpec describes a subscription — or a watch (ADR 0008), a
// subscription whose deliveries come from stores instead of a topic.
type SubscriptionSpec struct {
	// Topic is the node ID of the consumed topic; empty for a watch.
	Topic string `json:"topic"`
	// Mark is what a watch hears the writes of: "personal", "special" or
	// "moderated", ADR 0006's marks. Empty for a subscription.
	Mark string `json:"mark,omitempty"`
	// Stores are the node IDs of the stores that feed a watch, sorted: those
	// whose entity holds a field with its mark, but its own module's — the
	// product's own, for a watch of the product's. Each draws a declared
	// delivers edge to it.
	Stores []string `json:"stores,omitempty"`
	// MaxDeliveries is how many attempts a message gets before it is
	// dead-lettered.
	MaxDeliveries int `json:"maxDeliveries,omitempty"`
	// Parallelism is how many messages are handled concurrently.
	Parallelism int `json:"parallelism,omitempty"`
	// DeadLetters is how many messages were abandoned, on a runtime graph.
	DeadLetters *int `json:"deadLetters,omitempty"`
}

// WorkflowSpec describes a state machine bound to a store.
// It carries the states and the transitions between them, and the store it
// lives in.
type WorkflowSpec struct {
	// Store is the node ID of the store holding the instances.
	Store string `json:"store"`
	// Field is the wire name of the entity field holding the state, when the
	// state function points at a field of the entity.
	Field string `json:"field,omitempty"`
	// Initial is the state a new instance enters.
	Initial string `json:"initial"`
	// States are in declaration order.
	States []StateSpec `json:"states"`
	// Transitions are in declaration order.
	Transitions []TransitionSpec `json:"transitions"`
}

// StateSpec is one state of a workflow.
// Terminal marks a state an instance stays in; Count is how many instances
// are in it.
type StateSpec struct {
	// Name is the state value, as text.
	Name string `json:"name"`
	// Terminal is set when no transition leaves the state.
	Terminal bool `json:"terminal,omitempty"`
	// Count is how many instances are in the state, on a runtime graph.
	Count *int `json:"count,omitempty"`
	// OnEnter lists the hooks run when an instance enters the state.
	OnEnter []SourceMessage `json:"onEnter,omitempty"`
}

// TransitionSpec is one arrow of a workflow.
// It names the event that fires it and the states it goes from and to.
type TransitionSpec struct {
	// Event names the transition. Timer and guard transitions have one too.
	Event string `json:"event"`
	// From is the source state.
	From string `json:"from"`
	// To is the target state.
	To string `json:"to"`
	// Trigger is [TriggerEvent], [TriggerTimer] or [TriggerGuard].
	Trigger string `json:"trigger"`
	// After is the delay of a timer transition, as a Go duration string.
	After string `json:"after,omitempty"`
	// Setting names the setting of the service the delay is read from, when
	// the environment gives it: "archive-after".
	Setting string `json:"setting,omitempty"`
	// At locates the function that says when a timer transition declared at
	// an instant is due — a due date, a deadline the entity carries.
	At *SourceMessage `json:"at,omitempty"`
	// Guard locates the condition of a guard transition.
	Guard *SourceMessage `json:"guard,omitempty"`
	// Source is where the transition is declared.
	Source *SourceMessage `json:"source,omitempty"`
	// Callers are the node IDs found to fire this event.
	Callers []string `json:"callers,omitempty"`
}

// JobSpec describes a scheduled job.
// It carries the fixed interval or the cron expression that schedules it.
type JobSpec struct {
	// Schedule is "every 10s" or a five-field cron expression.
	Schedule string `json:"schedule"`
	// Kind is "interval" or "cron".
	Kind string `json:"kind"`
}

// FrontendSpec describes static assets served by the product.
// It carries the path the assets are served under and where they are read
// from.
type FrontendSpec struct {
	// Prefix is the URL path the assets are served under.
	Prefix string `json:"prefix"`
	// Files is how many files are served.
	Files int `json:"files,omitempty"`
}

// AuthSpec describes the app's authentication handler.
// Credentials says what it reads from a request; Endpoints are the ones
// behind it.
type AuthSpec struct {
	// Credentials is the type the handler reads: its fields are tagged
	// cookie:"…" or header:"…".
	Credentials *SchemaMessage `json:"credentials,omitempty"`
	// Data is the type of what the handler says about the caller, which
	// endpoints read with kit.AuthData.
	Data *SchemaMessage `json:"data,omitempty"`
	// Endpoints counts the endpoints that ask for authentication.
	Endpoints int `json:"endpoints"`
}

// MailerSpec describes outbound mail.
// It carries the transport that empties the outbox and the outbox's counters.
type MailerSpec struct {
	// Transport is [TransportSMTP] or [TransportCapture].
	Transport string `json:"transport"`
	// Server is the relay's host:port. Credentials never appear.
	Server string `json:"server,omitempty"`
	// TLS is "starttls", "implicit" or "none", for SMTP.
	TLS string `json:"tls,omitempty"`
	// From is the default sender.
	From string `json:"from,omitempty"`
	// MaxAttempts is how many deliveries a message gets before it is
	// dead-lettered.
	MaxAttempts int `json:"maxAttempts"`
	// Outbox is "memory" or "file": where queued messages wait.
	Outbox string `json:"outbox,omitempty"`
	// Queued is how many messages wait in the outbox, on a runtime graph.
	Queued *int `json:"queued,omitempty"`
	// Sent is how many messages the transport accepted, on a runtime graph.
	Sent *int `json:"sent,omitempty"`
	// DeadLetters is how many messages were abandoned, on a runtime graph.
	DeadLetters *int `json:"deadLetters,omitempty"`
}

// SecretSpec describes a declared secret: how it is made, where it lives,
// its versions — never its value.
type SecretSpec struct {
	// Origin is [SecretProvided] or [SecretGenerated].
	Origin string `json:"origin"`
	// Variable is the environment variable that provides it, or — for a
	// generated secret — pins it: TODO_SMTP_URL, or TODO_SMTP_URL_FILE naming
	// a file.
	Variable string `json:"variable"`
	// Bytes is the size of each version of a generated secret.
	Bytes int `json:"bytes,omitempty"`
	// RotateEvery is how often kit makes a new version of a generated
	// secret, as a Go duration.
	RotateEvery string `json:"rotateEvery,omitempty"`
	// Keep is how many versions a rotation leaves, the new one included.
	Keep int `json:"keep,omitempty"`

	// From is where the running app found it: one of the SecretFrom
	// constants, on a runtime graph. Empty when it is found nowhere.
	From string `json:"from,omitempty"`
	// Version is the number of its current version, from 1.
	Version int `json:"version,omitempty"`
	// Created is when the current version was made; the environment does
	// not say when a variable was set.
	Created *time.Time `json:"created,omitempty"`
	// Versions is how many versions are kept.
	Versions int `json:"versions,omitempty"`
	// NextRotation is when kit rotates it next. Absent when kit does not
	// rotate it: provided, pinned by the environment, or in a store kit
	// cannot write.
	NextRotation *time.Time `json:"nextRotation,omitempty"`
	// Pinned: a generated secret the environment provides — kit uses it and
	// does not rotate it.
	Pinned bool `json:"pinned,omitempty"`
	// Rotations counts the rotations this process made.
	Rotations int `json:"rotations,omitempty"`
	// Problem says why it cannot be used, on a runtime graph.
	Problem string `json:"problem,omitempty"`
}

// LoopSpec describes a loop node.
// Style says whether it is declared (kit owns the wait) or hand-written.
type LoopSpec struct {
	// Style is [LoopDeclared] or [LoopGoroutine].
	Style string `json:"style"`
	// Wakes are what wakes a declared loop.
	Wakes []WakeSourceMessage `json:"wakes,omitempty"`
	// Selects are the cases of the select statements a hand-written loop
	// waits in, as the static analysis read them.
	Selects []SelectCaseMessage `json:"selects,omitempty"`
	// Restart is the supervision policy, as text.
	Restart string `json:"restart,omitempty"`
}

// WakeSourceMessage is one thing that wakes a declared loop.
// Kind says whether a topic, a period (Every) or a function wakes the loop.
type WakeSourceMessage struct {
	// Kind is one of the Wake constants.
	Kind string `json:"kind"`
	// Every is the period of an interval wake, as a Go duration.
	Every string `json:"every,omitempty"`
	// Topic is the node ID of a topic wake.
	Topic string `json:"topic,omitempty"`
	// Func locates the function computing a deadline wake.
	Func *SourceMessage `json:"func,omitempty"`
}

// SelectCaseMessage is one case of a select statement in a hand-written loop.
// The analyzer reads it from the loop's body; Kind says what the case waits
// on.
type SelectCaseMessage struct {
	// Text is the case as written: "<-ticker.C", "job := <-jobs".
	Text string `json:"text"`
	// Kind is one of the Select constants.
	Kind string `json:"kind"`
	// Source is where the case is.
	Source *SourceMessage `json:"source,omitempty"`
}

// MechanicMessage is a generic building block, backed by an SDK package, that a node
// composes. The catalog lists every mechanic kit offers; an endpoint's
// pipeline lists the ones it uses.
type MechanicMessage struct {
	// Kind is the stable identifier: "validate", "ratelimit", "timeout"…
	Kind string `json:"kind"`
	// Label is a short human rendering of the configuration: "5/s".
	Label string `json:"label"`
	// Package is the SDK package that implements it.
	Package string `json:"package,omitempty"`
	// Doc says what it does, for a catalog.
	Doc string `json:"doc,omitempty"`
	// Snippet is the Go code that adds it, for a catalog.
	Snippet string `json:"snippet,omitempty"`
	// Config is the configuration, as text.
	Config map[string]string `json:"config,omitempty"`
}

// SchemaMessage describes a Go type as it appears on the wire.
// It is a JSON-Schema-like shape: a kind, its fields or elements, and its Go
// type.
type SchemaMessage struct {
	// Type is "object", "array", "map", "string", "integer", "number",
	// "boolean" or "any".
	Type string `json:"type"`
	// Name is the qualified Go type name, for named types.
	Name string `json:"name,omitempty"`
	// Format refines a scalar: "date-time", "duration", "int64"…
	Format string `json:"format,omitempty"`
	// Fields are the members of an object.
	Fields []FieldMessage `json:"fields,omitempty"`
	// Items is the element type of an array.
	Items *SchemaMessage `json:"items,omitempty"`
	// Values is the value type of a map.
	Values *SchemaMessage `json:"values,omitempty"`
	// Enum lists the allowed values, when they are known.
	Enum []string `json:"enum,omitempty"`
	// Ref names a type already described higher up, to break a cycle.
	Ref string `json:"ref,omitempty"`
	// Nullable says a nil value is written null: a pointer, a slice, a map,
	// an interface — unless its member leaves it out (omitempty, omitzero).
	Nullable bool `json:"nullable,omitempty"`
}

// FieldMessage is one member of an object schema.
// It carries the JSON name, the schema of its value and the classes of its
// data.
type FieldMessage struct {
	// Name is the wire name.
	Name string `json:"name"`
	// In is where a request field is read from: "body", "path", "query",
	// "header" or "cookie". Empty outside requests.
	In string `json:"in,omitempty"`
	// Type is the member's schema.
	Type *SchemaMessage `json:"type"`
	// Optional is set when the member may be absent.
	Optional bool `json:"optional,omitempty"`
	// Rules are the validation rules, as written in the validate tag.
	Rules string `json:"rules,omitempty"`
	// Doc is the field's comment, when the analyzer found one.
	Doc string `json:"doc,omitempty"`

	// Class is what the field holds, as its kit tag says (ADR 0006): one of
	// the Class constants, or empty when the field is unclassified. A
	// subject field is personal unless it is special.
	Class string `json:"class,omitempty"`
	// Subject marks the field that says whom a record is about.
	Subject bool `json:"subject,omitempty"`
	// Moderated marks content others see and a moderator may act on.
	Moderated bool `json:"moderated,omitempty"`
	// Sealed is set when kit keeps the field sealed at rest. Nothing is
	// sealed before kit's sealing lands (ADR 0006, step 3).
	Sealed bool `json:"sealed,omitempty"`
	// Erased marks the time kit stamps when it erases the record.
	Erased bool `json:"erased,omitempty"`
	// History is how many former values kit keeps of the field (ADR 0007).
	History int `json:"history,omitempty"`
}

// authorization locates the function of a command's or a query's
// Authorize; nil for any other node, or one without.
func (n *NodeEntity) authorization() *SourceMessage {
	switch {
	case n.Command != nil:
		return n.Command.Authorize
	case n.Query != nil:
		return n.Query.Authorize
	}
	return nil
}
