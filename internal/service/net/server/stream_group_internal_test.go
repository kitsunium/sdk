// Package server — a group of listeners sharing one handler and policy.
package server

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_StreamGroup_resolved pins the three things composition has to get right.
//
// The chain is built ONCE, at Start, so it costs nothing per connection. The
// order is the one the declaration reads: the first middleware listed is the
// first to see a connection, which is the only ordering a reader will guess
// correctly. And a group carrying a TLS identity is wrapped in the handshake
// bound while a plaintext one is not wrapped at all — a group with no
// negotiation to bound must pay nothing for the bound.
func Test_StreamGroup_resolved(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// labels name the middlewares, in declaration order.
		labels []string
		// secured gives the group a TLS identity.
		secured bool
	}
	tests := []tc{
		{name: "a plaintext group with no middleware"},
		{name: "a plaintext group with one middleware", labels: []string{"a"}},
		{name: "a plaintext group with several middlewares", labels: []string{"outer", "middle", "inner"}},
		{name: "a secured group", secured: true},
		{name: "a secured group with middlewares", labels: []string{"outer", "inner"}, secured: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var order []string
		group := &StreamGroup{name: "api"}
		group.HandleFunc(func(context.Context, corenet.Conn) error {
			order = append(order, "handler")
			return nil
		})
		for _, label := range c.labels {
			group.Use(func(next corenet.ConnHandler) corenet.ConnHandler {
				return corenet.ConnHandlerFunc(func(ctx context.Context, conn corenet.Conn) error {
					order = append(order, label)
					return next.ServeConn(ctx, conn)
				})
			})
		}
		if c.secured {
			group.identity = testIdentity(t)
		}

		resolved := group.resolved()

		//: a plaintext group is not wrapped at all, so it pays nothing for a
		//: bound it has no negotiation to apply.
		_, wrapped := resolved.(handshakeHandler)
		if wrapped != c.secured {
			t.Fatalf("the resolved handler is a %T, want wrapped = %v", resolved, c.secured)
		}
		//: boundHandshake is a no-op on a plaintext socket, so the chain runs
		//: either way and the order is observable in both shapes.
		if err := resolved.ServeConn(t.Context(), &conn{Conn: &fakeSocket{}}); err != nil {
			t.Fatalf("ServeConn = %v, want nil", err)
		}
		want := append(slices.Clone(c.labels), "handler")
		//: the first middleware listed is the first to see the connection, which is
		//: the only ordering a reader will guess correctly.
		if !slices.Equal(order, want) {
			t.Fatalf("the chain ran %v, want %v", order, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_StreamGroup_resolved_HandsTheAdapterItsBounds pins WHEN the HTTP adapter
// learns the group's deadlines.
//
// It has to be at Start: that is the first moment every option is certainly
// applied, and still before any accept loop exists to read them. Reading them at
// declaration would miss an option applied afterwards, and net/http left at zero
// bounds nothing at all — so a single slow reader could hold a request open for
// as long as it liked on an otherwise bounded group.
func Test_StreamGroup_resolved_HandsTheAdapterItsBounds(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// read, write and idle are the group's budgets.
		read  time.Duration
		write time.Duration
		idle  time.Duration
	}
	tests := []tc{
		{name: "no bounds configured"},
		{name: "a read bound", read: time.Second},
		{name: "every bound", read: time.Second, write: 2 * time.Second, idle: 3 * time.Second},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		group := &StreamGroup{name: "api"}
		group.HandleHTTP(http.NotFoundHandler())
		//: the option is applied AFTER the handler, which is the ordering that
		//: makes reading the bounds at declaration time wrong.
		group.timeouts = timeouts(c.read, c.write, c.idle)

		group.resolved()

		if group.httpAdapter.timeouts != group.timeouts {
			t.Fatalf("the adapter carries %+v, want %+v — net/http left at zero "+
				"bounds nothing at all", group.httpAdapter.timeouts, group.timeouts)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
