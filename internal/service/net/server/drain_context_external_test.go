package server_test

import (
	"context"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

func TestDrainContext_endsWithTheDrain(t *testing.T) {
	drain := make(chan struct{})
	ctx, cancel := server.DrainContext(corenet.WithDrainSignal(context.Background(), drain))
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("cancelled before the drain")
	default:
	}
	close(drain)
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the drain did not cancel the context")
	}
}

func TestDrainContext_endsWithItsParent(t *testing.T) {
	parent, stop := context.WithCancel(corenet.WithDrainSignal(context.Background(), make(chan struct{})))
	ctx, cancel := server.DrainContext(parent)
	defer cancel()
	stop()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the parent's end did not reach the context")
	}
}

func TestDrainContext_withoutSignal(t *testing.T) {
	ctx, cancel := server.DrainContext(context.Background())
	if ctx.Err() != nil {
		t.Fatal("a context with no drain signal starts cancelled")
	}
	cancel()
	if ctx.Err() == nil {
		t.Fatal("cancel did not end the context")
	}
}
