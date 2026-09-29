// The architecture as a Mermaid flowchart.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// Mermaid renders the architecture of a graph as a Mermaid flowchart: one
// subgraph per service — a module's inside the module's own, ADR 0008 —, one
// arrow per edge. GitHub renders it in Markdown, which makes a product's
// README carry its own, always regenerable, diagram.
//
// Each kind has its shape: an endpoint a box (marked "auth" when it asks for
// a user; an implementation of a port by its name), a store a cylinder, a
// topic a flag, a subscription a parallelogram (a watch, ADR 0008, marked
// with the mark it watches — its stores' arrows say where from), a
// workflow a hexagon, a job a
// stadium, the auth handler a trapezoid — a gate —, a mailer a subroutine
// box, a declared loop a circle, a hand-written loop a double circle, a
// port an inverted trapezoid — a socket, UML's required interface —, a
// command a parallelogram leaning back — a subscription's leans forward —,
// marked "queued" when it waits in its queue, and a query a rhombus, a
// question. An arrow is solid when it exists by construction or was
// observed, dotted when only the code says so — or when it is a port's
// binding to its own fallback, the default the app may replace, as the
// Studio draws it.
func Mermaid(g *Graph) string {
	return core.Mermaid(g)
}
