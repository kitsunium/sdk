package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// Catalog lists the generic mechanics kit offers: the building blocks a node
// composes rather than implements, each one backed by an SDK package. It is
// part of every graph, so the Studio — and an AI agent reading the graph —
// knows what can be added to a product and the exact code that adds it.
func Catalog() []model.Mechanic {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Catalog()
}

// NewID returns a new identifier with the given prefix: a TypeID, time
// ordered, like "todo_01k5zq7m3xe8tvbfg0s7zr4w6c". The prefix must be one to
// sixty-three lower-case letters, with '_' only between two letters; any
// other prefix is a programming error and panics.
func NewID(prefix string) string {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.NewID(prefix)
}
