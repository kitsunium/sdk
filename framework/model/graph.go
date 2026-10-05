// The graph's identities and its canonical form: node and edge IDs,
// Normalize, Merge and the files a graph names.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// NodeID builds the ID of a node declared in a service. A service's own ID is
// its name.
func NodeID(service string, kind NodeKind, name string) string {
	return core.NodeID(service, kind, name)
}

// edgeID is EdgeID's body: decl_gen.go writes EdgeID, from the
// design, as one call of it.
func edgeID(from string, kind EdgeKind, to, label string) string {
	return core.EdgeID(from, kind, to, label)
}

// compareSource is CompareSource's body: decl_gen.go writes CompareSource, from the
// design, as one call of it.
func compareSource(a, b Source) int {
	return core.CompareSource(a, b)
}

// merge is Merge's body: decl_gen.go writes Merge, from the
// design, as one call of it.
func merge(base, extra *Graph) *Graph {
	return core.Merge(base, extra)
}

// serviceOf is ServiceOf's body: decl_gen.go writes ServiceOf, from the
// design, as one call of it.
func serviceOf(id string) string {
	return core.ServiceOf(id)
}
