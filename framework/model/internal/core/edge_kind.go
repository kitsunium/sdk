// The edge kinds of the graph: a closed set, written by name on the wire.

package core

// The edge kinds.
const (
	// EdgeCalls targets an endpoint or a port. From a port, it is the port's
	// binding — what the port calls — and it is declared.
	EdgeCalls EdgeKind = "calls"
	// EdgeReads targets a store the source reads from.
	EdgeReads EdgeKind = "reads"
	// EdgeWrites targets a store the source writes to.
	EdgeWrites EdgeKind = "writes"
	// EdgePublishes targets a topic.
	EdgePublishes EdgeKind = "publishes"
	// EdgeDelivers goes from a topic to each of its subscriptions, and from a
	// store to each watch it feeds (ADR 0008). Declared.
	EdgeDelivers EdgeKind = "delivers"
	// EdgeTransitions targets a workflow; Label carries the event.
	EdgeTransitions EdgeKind = "transitions"
	// EdgePersists goes from a workflow to the store it lives in. Declared.
	EdgePersists EdgeKind = "persists"
	// EdgeSends targets a mailer: the source puts a message in its outbox.
	EdgeSends EdgeKind = "sends"
	// EdgeWakes goes from a topic to a declared loop it wakes. Declared.
	EdgeWakes EdgeKind = "wakes"
	// EdgeUses targets a secret whose value or keys the source uses.
	EdgeUses EdgeKind = "uses"
	// EdgeDispatches targets a command: the source dispatches it. From an
	// endpoint that exposes the command, or from a port bound to it, it is
	// declared.
	EdgeDispatches EdgeKind = "dispatches"
	// EdgeAsks targets a query: the source asks it. From an endpoint that
	// exposes the query, or from a port bound to it, it is declared.
	EdgeAsks EdgeKind = "asks"
	// EdgeContracts goes from one process role to another it talks to —
	// through a socket, a pipe, a command line —, and names the versioned
	// contract they share in [EdgeMessage.Contract]. It is the only edge between two
	// roles (D22). Declared.
	EdgeContracts EdgeKind = "contracts"
	// EdgeRuns goes from a role to what it runs: a CLI command, a listener,
	// an endpoint's server, a loop. Declared.
	EdgeRuns EdgeKind = "runs"
	// EdgeImports goes from a component to a library it may import (D19).
	// Declared.
	EdgeImports EdgeKind = "imports"
	// EdgeRenders goes from a presentation to what it renders. Declared.
	EdgeRenders EdgeKind = "renders"
)
