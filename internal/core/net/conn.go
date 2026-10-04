// Package net — the accepted stream connection port.
package net

import (
	"context"
)

// ConnHandlerFunc adapts a plain function to ConnHandler.
type ConnHandlerFunc func(ctx context.Context, c Conn) error

// ServeConn implements ConnHandler by calling f.
func (f ConnHandlerFunc) ServeConn(ctx context.Context, c Conn) error {
	//: the function IS the handler — nothing else to consult.
	return f(ctx, c)
}
