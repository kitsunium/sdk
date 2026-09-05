// Package server — a group of datagram listeners.
package server

import (
	"context"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_PacketGroup_resolved pins that the middleware chain is composed ONCE, at
// Start, and in the order the declaration reads.
//
// Composing per datagram would put an allocation on the hot path the batched
// reader exists to keep free, and the order is what a reader has to be able to
// guess correctly: the first middleware listed is the first to see a datagram.
func Test_PacketGroup_resolved(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// labels name the middlewares, in declaration order.
		labels []string
	}
	tests := []tc{
		{name: "no middleware at all"},
		{name: "one middleware", labels: []string{"a"}},
		{name: "several middlewares", labels: []string{"outer", "middle", "inner"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var order []string
		group := &PacketGroup{name: "sip"}
		group.HandleFunc(func(context.Context, corenet.Packet) error {
			order = append(order, "handler")
			return nil
		})
		for _, label := range c.labels {
			group.Use(func(next corenet.PacketHandler) corenet.PacketHandler {
				return corenet.PacketHandlerFunc(func(ctx context.Context, p corenet.Packet) error {
					order = append(order, label)
					return next.ServePacket(ctx, p)
				})
			})
		}

		resolved := group.resolved()

		if resolved == nil {
			t.Fatal("resolved returned no handler")
		}
		if err := resolved.ServePacket(t.Context(), &packet{conn: &fakePacketConn{}}); err != nil {
			t.Fatalf("ServePacket = %v, want nil", err)
		}
		want := append(append([]string(nil), c.labels...), "handler")
		if len(order) != len(want) {
			t.Fatalf("the chain ran %v, want %v", order, want)
		}
		//: the first middleware listed is the first to see the datagram, which
		//: is the only ordering a reader will guess correctly.
		for i := range want {
			if order[i] != want[i] {
				t.Fatalf("the chain ran %v, want %v", order, want)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
