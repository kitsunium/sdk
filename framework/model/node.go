// A node of the graph: one building block, its identity, where it was
// declared, and the details of its kind.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

type (
	// Node is one building block.
	// Its ID is its identity; exactly one of the kind-specific fields matching
	// Kind is set.
	Node = core.NodeEntity
)
