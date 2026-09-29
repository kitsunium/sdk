// The kind-specific details of a node: an endpoint's, a port's, a store's, a
// workflow's, and the rest.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// Exposure says who may reach an endpoint.
const (
	// ExposePublic endpoints are routed on the product's HTTP listener.
	ExposePublic string = core.ExposePublic
	// ExposePrivate endpoints are reachable only in-process, through Call.
	ExposePrivate string = core.ExposePrivate
)

// Authentication an endpoint asks for.
const (
	// AuthRequired endpoints run only for an authenticated caller; the others
	// are answered 401 before the handler runs.
	AuthRequired string = core.AuthRequired
	// AuthOptional endpoints run the auth handler when credentials are
	// present, and serve anonymous callers too.
	AuthOptional string = core.AuthOptional
)

// Transition triggers.
const (
	// TriggerEvent is a transition fired by code, naming an event.
	TriggerEvent string = core.TriggerEvent
	// TriggerTimer is a transition fired after a duration in a state, or at
	// an instant the entity carries.
	TriggerTimer string = core.TriggerTimer
	// TriggerGuard is a transition fired when a condition on the entity
	// holds, checked each time the entity is written.
	TriggerGuard string = core.TriggerGuard
	// TriggerCreate is the entry of a new instance into the initial state.
	TriggerCreate string = core.TriggerCreate
)

// How the start chose what a port calls: kit.Bind, else the one
// Service.Implement among the services the app mounts, else the port's
// fallback — or a test replaced the port.
const (
	// ViaBind is the app's kit.Bind.
	ViaBind string = core.ViaBind
	// ViaImplement is the one Service.Implement among the mounted services.
	ViaImplement string = core.ViaImplement
	// ViaFallback is the port's own kit.Fallback.
	ViaFallback string = core.ViaFallback
	// ViaReplace is a test's kit.Replace: a function answers in place of
	// whatever the port would call, and nothing is bound.
	ViaReplace string = core.ViaReplace
)

// How a command is handled.
const (
	// ModeSync is handled on its caller's goroutine: Dispatch returns the
	// handler's result.
	ModeSync string = core.ModeSync
	// ModeQueued is sent to the background (kit.Queued): Dispatch returns
	// once the command's own queue accepted it, and a consumer handles it,
	// retried and dead-lettered.
	ModeQueued string = core.ModeQueued
)

// Mail transports.
const (
	// TransportSMTP delivers to an SMTP relay.
	TransportSMTP string = core.TransportSMTP
	// TransportCapture keeps every message for the Studio's mailbox and
	// delivers nothing: the transport of dev and tests.
	TransportCapture string = core.TransportCapture
)

// How a secret is made.
const (
	// SecretProvided is given by the operator: an SMTP URL, an API token.
	SecretProvided string = core.SecretProvided
	// SecretGenerated is made by kit and rotated on a schedule: a signing or
	// sealing key.
	SecretGenerated string = core.SecretGenerated
)

// Where a running app found a secret.
const (
	// SecretFromEnv is the environment: the variable, or the file its _FILE
	// form names.
	SecretFromEnv string = core.SecretFromEnv
	// SecretFromFile is kit's encrypted file store: KIT_SECRETS=file:<dir>,
	// or .kit/secrets/dev in dev.
	SecretFromFile string = core.SecretFromFile
	// SecretFromMemory is a store in the process's memory: a test, or
	// KIT_SECRETS=memory. A generated secret is made again at every start.
	SecretFromMemory string = core.SecretFromMemory
	// SecretFromStore is a store the product's code gave kit.
	SecretFromStore string = core.SecretFromStore
)

// Loop styles.
const (
	// LoopDeclared is Service.Loop: kit owns the wait — the wake sources are
	// data, so the diagram draws them — and the product owns the work.
	LoopDeclared string = core.LoopDeclared
	// LoopGoroutine is Service.Go: the product wrote the whole loop; kit
	// starts it, stops it, restarts it after a failure, and reads its select
	// statements to draw what it waits on.
	LoopGoroutine string = core.LoopGoroutine
)

// Wake source kinds of a declared loop, and why a loop of kit's ran.
const (
	WakeInterval string = core.WakeInterval // a fixed period
	WakeDeadline string = core.WakeDeadline // a time the product computes after every run
	WakeTopic    string = core.WakeTopic    // any publish on a topic
	WakeStart    string = core.WakeStart    // once, when the app starts
	WakeManual   string = core.WakeManual   // Loop.Nudge, or the Studio
	WakeChange   string = core.WakeChange   // a write of the store a workflow runs over, or of one a watch hears
)

// Select case kinds.
const (
	SelectTimer   string = core.SelectTimer   // a timer, ticker or time.After channel
	SelectDone    string = core.SelectDone    // ctx.Done(): the loop's way out
	SelectReceive string = core.SelectReceive // any other receive
	SelectSend    string = core.SelectSend    // a send
	SelectDefault string = core.SelectDefault // the default case: the select never blocks
)

