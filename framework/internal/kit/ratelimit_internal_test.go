package kit

import (
	"context"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
)

func admit(context.Context) error { return nil }

// One client emptying its bucket must not stop another: a shared bucket on
// a sign-in endpoint would let anyone lock everybody out.
func TestEachClientHasItsOwnBucket(t *testing.T) {
	l := newClientLimiter(0.001, 2, 100, time.Minute, nil)
	ann := WithUser(context.Background(), "ann", struct{}{})
	bob := WithUser(context.Background(), "bob", struct{}{})
	for range 2 {
		if err := l.Run(ann, admit); err != nil {
			t.Fatalf("ann's call within her burst was refused: %v", err)
		}
	}
	if err := l.Run(ann, admit); err == nil {
		t.Fatal("ann's third call in a burst of two was admitted")
	}
	if err := l.Run(bob, admit); err != nil {
		t.Fatalf("bob was limited by ann's calls: %v", err)
	}
	if clientKey(context.Background()) != "in-process" {
		t.Error("a call with no user and no request is not keyed as in-process")
	}
}

// Buckets are bounded in number, and an idle one is forgotten: a client
// whose bucket was dropped comes back with a full one.
func TestClientBucketsAreBounded(t *testing.T) {
	clk := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	l := newClientLimiter(0.0001, 1, 3, time.Minute, clk)
	as := func(u UID) context.Context { return WithUser(context.Background(), u, 0) }
	if err := l.Run(as("a"), admit); err != nil {
		t.Fatal(err)
	}
	if err := l.Run(as("a"), admit); err == nil {
		t.Fatal("a's second call in a burst of one was admitted")
	}
	for _, u := range []UID{"b", "c", "d"} {
		if err := l.Run(as(u), admit); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Run(as("a"), admit); err != nil {
		t.Fatalf("a's bucket outlived the bound of 3: %v", err)
	}
	if err := l.Run(as("d"), admit); err == nil {
		t.Fatal("d's bucket was dropped although it was among the 3 most recent")
	}
	clk.Advance(2 * time.Minute)
	if err := l.Run(as("d"), admit); err != nil {
		t.Fatalf("d's bucket was not forgotten after going idle: %v", err)
	}
}
