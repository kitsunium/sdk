// Package kit — the interfaces of nodes: what every declaration is, and the
// roles some play.
package kit

import (
	"context"

	"github.com/kitsunium/sdk/framework/model"
)

// node is implemented by every building block.
type node interface {
	base() *nodeBase
	// describe fills the node's kind-specific description and reports the
	// edges that exist by construction.
	describe(a *App, out *model.Node) []model.Edge
}

// mounter is a node that serves HTTP routes. mount returns a problem to
// report, or "".
type mounter interface {
	mount(a *App, mux *routes) phrase
}

// starter is a node with something to bring up and take down.
type starter interface {
	start(ctx context.Context, a *App) error
	stop(ctx context.Context, a *App) error
}
