package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Activity is a declared activity of a service: a function that says
// whether the product is busy. A daemon's IdleStop waits until every
// activity of its mounted services says false AND no connection is open on
// its listeners.
type Activity = iapp.ActivityHandler
