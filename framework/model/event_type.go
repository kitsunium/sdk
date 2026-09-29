// The event types of the graph: a closed set, written by name on the wire.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// The live event types.
const (
	// EventHello opens every stream and carries the graph revision.
	EventHello EventType = core.EventHello
	// EventSpan reports one unit of work that finished on a node.
	EventSpan EventType = core.EventSpan
	// EventTransition reports a workflow instance changing state.
	EventTransition EventType = core.EventTransition
	// EventLifecycle reports a lifecycle component moving, or the process
	// changing phase.
	EventLifecycle EventType = core.EventLifecycle
	// EventLoop reports a loop run.
	EventLoop EventType = core.EventLoop
	// EventCensus reports a workflow's population per state.
	EventCensus EventType = core.EventCensus
	// EventGraph reports that the graph structure changed.
	EventGraph EventType = core.EventGraph
	// EventLog reports a log record written by product code, in dev.
	EventLog EventType = core.EventLog
	// EventMail reports a mail entering the outbox or changing status.
	EventMail EventType = core.EventMail
)

type (
	// EventType names the kind of a live event.
	// It is a wire format — the graph's JSON and the Studio's TypeScript unions
	// carry these names —, so it stays a string.
	//
	//ktn:wire-format
	EventType = core.EventType
)
