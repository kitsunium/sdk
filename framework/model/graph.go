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

// EdgeID builds the ID of an edge. The label is part of the identity: two
// transitions of one workflow fired from one endpoint are two edges.
func EdgeID(from string, kind EdgeKind, to, label string) string {
	return core.EdgeID(from, kind, to, label)
}

// CompareSource orders sources by root, file, then line.
func CompareSource(a, b Source) int {
	return core.CompareSource(a, b)
}

// Merge enriches a graph with what another description of the same product
// knows. base is authoritative on which nodes exist — a runtime graph describes
// what the process actually serves —, on the modules it mounts, on what each
// of its ports calls and on which stores feed each of its watches: the app
// chose them when it started, where the analysis only reads the same rules
// over the whole module. extra, typically
// the static analysis, contributes documentation, handler ranges — an
// authorization function's too —, transition callers and the edges it
// found. Nodes only extra knows about are dropped: code that is compiled but
// not mounted in this app is not part of this product — save a node of a
// module's service the base mounts, which a package the binary does not link
// declares: a warning says so.
func Merge(base, extra *Graph) *Graph {
	return core.Merge(base, extra)
}

// ServiceOf returns the service part of a node ID.
func ServiceOf(id string) string {
	return core.ServiceOf(id)
}
