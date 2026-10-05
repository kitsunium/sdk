// The architecture as a Mermaid flowchart.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// mermaid is Mermaid's body: decl_gen.go writes Mermaid, from the
// design, as one call of it.
func mermaid(g *Graph) string {
	return core.Mermaid(g)
}
