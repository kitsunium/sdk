// The architecture as a Mermaid flowchart.

package core

import (
	"fmt"
	"strings"
)

// simpleShapes are the Mermaid shapes of the kinds drawn by their name
// alone, as fmt formats of it.
var simpleShapes = map[NodeKind]string{
	KindStore:    "[(%q)]",
	KindTopic:    ">%q]",
	KindWorkflow: "{{%q}}",
	KindJob:      "([%q])",
	KindAuth:     "[/%q\\]",
	KindMailer:   "[[%q]]",
	KindPort:     "[\\%q/]",
}

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
func Mermaid(g *GraphMessage) string {
	var chart strings.Builder
	chart.WriteString("flowchart LR\n")
	ids := map[string]string{}
	mid := func(id string) string {
		if m, ok := ids[id]; ok {
			return m
		}
		m := fmt.Sprintf("n%d", len(ids))
		ids[id] = m
		return m
	}
	services, byService := groupNodes(&chart, g.Nodes, mid)
	writeServices(&chart, g, services, byService, mid)
	for i := range g.Edges {
		e := &g.Edges[i]
		if _, ok := ids[e.From]; !ok {
			continue
		}
		if _, ok := ids[e.To]; !ok {
			continue
		}
		arrow := "-->"
		if (e.Observed == nil && !e.Declared) || fallbackBinding(g, e) {
			arrow = "-.->"
		}
		fmt.Fprintf(&chart, "  %s %s|%s| %s\n", mid(e.From), arrow, mermaidText(edgeLabel(e)), mid(e.To))
	}
	return chart.String()
}

// groupNodes sorts the nodes into the services and each service's nodes,
// and draws the outside world as it meets it.
func groupNodes(chart *strings.Builder, nodes []NodeEntity, mid func(string) string) (services []NodeEntity, byService map[string][]NodeEntity) {
	byService = map[string][]NodeEntity{}
	for _, n := range nodes {
		switch n.Kind {
		case KindService:
			services = append(services, n)
		case KindExternal:
			fmt.Fprintf(chart, "  %s((%q))\n", mid(n.ID), n.Name)
		default:
			byService[n.Service] = append(byService[n.Service], n)
		}
	}
	return services, byService
}

// edgeLabel is what an arrow says: its kind, and its label when it has one.
func edgeLabel(e *EdgeMessage) string {
	if e.Label == "" {
		return string(e.Kind)
	}
	return string(e.Kind) + " " + e.Label
}

// writeServices draws each service as a subgraph around its nodes — a
// module's inside the module's own.
func writeServices(b *strings.Builder, g *GraphMessage, services []NodeEntity, byService map[string][]NodeEntity, mid func(string) string) {
	service := func(svc NodeEntity, indent string) {
		fmt.Fprintf(b, "%ssubgraph %s[%q]\n", indent, mid(svc.ID), svc.Name)
		for _, n := range byService[svc.ID] {
			fmt.Fprintf(b, "%s  %s%s\n", indent, mid(n.ID), shape(&n))
		}
		b.WriteString(indent + "end\n")
	}
	for _, m := range g.Modules {
		fmt.Fprintf(b, "  subgraph %s[%q]\n", mid("module:"+m.Name), "module "+m.Name)
		for _, svc := range services {
			if svc.Module == m.Name {
				service(svc, "    ")
			}
		}
		b.WriteString("  end\n")
	}
	for _, svc := range services {
		if svc.Module == "" || g.ModuleOf(svc.Module) == nil {
			service(svc, "  ")
		}
	}
}

// fallbackBinding reports whether e is a port's binding to its own fallback.
func fallbackBinding(g *GraphMessage, e *EdgeMessage) bool {
	n := g.Node(e.From)
	return n != nil && n.Port != nil && n.Port.Via == ViaFallback && n.Port.Bound == e.To
}

// shape draws each kind of node with its own Mermaid shape.
func shape(n *NodeEntity) string {
	if format, ok := simpleShapes[n.Kind]; ok {
		return fmt.Sprintf(format, n.Name)
	}
	switch n.Kind {
	case KindEndpoint:
		return endpointShape(n)
	case KindSubscription:
		return subscriptionShape(n, n.Name)
	case KindLoop:
		return loopShape(n)
	case KindCommand, KindQuery:
		return operationShape(n, n.Name)
	default:
		return fmt.Sprintf("[%q]", n.Name)
	}
}

// endpointShape draws an endpoint by its method and path, and says when it
// asks for a user.
func endpointShape(n *NodeEntity) string {
	label := n.Name
	if n.Endpoint == nil {
		return fmt.Sprintf("[%q]", label)
	}
	if n.Endpoint.Method != "" {
		label = n.Endpoint.Method + " " + n.Endpoint.Path
	}
	switch n.Endpoint.Auth {
	case AuthRequired:
		label += " · auth"
	case AuthOptional:
		label += " · auth optional"
	default:
	}
	return fmt.Sprintf("[%q]", label)
}

// loopShape draws a hand-written loop with a double circle, a declared one
// with a circle.
func loopShape(n *NodeEntity) string {
	if n.Loop != nil && n.Loop.Style == LoopGoroutine {
		return fmt.Sprintf("(((%q)))", n.Name)
	}
	return fmt.Sprintf("((%q))", n.Name)
}

// subscriptionShape draws a subscription as a parallelogram — a watch
// (ADR 0008) marked with the mark it watches.
func subscriptionShape(n *NodeEntity, label string) string {
	if n.Subscription != nil && n.Subscription.Mark != "" {
		label += " · watches " + n.Subscription.Mark
	}
	return fmt.Sprintf("[/%q/]", label)
}

// operationShape draws a command as a parallelogram leaning back — a
// subscription's leans forward —, marked when it waits in its queue, and a
// query as a rhombus, a question.
func operationShape(n *NodeEntity, label string) string {
	if n.Kind == KindQuery {
		return fmt.Sprintf("{%q}", label)
	}
	if n.Command != nil && n.Command.Mode == ModeQueued {
		label += " · queued"
	}
	return fmt.Sprintf("[\\%q\\]", label)
}

// mermaidText makes s safe inside a Mermaid edge label: a pipe would end
// the label, a double quote the string.
func mermaidText(s string) string {
	return strings.NewReplacer("|", "/", `"`, "'").Replace(s)
}
