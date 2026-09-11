package topic_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/topic"
)

// TestEverySubscriberReceivesEveryValue is the broadcast contract: a fan-out,
// not a queue — nobody steals another subscriber's copy.
func TestEverySubscriberReceivesEveryValue(t *testing.T) {
	t.Parallel()
	var board topic.Topic[int]
	subs := make([]*topic.Listener[int], 3)
	for i := range subs {
		subs[i] = board.Subscribe(topic.Block(8))
	}
	for v := range 5 {
		if delivered := board.Publish(t.Context(), v); delivered != 3 {
			t.Fatalf("Publish(%d) delivered to %d subscribers, want 3", v, delivered)
		}
	}
	for i, sub := range subs {
		for want := range 5 {
			got := <-sub.Values()
			if got != want {
				t.Fatalf("subscriber %d received %d, want %d — order must follow publication", i, got, want)
			}
		}
		if dropped := sub.Dropped(); dropped != 0 {
			t.Fatalf("subscriber %d dropped %d, want 0", i, dropped)
		}
	}
}

// TestTheZeroTopicIsUsable checks the sync.Mutex-style contract: no
// constructor, no initialisation step to forget.
func TestTheZeroTopicIsUsable(t *testing.T) {
	t.Parallel()
	var board topic.Topic[string]
	if got := board.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() on a fresh Topic = %d, want 0", got)
	}
	if got := board.Publish(t.Context(), "nobody is listening"); got != 0 {
		t.Fatalf("Publish() with no subscribers delivered %d, want 0", got)
	}
	sub := board.Subscribe(topic.DropNewest(1))
	if got := board.Subscribers(); got != 1 {
		t.Fatalf("Subscribers() = %d, want 1", got)
	}
	board.Publish(t.Context(), "hello")
	if got := <-sub.Values(); got != "hello" {
		t.Fatalf("received %q, want %q", got, "hello")
	}
}

// TestSubscribeWithoutADeliveryConfigRefuses is ADR 0031 on this primitive.
// Whatever a zero Delivery meant it would be a backpressure decision the caller
// never made, and both candidates are the failures the package exists to
// prevent — so the zero is refused at the call that omitted it.
func TestSubscribeWithoutADeliveryConfigRefuses(t *testing.T) {
	t.Parallel()
	defer func() {
		raised := recover()
		if raised == nil {
			t.Fatal("Subscribe(Delivery{}) returned a subscription, want a refusal")
		}
		message, ok := raised.(string)
		if !ok {
			t.Fatalf("recovered %T, want a string message", raised)
		}
		if !strings.Contains(message, "explicit delivery policy") {
			t.Fatalf("panic message = %q, want it to name the missing decision", message)
		}
		for _, builder := range []string{"topic.Block", "topic.DropOldest", "topic.DropNewest"} {
			if !strings.Contains(message, builder) {
				t.Fatalf("panic message = %q, want it to name %s", message, builder)
			}
		}
	}()
	var board topic.Topic[int]
	var unset topic.DeliveryConfig
	board.Subscribe(unset)
}

// TestDropNewestKeepsTheEarliestWindowAndCountsTheLoss covers the policy for a
// log: the first values describe how a burst began, and the discards are
// counted rather than silent.
func TestDropNewestKeepsTheEarliestWindowAndCountsTheLoss(t *testing.T) {
	t.Parallel()
	var board topic.Topic[int]
	sub := board.Subscribe(topic.DropNewest(3))
	for v := range 10 {
		board.Publish(t.Context(), v)
	}
	for want := range 3 {
		if got := <-sub.Values(); got != want {
			t.Fatalf("received %d, want %d — DropNewest keeps the EARLIEST window", got, want)
		}
	}
	if dropped := sub.Dropped(); dropped != 7 {
		t.Fatalf("Dropped() = %d, want 7 — a drop policy that does not count is a silent one", dropped)
	}
}

// TestDropOldestKeepsTheFreshestWindowAndCountsTheLoss covers the policy for
// state, where an old value is worse than no value.
func TestDropOldestKeepsTheFreshestWindowAndCountsTheLoss(t *testing.T) {
	t.Parallel()
	var board topic.Topic[int]
	sub := board.Subscribe(topic.DropOldest(3))
	for v := range 10 {
		board.Publish(t.Context(), v)
	}
	for _, want := range []int{7, 8, 9} {
		if got := <-sub.Values(); got != want {
			t.Fatalf("received %d, want %d — DropOldest keeps the FRESHEST window", got, want)
		}
	}
	if dropped := sub.Dropped(); dropped != 7 {
		t.Fatalf("Dropped() = %d, want 7", dropped)
	}
}

