package topic_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/topic"
)

// drain spawns ONE reader goroutine that keeps sub up to date for the duration
// of the benchmark, so what is measured is a working fan-out rather than a
// saturated one.
//
// Lifecycle: the goroutine starts here and returns when the b.Cleanup registered
// below closes stop; that cleanup then blocks on done, so the goroutine is
// joined before the benchmark binary moves on and cannot leak into the next
// case's measurement.
func drain[T any](b *testing.B, sub *topic.Listener[T]) {
	b.Helper()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case _, ok := <-sub.Values():
				//: the value channel is never closed by design, so !ok can only
				//: mean the invariant broke; leaving beats spinning on it.
				if !ok {
					return
				}
			case <-stop:
				return
			}
		}
	}()
	b.Cleanup(func() {
		close(stop)
		<-done
	})
}

// BenchmarkPublish_NoSubscribers is the floor: one atomic load and a nil check.
// It is what a topic costs when nobody is listening, which is the common state
// of a diagnostic channel in production.
func BenchmarkPublish_NoSubscribers(b *testing.B) {
	var board topic.Topic[int]
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		board.Publish(ctx, i)
	}
}

// BenchmarkPublish_OneDrainedSubscriber is the realistic single-consumer shape:
// a subscriber that keeps up, so every value lands. It is the number to compare
// against BenchmarkBaseline_DirectChannelSend.
func BenchmarkPublish_OneDrainedSubscriber(b *testing.B) {
	var board topic.Topic[int]
	sub := board.Subscribe(topic.DropNewest(1024))
	drain(b, sub)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		board.Publish(ctx, i)
	}
}

// BenchmarkPublish_EightDrainedSubscribers is the fan-out. Divided by eight it
// gives the marginal cost of one more subscriber, which is what decides whether
// a topic scales to the membership a caller has in mind.
func BenchmarkPublish_EightDrainedSubscribers(b *testing.B) {
	var board topic.Topic[int]
	for range 8 {
		drain(b, board.Subscribe(topic.DropNewest(1024)))
	}
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		board.Publish(ctx, i)
	}
}

// BenchmarkPublish_SaturatedDropNewest is the cheap overflow path: one
// non-blocking send that fails, one counter increment. No reader at all, so
// every iteration after the first is a drop.
func BenchmarkPublish_SaturatedDropNewest(b *testing.B) {
	var board topic.Topic[int]
	board.Subscribe(topic.DropNewest(1))
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		board.Publish(ctx, i)
	}
}

// BenchmarkPublish_SaturatedDropOldest is the expensive overflow path: a mutex,
// a failed send, a receive, and a second send. The gap against DropNewest is
// what keeping the FRESHEST window costs when a subscriber is permanently
// behind.
func BenchmarkPublish_SaturatedDropOldest(b *testing.B) {
	var board topic.Topic[int]
	board.Subscribe(topic.DropOldest(1))
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		board.Publish(ctx, i)
	}
}

// BenchmarkSubscribeUnsubscribe is the copy-on-write price. Membership changes
// rebuild the whole list, which is the trade that makes Publish a single
// atomic load — so this number is the one that says how large a membership can
// churn before the trade stops paying.
func BenchmarkSubscribeUnsubscribe(b *testing.B) {
	var board topic.Topic[int]
	//: a resident membership, so each churn copies a realistic list rather
	//: than a one-element one.
	for range 16 {
		board.Subscribe(topic.DropNewest(1))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		board.Subscribe(topic.DropNewest(1)).Unsubscribe()
	}
}

// BenchmarkBaseline_DirectChannelSend is one buffered channel, drained by one
// goroutine, with no topic at all. Every number above is only meaningful
// against it: a Topic is worth its overhead when the fan-out, the departure
// safety and the counted drops are worth this difference.
//
// Lifecycle: the reader goroutine starts here and returns when the registered
// b.Cleanup closes stop; that cleanup blocks on done, so it is joined before the
// benchmark ends.
func BenchmarkBaseline_DirectChannelSend(b *testing.B) {
	values := make(chan int, 1024)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case _, ok := <-values:
				//: the baseline owns this channel and never closes it; the guard
				//: keeps the loop from spinning if that ever changes.
				if !ok {
					return
				}
			case <-stop:
				return
			}
		}
	}()
	b.Cleanup(func() {
		close(stop)
		<-done
	})
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		select {
		case values <- i:
		default:
		}
	}
}
