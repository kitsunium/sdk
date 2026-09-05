// Package net_test — the generic handler decorator.
package net_test

import (
	"context"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// tag returns a middleware that appends its name on the way in, so the final
// string records the order the middlewares actually ran in.
func tag(log *strings.Builder, name string) corenet.Middleware[corenet.ConnHandler] {
	return func(next corenet.ConnHandler) corenet.ConnHandler {
		return corenet.ConnHandlerFunc(func(ctx context.Context, c corenet.Conn) error {
			log.WriteString(name)
			return next.ServeConn(ctx, c)
		})
	}
}

// TestChain pins the ordering contract and the two edges around it.
//
// Chain(h, a, b, c) must yield a(b(c(h))): the first middleware listed is the
// first to see a connection, because that is the only ordering a reader guesses
// correctly from the call site. A nil entry is skipped rather than panicking at
// serve time — conditionally-built middleware lists are common, and a panic
// there surfaces as a dead connection rather than a clear error.
//
// The packet cases pin the reason Middleware is generic at all: one Chain has
// to serve both handler natures, or the domain would carry two near-identical
// middleware types that inevitably drift.
func TestChain(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: names to wrap the base handler with; an empty entry means a nil
		//: middleware, which must be skipped.
		names []string
		want  string
	}
	tests := []tc{
		{"no middleware at all", nil, "h"},
		{"a single middleware", []string{"a"}, "ah"},
		{"three, outermost first", []string{"a", "b", "c"}, "abch"},
		{"a nil entry between two real ones", []string{"", "a", ""}, "ah"},
		{"only nil entries", []string{"", ""}, "h"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()

		var streamLog strings.Builder
		streamBase := corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error {
			streamLog.WriteString("h")
			return nil
		})
		streamMW := make([]corenet.Middleware[corenet.ConnHandler], 0, len(c.names))
		for _, name := range c.names {
			//: an empty name stands for a nil middleware.
			if name == "" {
				streamMW = append(streamMW, nil)
				continue
			}
			streamMW = append(streamMW, tag(&streamLog, name))
		}
		if err := corenet.Chain(corenet.ConnHandler(streamBase), streamMW...).ServeConn(t.Context(), nil); err != nil {
			t.Fatalf("ServeConn = %v, want nil", err)
		}
		if got := streamLog.String(); got != c.want {
			t.Errorf("stream order = %q, want %q", got, c.want)
		}

		//: the same table, through the datagram nature, from the same Chain.
		var packetLog strings.Builder
		packetBase := corenet.PacketHandlerFunc(func(context.Context, corenet.Packet) error {
			packetLog.WriteString("h")
			return nil
		})
		wrap := func(name string) corenet.Middleware[corenet.PacketHandler] {
			return func(next corenet.PacketHandler) corenet.PacketHandler {
				return corenet.PacketHandlerFunc(func(ctx context.Context, p corenet.Packet) error {
					packetLog.WriteString(name)
					return next.ServePacket(ctx, p)
				})
			}
		}
		packetMW := make([]corenet.Middleware[corenet.PacketHandler], 0, len(c.names))
		for _, name := range c.names {
			if name == "" {
				packetMW = append(packetMW, nil)
				continue
			}
			packetMW = append(packetMW, wrap(name))
		}
		if err := corenet.Chain(corenet.PacketHandler(packetBase), packetMW...).ServePacket(t.Context(), nil); err != nil {
			t.Fatalf("ServePacket = %v, want nil", err)
		}
		if got := packetLog.String(); got != c.want {
			t.Errorf("packet order = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
