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

// TestChainAppliesOutermostFirst pins the ordering contract. Chain(h, a, b, c)
// must yield a(b(c(h))): the first middleware listed is the first to see a
// connection, because that is the only ordering a reader guesses correctly from
// the call site.
func TestChainAppliesOutermostFirst(t *testing.T) {
	t.Parallel()
	var log strings.Builder
	base := corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error {
		log.WriteString("h")
		return nil
	})
	chained := corenet.Chain[corenet.ConnHandler](base, tag(&log, "a"), tag(&log, "b"), tag(&log, "c"))
	if err := chained.ServeConn(t.Context(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := log.String(); got != "abch" {
		t.Fatalf("order = %q, want \"abch\"", got)
	}
}

// TestChainSkipsNilMiddleware pins that a nil entry is skipped rather than
// panicking at serve time — a conditionally-built middleware list is common and
// a panic there would surface as a dead connection, not a clear error.
func TestChainSkipsNilMiddleware(t *testing.T) {
	t.Parallel()
	var log strings.Builder
	base := corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error {
		log.WriteString("h")
		return nil
	})
	chained := corenet.Chain[corenet.ConnHandler](base, nil, tag(&log, "a"), nil)
	if err := chained.ServeConn(t.Context(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := log.String(); got != "ah" {
		t.Fatalf("order = %q, want \"ah\"", got)
	}
}

// TestChainWithNoMiddlewareReturnsTheHandler pins the identity case.
func TestChainWithNoMiddlewareReturnsTheHandler(t *testing.T) {
	t.Parallel()
	var log strings.Builder
	base := corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error {
		log.WriteString("h")
		return nil
	})
	if err := corenet.Chain[corenet.ConnHandler](base).ServeConn(t.Context(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := log.String(); got != "h" {
		t.Fatalf("order = %q, want \"h\"", got)
	}
}

// TestChainWorksForPacketHandlersToo pins that one generic Chain serves both
// handler natures — the reason Middleware is generic at all.
func TestChainWorksForPacketHandlersToo(t *testing.T) {
	t.Parallel()
	var log strings.Builder
	wrap := func(name string) corenet.Middleware[corenet.PacketHandler] {
		return func(next corenet.PacketHandler) corenet.PacketHandler {
			return corenet.PacketHandlerFunc(func(ctx context.Context, p corenet.Packet) error {
				log.WriteString(name)
				return next.ServePacket(ctx, p)
			})
		}
	}
	base := corenet.PacketHandlerFunc(func(context.Context, corenet.Packet) error {
		log.WriteString("h")
		return nil
	})
	chained := corenet.Chain[corenet.PacketHandler](base, wrap("a"), wrap("b"))
	if err := chained.ServePacket(t.Context(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := log.String(); got != "abh" {
		t.Fatalf("order = %q, want \"abh\"", got)
	}
}