// TestBlockIsBoundedByThePublishersContext is what makes "Block" a coupling
// rather than a hang: the producer keeps its own escape hatch, and the value it
// gave up on is counted as lost.
func TestBlockIsBoundedByThePublishersContext(t *testing.T) {
	t.Parallel()
	var board topic.Topic[int]
	sub := board.Subscribe(topic.Block(1))
	//: fill the one slot, so the next Publish must wait.
	if delivered := board.Publish(t.Context(), 1); delivered != 1 {
		t.Fatalf("first Publish delivered %d, want 1", delivered)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if delivered := board.Publish(ctx, 2); delivered != 0 {
		t.Fatalf("blocked Publish delivered %d, want 0", delivered)
	}
	if elapsed := time.Since(start); elapsed < 10*time.Millisecond {
		t.Fatalf("blocked Publish returned after %v, want it to have actually waited", elapsed)
	}
	if dropped := sub.Dropped(); dropped != 1 {
		t.Fatalf("Dropped() = %d, want 1 — a value the producer gave up on IS a loss", dropped)
	}
}

// TestUnsubscribeReleasesAPublisherBlockedOnThatSubscriber is the deadlock this
// design exists to make impossible: the subscriber goroutine that stopped
// reading is usually the one that unsubscribes, so an unsubscribe that waited
// on the delivery it must cancel would never complete.
//
// Lifecycle: the single publisher goroutine returns when its Publish does, and
// the test joins it by receiving from `returned`.
func TestUnsubscribeReleasesAPublisherBlockedOnThatSubscriber(t *testing.T) {
	t.Parallel()
	var board topic.Topic[int]
	sub := board.Subscribe(topic.Block(1))
	board.Publish(t.Context(), 1)
	returned := make(chan int, 1)
	go func() { returned <- board.Publish(context.WithoutCancel(t.Context()), 2) }()
	//: no context deadline anywhere: if the unsubscribe did not release the
	//: publisher, this test hangs and the suite reports it as such.
	sub.Unsubscribe()
	if delivered := <-returned; delivered != 0 {
		t.Fatalf("Publish delivered %d, want 0 — the subscriber had left", delivered)
	}
	if dropped := sub.Dropped(); dropped != 0 {
		t.Fatalf("Dropped() = %d, want 0 — leaving is a departure, not a drop", dropped)
	}
}

// TestUnsubscribeMidFanOutDoesNotCostTheOthersTheirCopy is the property the
// whole design is arranged around. The interleaving is forced rather than
// hoped for: receiving the SECOND value from subscriber A proves the publisher
// has loaded the membership snapshot (which still contains B) and is now
// delivering, so unsubscribing B at that point is unambiguously mid-fan-out.
//
// Lifecycle: the one publisher goroutine started below returns as soon as its
// Publish does, and the test joins it by receiving its result from `returned`
// before finishing — so it cannot outlive the case.
func TestUnsubscribeMidFanOutDoesNotCostTheOthersTheirCopy(t *testing.T) {
	t.Parallel()
	var board topic.Topic[string]
	first := board.Subscribe(topic.Block(1))
	leaving := board.Subscribe(topic.Block(1))
	last := board.Subscribe(topic.Block(1))
	//: fill every buffer, so the second Publish blocks on each in turn.
	if delivered := board.Publish(t.Context(), "one"); delivered != 3 {
		t.Fatalf("first Publish delivered %d, want 3", delivered)
	}
	returned := make(chan int, 1)
	go func() { returned <- board.Publish(context.WithoutCancel(t.Context()), "two") }()

	if got := <-first.Values(); got != "one" {
		t.Fatalf("first subscriber received %q, want %q", got, "one")
	}
	//: this receive can only complete after the publisher has sent to the
	//: first subscriber, i.e. after it loaded a snapshot containing `leaving`.
	if got := <-first.Values(); got != "two" {
		t.Fatalf("first subscriber received %q, want %q", got, "two")
	}
	leaving.Unsubscribe()

	//: the third subscriber must still get its copy, which is the point.
	if got := <-last.Values(); got != "one" {
		t.Fatalf("last subscriber received %q, want %q", got, "one")
	}
	if got := <-last.Values(); got != "two" {
		t.Fatalf("last subscriber received %q, want %q — a departure must not cost a bystander its copy", got, "two")
	}
	if delivered := <-returned; delivered != 2 {
		t.Fatalf("Publish delivered %d, want 2 (the two who stayed)", delivered)
	}
}

// TestConcurrentPublishAndUnsubscribeNeverPanics is the crude counterpart to
// the deterministic test above: under -race, hundreds of unsynchronised
// departures during live fan-out. A design that closed the value channel would
// fail here with "send on closed channel", in the publisher.
//
// Lifecycle: four publisher goroutines start below and return when stop is
// closed; wg.Wait joins all four before the assertions, so none outlives the
// test.
func TestConcurrentPublishAndUnsubscribeNeverPanics(t *testing.T) {
	t.Parallel()
	var board topic.Topic[int]
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				board.Publish(context.WithoutCancel(t.Context()), 1)
			}
		})
	}
	for range 200 {
		sub := board.Subscribe(topic.DropOldest(4))
		//: read a little, then leave in the middle of whatever is in flight.
		select {
		case <-sub.Values():
		default:
		}
		sub.Unsubscribe()
	}
	close(stop)
	wg.Wait()
	if got := board.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() = %d after every subscriber left, want 0", got)
	}
}

