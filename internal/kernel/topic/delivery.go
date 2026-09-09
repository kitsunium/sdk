// Package topic — the per-subscriber backpressure decision, made at the call
// site and impossible to leave unmade.
package topic

// minCapacity is the floor a subscriber's buffer is clamped to.
//
// A zero-capacity buffer turns every delivery into a rendezvous: under a drop
// policy nothing lands unless a receiver happens to be parked at that instant,
// which is a subscription that mostly discards its topic — inert in the sense
// ADR 0031 names. One is a floor, not a guess at intent: "hold at least one
// value" is the smallest subscription that is still a subscription, exactly as
// the SDK's rate limiter clamps Burst to "admit at least one".
const minCapacity int = 1

// mode is the discriminant of a [DeliveryConfig]. It is unexported so the only
// way to name one is through [Block] / [DropOldest] / [DropNewest].
type mode uint8

const (
	// modeUnset is the zero value, and is refused by Subscribe. It exists so
	// that "the caller never chose" is a state the code can SEE, rather than
	// one that silently reads as whichever policy happened to be first.
	modeUnset mode = iota
	// modeBlock waits for room.
	modeBlock
	// modeDropOldest discards the stalest buffered value to make room.
	modeDropOldest
	// modeDropNewest discards the arriving value.
	modeDropNewest
)

// DeliveryConfig is the answer to one question, asked at every
// [Topic.Subscribe]: what happens when this subscriber is not keeping up?
//
// There is no usable zero value, and that is the design. Its fields are
// unexported so a DeliveryConfig{} literal cannot be written, and a
// `var p DeliveryConfig` is refused by Subscribe rather than silently picking a
// backpressure policy on the caller's behalf — ADR 0031's "a zero value is a
// safe default or an explicit refusal, never an inert policy", applied where no
// safe default exists because either candidate is a documented failure mode.
//
// Build one with [Block], [DropOldest] or [DropNewest].
type DeliveryConfig struct {
	// mode decides what a full buffer means.
	mode mode
	// capacity is the subscriber's buffer depth, at least minCapacity.
	capacity int
}

// Block makes a full subscriber buffer stall the publisher until room appears.
//
// It is the honest choice when every value matters more than the producer's
// latency — and it is a real coupling: one subscriber that stops reading holds
// up the fan-out to everyone visited after it. Two things keep that from being
// a hang: [Topic.Publish] takes a context that bounds the wait, and a
// subscriber that unsubscribes releases the publisher immediately.
//
// capacity is the buffer depth; anything below 1 is clamped to 1.
func Block(capacity int) DeliveryConfig {
	//: the policy is spelled at the call site; that is the whole point.
	return DeliveryConfig{mode: modeBlock, capacity: atLeastMinimum(capacity)}
}

// DropOldest keeps the FRESHEST window: when the buffer is full the stalest
// value is discarded to make room for the arriving one.
//
// It is the choice for state — a metric, a health status, a position — where an
// old value is worse than no value. Every discard is counted in
// [Listener.Dropped], which is what makes the loss visible rather than
// silent.
//
// capacity is the buffer depth; anything below 1 is clamped to 1.
func DropOldest(capacity int) DeliveryConfig {
	//: eviction costs a mutex per delivery; see Listener.deliverEvicting.
	return DeliveryConfig{mode: modeDropOldest, capacity: atLeastMinimum(capacity)}
}

// DropNewest keeps the EARLIEST window: when the buffer is full the arriving
// value is discarded.
//
// It is the choice for a log or an audit trail, where the first N records
// describe how a burst began and the last N describe only that it continued.
// Every discard is counted in [Listener.Dropped].
//
// capacity is the buffer depth; anything below 1 is clamped to 1.
func DropNewest(capacity int) DeliveryConfig {
	//: the cheapest policy: one non-blocking send, no lock at all.
	return DeliveryConfig{mode: modeDropNewest, capacity: atLeastMinimum(capacity)}
}

// atLeastMinimum clamps a buffer depth to the floor.
//
// It also absorbs a negative depth, which make(chan T, n) would otherwise turn
// into a panic several frames away from the call that produced the number.
func atLeastMinimum(capacity int) int {
	//: the floor; see minCapacity for why it is a clamp and not a refusal.
	if capacity < minCapacity {
		//: hold at least one value.
		return minCapacity
	}
	//: the caller's depth, honoured as given.
	return capacity
}
