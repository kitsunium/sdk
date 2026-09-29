// Package kit — the warnings for endpoints that set passwords outside a
// policy.
package kit

import (
	"slices"

	"github.com/kitsunium/sdk/framework/model"
)

// A password change costs: the SDK's Verify takes 144 ms by design, and a
// policy that refuses n former passwords verifies up to n+1 hashes per
// change. An endpoint that reaches Set or Change without a rate limit hands
// that cost, and the reuse oracle, to whoever calls it as often as they
// like. In dev, kit warns of each such endpoint (ADR 0007 §2): what reaches
// the policy is what the diagram shows — the edges the static analysis
// found in the endpoint's code, and those a run drew —, labelled password.

// passwordWarnings are the warnings of the endpoints of g that set or
// change a password with no rate limit in their pipeline, found among g's
// edges and static's, the static analysis — nil before it ran. Outside dev,
// kit says nothing.
func (a *App) passwordWarnings(g, static *model.Graph) []model.Diagnostic {
	if a.cfg.env != EnvDev {
		return nil
	}
	edges := g.Edges
	if static != nil {
		edges = append(slices.Clip(edges), static.Edges...)
	}
	var out []model.Diagnostic
	warned := map[string]bool{}
	for _, e := range edges {
		if e.Kind != model.EdgeWrites || e.Label != policyLabel || warned[e.From] {
			continue
		}
		n := g.Node(e.From)
		if !unlimited(n) {
			continue
		}
		warned[e.From] = true
		out = append(out, diagnosticOf("warning", n.ID, n.Source, say("passwords.no-rate-limit", "endpoint", n.ID, "store", e.To)))
	}
	return out
}

// unlimited reports whether n is an endpoint no rate limit guards:
// kit.RateLimit or kit.RateLimitPerClient in its pipeline.
func unlimited(n *model.Node) bool {
	if n == nil || n.Kind != model.KindEndpoint || n.Endpoint == nil {
		return false
	}
	return !slices.ContainsFunc(n.Endpoint.Pipeline, func(m model.Mechanic) bool { return m.Kind == "ratelimit" })
}
