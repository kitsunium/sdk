// The event types of the graph: a closed set, written by name on the wire.

package core

// The live event types.
const (
	// EventHello opens every stream and carries the graph revision.
	EventHello EventType = "hello"
	// EventSpan reports one unit of work that finished on a node.
	EventSpan EventType = "span"
	// EventTransition reports a workflow instance changing state.
	EventTransition EventType = "transition"
	// EventLifecycle reports a lifecycle component moving, or the process
	// changing phase.
	EventLifecycle EventType = "lifecycle"
	// EventLoop reports a loop run.
	EventLoop EventType = "loop"
	// EventCensus reports a workflow's population per state.
	EventCensus EventType = "census"
	// EventGraph reports that the graph structure changed.
	EventGraph EventType = "graph"
	// EventLog reports a log record written by product code, in dev.
	EventLog EventType = "log"
	// EventMail reports a mail entering the outbox or changing status.
	EventMail EventType = "mail"
)

// EventType names the kind of a live event.
// It is a wire format — the graph's JSON and the Studio's TypeScript unions
// carry these names —, so it stays a string.
//
//ktn:wire-format
type EventType string
