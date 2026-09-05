// Package net_test — the datagram handler port adapter.
package net_test

import (
	"context"
	"errors"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_PacketHandlerFunc_ServePacket pins the adapter: it must call the wrapped
// function exactly once, hand it the caller's own context and packet, and
// return whatever the function returned untouched. A datagram service keeps
// reading after a handler error, so that return value is the only record the
// packet was rejected.
func Test_PacketHandlerFunc_ServePacket(t *testing.T) {
	t.Parallel()
	handlerErr := errors.New("the handler refused this datagram")
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
		var gotPacket corenet.Packet

		//: a distinguishable context so the adapter cannot pass a fresh one and
		//: still look correct.
		type ctxKey struct{}
		ctx := context.WithValue(t.Context(), ctxKey{}, "marker")

		var h corenet.PacketHandler = corenet.PacketHandlerFunc(
			func(fnCtx context.Context, p corenet.Packet) error {
				calls++
				gotCtx = fnCtx
				gotPacket = p
				return c.ret
			})

		//: a nil Packet is what the adapter itself does with the argument: pass
		//: it through. The adapter must not consult it.
		err := h.ServePacket(ctx, nil)

		if !errors.Is(err, c.ret) {
			t.Errorf("ServePacket = %v, want %v", err, c.ret)
		}
		if calls != 1 {
			t.Errorf("the wrapped function ran %d times, want 1", calls)
		}
		if gotCtx != ctx {
			t.Error("the wrapped function received a different context")
		}
		if gotPacket != nil {
			t.Errorf("the wrapped function received %v, want the caller's nil Packet", gotPacket)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
