// The edge kinds of the graph: a closed set, written by name on the wire.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// The edge kinds.
const (
	// EdgeCalls targets an endpoint or a port. From a port, it is the port's
	// binding — what the port calls — and it is declared.
	EdgeCalls EdgeKind = core.EdgeCalls
	// EdgeReads targets a store the source reads from.
	EdgeReads EdgeKind = core.EdgeReads
	// EdgeWrites targets a store the source writes to.
	EdgeWrites EdgeKind = core.EdgeWrites
	// EdgePublishes targets a topic.
	EdgePublishes EdgeKind = core.EdgePublishes
	// EdgeDelivers goes from a topic to each of its subscriptions, and from a
	// store to each watch it feeds (ADR 0008). Declared.
	EdgeDelivers EdgeKind = core.EdgeDelivers
	// EdgeTransitions targets a workflow; Label carries the event.
	EdgeTransitions EdgeKind = core.EdgeTransitions
	// EdgePersists goes from a workflow to the store it lives in. Declared.
	EdgePersists EdgeKind = core.EdgePersists
	// EdgeSends targets a mailer: the source puts a message in its outbox.
	EdgeSends EdgeKind = core.EdgeSends
	// EdgeWakes goes from a topic to a declared loop it wakes. Declared.
	EdgeWakes EdgeKind = core.EdgeWakes
	// EdgeUses targets a secret whose value or keys the source uses.
	EdgeUses EdgeKind = core.EdgeUses
	// EdgeDispatches targets a command: the source dispatches it. From an
	// endpoint that exposes the command, or from a port bound to it, it is
	// declared.
	EdgeDispatches EdgeKind = core.EdgeDispatches
	// EdgeAsks targets a query: the source asks it. From an endpoint that
	// exposes the query, or from a port bound to it, it is declared.
	EdgeAsks EdgeKind = core.EdgeAsks
	// EdgeContracts goes from one process role to another it talks to —
	// through a socket, a pipe, a command line —, and names the versioned
	// contract they share in [Edge].Contract. It is the only edge between two
	// roles (D22). Declared.
	EdgeContracts EdgeKind = core.EdgeContracts
	// EdgeRuns goes from a role to what it runs: a CLI command, a listener,
	// an endpoint's server, a loop. Declared.
	EdgeRuns EdgeKind = core.EdgeRuns
	// EdgeImports goes from a component to a library it may import (D19).
	// Declared.
	EdgeImports EdgeKind = core.EdgeImports
	// EdgeRenders goes from a presentation to what it renders. Declared.
	EdgeRenders EdgeKind = core.EdgeRenders
)

type (
	// EdgeKind says how two nodes relate.
	// It is a wire format — the graph's JSON and the Studio's TypeScript unions
	// carry these names —, so it stays a string.
	//
	//ktn:wire-format
	EdgeKind = core.EdgeKind
)
