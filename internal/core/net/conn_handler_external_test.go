// Package net_test — the stream handler port adapter.
package net_test

import (
	"context"
	"errors"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_ConnHandlerFunc_ServeConn pins the adapter: it must call the wrapped
// function exactly once, hand it the caller's own context and connection, and
// return whatever the function returned untouched. Swallowing an error here
// would silence the one signal the server uses to decide a connection is done.
func Test_ConnHandlerFunc_ServeConn(t *testing.T) {
	t.Parallel()
	handlerErr := errors.New("the handler refused this connection")
	type tc struct {
		name string
		ret  error
	}
	tests := []tc{
		{"a handler that succeeds", nil},
		{"a handler that reports a failure", handlerErr},
		{"a handler that reports a cancelled context", context.Canceled},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		calls := 0
		var gotCtx context.Context
		var gotConn corenet.Conn

		//: a distinguishable context so the adapter cannot pass a fresh one and
		//: still look correct.
		type ctxKey struct{}
		ctx := context.WithValue(t.Context(), ctxKey{}, "marker")

		var h corenet.ConnHandler = corenet.ConnHandlerFunc(
			func(fnCtx context.Context, conn corenet.Conn) error {
				calls++
				gotCtx = fnCtx
				gotConn = conn
				return c.ret
			})

		//: a nil Conn is what the adapter itself does with the argument: pass
		//: it through. The adapter must not consult it.
		err := h.ServeConn(ctx, nil)

		if !errors.Is(err, c.ret) {
			t.Errorf("ServeConn = %v, want %v", err, c.ret)
		}
		if calls != 1 {
			t.Errorf("the wrapped function ran %d times, want 1", calls)
		}
		if gotCtx != ctx {
			t.Error("the wrapped function received a different context")
		}
		if gotConn != nil {
			t.Errorf("the wrapped function received %v, want the caller's nil Conn", gotConn)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
