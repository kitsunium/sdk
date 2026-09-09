// Package net_test — the shutdown signal a long-lived handler observes.
package net_test

import (
	"context"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestDrainSignalRoundTrip pins the carrier, and pins the absent case as a nil
// channel rather than an error or a second return value.
//
// The nil is the point: receiving from a nil channel blocks forever, so a
// handler that selects on this alongside its other cases needs no nil check and
// no branch. An absent signal simply never fires — which is the correct
// behaviour on a server that does not publish one.
func TestDrainSignalRoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		attach  bool
		wantNil bool
	}
	tests := []tc{
		{name: "a published signal comes back", attach: true},
		{name: "no signal published yields a nil channel", wantNil: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx := context.Background()
		published := make(chan struct{})
		if c.attach {
			ctx = corenet.WithDrainSignal(ctx, published)
		}
		got := corenet.DrainSignal(ctx)
		if c.wantNil {
			if got != nil {
				t.Fatalf("DrainSignal() = %v, want nil", got)
			}
			return
		}
		if got == nil {
			t.Fatalf("DrainSignal() = nil, want the published channel")
		}
		select {
		case <-got:
			t.Fatalf("DrainSignal() reported a drain before one started")
		default:
		}
		close(published)
		//: a closed channel must be observable through the context, or the
		//: signal reaches nobody.
		<-got
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDrainSignalIgnoresAForeignValue pins that the key is private: a context
// carrying some other package's value under some other key must not be mistaken
// for a drain signal. The typed key is what guarantees it, and a string key
// would not have.
func TestDrainSignalIgnoresAForeignValue(t *testing.T) {
	t.Parallel()
	type foreignKey struct{}
	ctx := context.WithValue(context.Background(), foreignKey{}, make(chan struct{}))
	if got := corenet.DrainSignal(ctx); got != nil {
		t.Fatalf("DrainSignal() = %v on a foreign value, want nil", got)
	}
}