// TestCloseReleasesEverySubscriber checks the shutdown signal the value channel
// deliberately does not carry.
func TestCloseReleasesEverySubscriber(t *testing.T) {
	t.Parallel()
	var board topic.Topic[int]
	subs := make([]*topic.Listener[int], 3)
	for i := range subs {
		subs[i] = board.Subscribe(topic.Block(2))
	}
	board.Publish(t.Context(), 7)
	board.Close()
	for i, sub := range subs {
		select {
		case <-sub.Done():
		default:
			t.Fatalf("subscriber %d is not done after Close()", i)
		}
		//: already-delivered values stay readable — Close is not a drain.
		if got := <-sub.Values(); got != 7 {
			t.Fatalf("subscriber %d lost its buffered value, got %d", i, got)
		}
	}
	if got := board.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() after Close = %d, want 0", got)
	}
	//: idempotent: a second Close must not panic on an already-closed done.
	board.Close()
}

// TestSubscribeAfterCloseHandsBackADoneSubscription keeps a shutdown race
// reading as a shutdown rather than as a crash or a subscription that will
// never receive anything and never say so.
func TestSubscribeAfterCloseHandsBackADoneSubscription(t *testing.T) {
	t.Parallel()
	var board topic.Topic[int]
	board.Close()
	sub := board.Subscribe(topic.Block(1))
	select {
	case <-sub.Done():
	default:
		t.Fatal("Subscribe() on a closed Topic returned a live subscription")
	}
	if got := board.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() = %d, want 0 — a closed topic registers nobody", got)
	}
	if delivered := board.Publish(t.Context(), 1); delivered != 0 {
		t.Fatalf("Publish() on a closed Topic delivered %d, want 0", delivered)
	}
}

// TestUnsubscribeIsIdempotentAndSafeAfterClose covers the overlap that makes a
// deferred Unsubscribe safe to write next to a Topic that may shut down first.
func TestUnsubscribeIsIdempotentAndSafeAfterClose(t *testing.T) {
	t.Parallel()
	var board topic.Topic[int]
	sub := board.Subscribe(topic.DropNewest(1))
	sub.Unsubscribe()
	sub.Unsubscribe()
	board.Close()
	sub.Unsubscribe()
	if got := board.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() = %d, want 0", got)
	}
}

// TestANonPositiveCapacityStillHoldsOneValue is the clamp half of ADR 0031
// here: the buffer depth is a floor the SDK can supply, unlike the policy,
// which is the caller's intent.
func TestANonPositiveCapacityStillHoldsOneValue(t *testing.T) {
	t.Parallel()
	for _, capacity := range []int{0, -1, -64} {
		var board topic.Topic[int]
		sub := board.Subscribe(topic.DropNewest(capacity))
		if delivered := board.Publish(t.Context(), 42); delivered != 1 {
			t.Fatalf("capacity %d: Publish delivered %d, want 1 — a clamped buffer still holds one", capacity, delivered)
		}
		if got := <-sub.Values(); got != 42 {
			t.Fatalf("capacity %d: received %d, want 42", capacity, got)
		}
	}
}

// TestASubscriberThatKeepsUpNeverDrops guards against an over-eager eviction
// path: the drop counters must stay at zero for a reader that is not behind.
func TestASubscriberThatKeepsUpNeverDrops(t *testing.T) {
	t.Parallel()
	for _, delivery := range []topic.DeliveryConfig{topic.Block(4), topic.DropOldest(4), topic.DropNewest(4)} {
		var board topic.Topic[int]
		sub := board.Subscribe(delivery)
		for v := range 100 {
			if delivered := board.Publish(t.Context(), v); delivered != 1 {
				t.Fatalf("Publish(%d) delivered %d, want 1", v, delivered)
			}
			if got := <-sub.Values(); got != v {
				t.Fatalf("received %d, want %d", got, v)
			}
		}
		if dropped := sub.Dropped(); dropped != 0 {
			t.Fatalf("Dropped() = %d, want 0 for a subscriber that kept up", dropped)
		}
	}
}