// What a field holds: the classes of a kit tag (ADR 0006).
const (
	// ClassPublic holds nothing personal and nothing secret: the question
	// was asked.
	ClassPublic string = core.ClassPublic
	// ClassPersonal holds data about an identifiable person: a name, an
	// address, a message a person wrote.
	ClassPersonal string = core.ClassPersonal
	// ClassSpecial holds the special categories of GDPR art. 9(1), or
	// criminal convictions (art. 10).
	ClassSpecial string = core.ClassSpecial
	// ClassSecret holds a credential: a password's hash, a token's digest.
	ClassSecret string = core.ClassSecret
)

type (
	// EndpointInfo describes an HTTP endpoint, or the implementation of a port.
	// It carries the method and path, the request and response schemas, and its
	// auth.
	EndpointInfo = core.EndpointSpec
)

type (
	// PortInfo describes a port: an operation a service needs and another
	// implements.
	PortInfo = core.PortSpec
)

type (
	// CommandInfo describes a command: an operation that changes something.
	// It carries its input and result schemas, its handler, its queue and who may
	// dispatch it.
	CommandInfo = core.CommandSpec
)

type (
	// QueryInfo describes a query: an operation that reads and changes
	// nothing.
	QueryInfo = core.QuerySpec
)

type (
	// Permission is what a caller needs to dispatch a command or to ask a
	// query: that the app's policy lets it do Action to Resource (Allow).
	Permission = core.PermissionMessage
)

type (
	// StoreInfo describes a store.
	// It carries the entity's schema, its backend, its indexes, its privacy and
	// its history.
	StoreInfo = core.StoreSpec
)

type (
	// StorePrivacy is what a store keeps of people: whom each record is about,
	// why, for how long, and — on a runtime graph — what is held and due.
	StorePrivacy = core.StorePrivacyMessage
)

type (
	// Retention is when kit erases or deletes a record: after a delay counted
	// from an instant the record carries (After or Setting, and Since), or at an
	// instant a function gives (At).
	Retention = core.RetentionSpec
)

type (
	// HeldUntil is the hold a store declares on each of its records, until an
	// instant the record carries.
	HeldUntil = core.HeldUntilSpec
)

type (
	// IndexInfo is one secondary index of a store.
	// Its name is unique in the store; Unique says whether two records may share
	// a key.
	IndexInfo = core.IndexSpec
)

type (
	// TopicInfo describes a topic.
	// It carries the message's schema; its subscriptions are the nodes it
	// delivers to.
	TopicInfo = core.TopicSpec
)

type (
	// SubscriptionInfo describes a subscription — or a watch (ADR 0008), a
	// subscription whose deliveries come from stores instead of a topic.
	SubscriptionInfo = core.SubscriptionSpec
)

type (
	// WorkflowInfo describes a state machine bound to a store.
	// It carries the states and the transitions between them, and the store it
	// lives in.
	WorkflowInfo = core.WorkflowSpec
)

type (
	// StateInfo is one state of a workflow.
	// Terminal marks a state an instance stays in; Count is how many instances
	// are in it.
	StateInfo = core.StateSpec
)

type (
	// TransitionInfo is one arrow of a workflow.
	// It names the event that fires it and the states it goes from and to.
	TransitionInfo = core.TransitionSpec
)

type (
	// JobInfo describes a scheduled job.
	// It carries the fixed interval or the cron expression that schedules it.
	JobInfo = core.JobSpec
)

type (
	// FrontendInfo describes static assets served by the product.
	// It carries the path the assets are served under and where they are read
	// from.
	FrontendInfo = core.FrontendSpec
)

type (
	// AuthInfo describes the app's authentication handler.
	// Credentials says what it reads from a request; Endpoints are the ones
	// behind it.
	AuthInfo = core.AuthSpec
)

type (
	// MailerInfo describes outbound mail.
	// It carries the transport that empties the outbox and the outbox's counters.
	MailerInfo = core.MailerSpec
)

type (
	// SecretInfo describes a declared secret: how it is made, where it lives,
	// its versions — never its value.
	SecretInfo = core.SecretSpec
)

type (
	// LoopInfo describes a loop node.
	// Style says whether it is declared (kit owns the wait) or hand-written.
	LoopInfo = core.LoopSpec
)

type (
	// WakeSource is one thing that wakes a declared loop.
	// Kind says whether a topic, a period (Every) or a function wakes the loop.
	WakeSource = core.WakeSourceMessage
)

type (
	// SelectCase is one case of a select statement in a hand-written loop.
	// The analyzer reads it from the loop's body; Kind says what the case waits
	// on.
	SelectCase = core.SelectCaseMessage
)

type (
	// Mechanic is a generic building block, backed by an SDK package, that a node
	// composes. The catalog lists every mechanic kit offers; an endpoint's
	// pipeline lists the ones it uses.
	Mechanic = core.MechanicMessage
)

type (
	// Schema describes a Go type as it appears on the wire.
	// It is a JSON-Schema-like shape: a kind, its fields or elements, and its Go
	// type.
	Schema = core.SchemaMessage
)

type (
	// Field is one member of an object schema.
	// It carries the JSON name, the schema of its value and the classes of its
	// data.
	Field = core.FieldMessage
)
