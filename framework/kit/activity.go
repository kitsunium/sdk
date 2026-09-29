// Package kit — activities: what a daemon's idle stop waits for besides its
// connections, a notion of the product's own — sessions still open, work
// still queued.
package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Activity is a declared activity of a service: a function that says
// whether the product is busy. A daemon's IdleStop waits until every
// activity of its mounted services says false AND no connection is open on
// its listeners.
type Activity = ikit.ActivityHandler
