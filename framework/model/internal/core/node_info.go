// The kind-specific details of a node: an endpoint's, a port's, a store's, a
// workflow's, and the rest.

package core

// Exposure says who may reach an endpoint.
const (
	// ExposePublic endpoints are routed on the product's HTTP listener.
	ExposePublic = "public"
	// ExposePrivate endpoints have no route: the implementation of a port
	// (Service.Implement), reached in process through its port. A graph kit
	// made before its operations were internal by default also marks so an
	// endpoint it kept off the listener.
	ExposePrivate = "private"
)

// How an exposure says it is open on purpose, when its operation declares
// no permission and no rule (EndpointInfo.Access).
const (
	// AccessAnyone is kit.Anyone(): whoever reaches the route may run the
	// operation.
	AccessAnyone = "anyone"
	// AccessAnyUser is kit.AnyUser(): any signed-in user may run it — the
	// operation asks for one.
	AccessAnyUser = "any-user"
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
