// Package net — the generic handler decorator.
package net

// Middleware decorates a handler with another of the same type.
//
// It is generic over the handler type so stream and datagram handlers share one
// shape and one Chain helper, instead of the domain carrying two near-identical
// middleware types that would inevitably drift.
type Middleware[H any] func(next H) H

// Chain applies middlewares to a handler, outermost first.
//
// Chain(h, a, b, c) yields a(b(c(h))): the first middleware listed is the first
// to see a connection. That ordering matches how the list reads on the page,
// which is the only ordering a reader will guess correctly.
func Chain[H any](h H, middlewares ...Middleware[H]) H {
	//: apply in reverse so the first entry ends up outermost.
	for i := len(middlewares) - 1; i >= 0; i-- {
		//: a nil entry is skipped rather than panicking at serve time.
		if middlewares[i] != nil {
			h = middlewares[i](h)
		}
	}
	//: the fully decorated handler.
	return h
}
