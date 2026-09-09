package topic

import (
	"testing"
)

// TestTheZeroDeliveryConfigIsTheUnsetMode is the assumption Subscribe's refusal
// rests on. If a future reordering of the mode constants made a REAL policy the
// zero value, the refusal would silently become a default and this test is what
// says so.
func TestTheZeroDeliveryConfigIsTheUnsetMode(t *testing.T) {
	t.Parallel()
	var unset DeliveryConfig
	if unset.mode != modeUnset {
		t.Fatalf("zero DeliveryConfig.mode = %d, want modeUnset (%d)", unset.mode, modeUnset)
	}
	for _, built := range []DeliveryConfig{Block(1), DropOldest(1), DropNewest(1)} {
		if built.mode == modeUnset {
			t.Fatalf("a built DeliveryConfig carries modeUnset: %+v", built)
		}
	}
}

// TestAtLeastMinimumClampsANonPositiveCapacity checks the clamp where it is
// implemented; it also absorbs a negative depth that make(chan T, n) would
// otherwise turn into a panic several frames from the call that produced it.
func TestAtLeastMinimumClampsANonPositiveCapacity(t *testing.T) {
	t.Parallel()
	cases := map[int]int{-64: 1, -1: 1, 0: 1, 1: 1, 2: 2, 4096: 4096}
	for given, want := range cases {
		if got := atLeastMinimum(given); got != want {
			t.Fatalf("atLeastMinimum(%d) = %d, want %d", given, got, want)
		}
	}
}

// TestEachBuilderCarriesItsCapacityAndMode pins the small surface the three
// constructors have, so a copy-paste between them is caught here rather than by
// a subscriber that silently blocks when it was told to drop.
func TestEachBuilderCarriesItsCapacityAndMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		built DeliveryConfig
		want  mode
	}{
		{"Block", Block(7), modeBlock},
		{"DropOldest", DropOldest(7), modeDropOldest},
		{"DropNewest", DropNewest(7), modeDropNewest},
	}
	for _, tc := range cases {
		if tc.built.mode != tc.want {
			t.Fatalf("%s mode = %d, want %d", tc.name, tc.built.mode, tc.want)
		}
		if tc.built.capacity != 7 {
			t.Fatalf("%s capacity = %d, want 7", tc.name, tc.built.capacity)
		}
	}
}

// TestCloneStateWithCopiesRatherThanAliases is the copy-on-write contract: a
// publisher may be ranging over the current slice right now, so a writer that
// appended into it in place would mutate a list being read without a lock.
func TestCloneStateWithCopiesRatherThanAliases(t *testing.T) {
	t.Parallel()
	original := &state[int]{closed: true, subs: []*Listener[int]{{}, {}}}
	clone := cloneStateWith(original, &Listener[int]{})
	if !clone.closed {
		t.Fatal("cloneStateWith dropped the closed flag")
	}
	if len(clone.subs) != len(original.subs)+1 {
		t.Fatalf("clone holds %d subscribers, want %d", len(clone.subs), len(original.subs)+1)
	}
	clone.subs[0] = nil
	if original.subs[0] == nil {
		t.Fatal("cloneStateWith aliased the subscriber slice — a live publisher would see the write")
	}
	if len(original.subs) != 2 {
		t.Fatalf("original grew to %d entries, want 2", len(original.subs))
	}
}

// TestCloneStateWithOnTheNeverPublishedStateOpensAFreshMembership covers the nil
// branch: the first Subscribe on a zero Topic has no current snapshot to copy.
func TestCloneStateWithOnTheNeverPublishedStateOpensAFreshMembership(t *testing.T) {
	t.Parallel()
	joining := &Listener[int]{}
	fresh := cloneStateWith(nil, joining)
	if fresh == nil {
		t.Fatal("cloneStateWith(nil, …) = nil, want a fresh open membership")
	}
	if fresh.closed {
		t.Fatal("a never-published Topic reads as closed")
	}
	if len(fresh.subs) != 1 || fresh.subs[0] != joining {
		t.Fatalf("fresh.subs = %v, want exactly the joining subscriber", fresh.subs)
	}
}

// TestCloneStateWithoutRemovesOnlyTheNamedSubscriber checks the removal path
// keeps everyone else — the bug that would silently unsubscribe bystanders.
func TestCloneStateWithoutRemovesOnlyTheNamedSubscriber(t *testing.T) {
	t.Parallel()
	stay, leave, alsoStay := &Listener[int]{}, &Listener[int]{}, &Listener[int]{}
	original := &state[int]{subs: []*Listener[int]{stay, leave, alsoStay}}
	kept := cloneStateWithout(original, leave)
	if len(kept.subs) != 2 {
		t.Fatalf("kept %d subscribers, want 2", len(kept.subs))
	}
	if kept.subs[0] != stay || kept.subs[1] != alsoStay {
		t.Fatal("cloneStateWithout removed the wrong subscriber or reordered the list")
	}
	if len(original.subs) != 3 {
		t.Fatalf("original shrank to %d — the current list must stay valid for live publishers", len(original.subs))
	}
}

// TestDeliverEvictingCountsWhatItDiscards exercises the eviction path directly:
// a buffer filled behind the subscriber's back, then one delivery that has to
// make room. The counter is the assertion — a drop policy that does not count
// is a silent one.
func TestDeliverEvictingCountsWhatItDiscards(t *testing.T) {
	t.Parallel()
	var board Topic[int]
	sub := board.Subscribe(DropOldest(2))
	sub.values <- 1
	sub.values <- 2
	if !sub.deliverEvicting(3) {
		t.Fatal("deliverEvicting reported a failure with room available after eviction")
	}
	if got := sub.Dropped(); got != 1 {
		t.Fatalf("Dropped() = %d, want 1 — the evicted value must be counted", got)
	}
	if got := <-sub.values; got != 2 {
		t.Fatalf("oldest surviving value = %d, want 2 (1 was evicted)", got)
	}
	if got := <-sub.values; got != 3 {
		t.Fatalf("newest value = %d, want 3", got)
	}
}

// TestADepartedSubscriberIsSkippedBeforeAnyPolicyRuns checks the fast path at
// the top of deliver: a subscriber that has left is never counted as delivered
// and never counted as a drop, whatever policy it chose.
func TestADepartedSubscriberIsSkippedBeforeAnyPolicyRuns(t *testing.T) {
	t.Parallel()
	for _, policy := range []DeliveryConfig{Block(1), DropOldest(1), DropNewest(1)} {
		var board Topic[int]
		sub := board.Subscribe(policy)
		sub.Unsubscribe()
		if sub.deliver(t.Context(), 1) {
			t.Fatalf("mode %d: deliver to a departed subscriber reported success", policy.mode)
		}
		if got := sub.Dropped(); got != 0 {
			t.Fatalf("mode %d: Dropped() = %d, want 0 — a departure is not a drop", policy.mode, got)
		}
		if len(sub.values) != 0 {
			t.Fatalf("mode %d: a value reached a departed subscriber's buffer", policy.mode)
		}
	}
}

// TestDeliverRefusesAnUnsetModeRatherThanFallingThrough covers the branch that
// only a bug can reach: a Listener built past Subscribe's refusal must not
// silently block or silently drop, it must deliver nothing and say so.
func TestDeliverRefusesAnUnsetModeRatherThanFallingThrough(t *testing.T) {
	t.Parallel()
	sub := &Listener[int]{
		values: make(chan int, 1),
		done:   make(chan struct{}),
	}
	if sub.deliver(t.Context(), 1) {
		t.Fatal("deliver reported success under an unset mode")
	}
	if len(sub.values) != 0 {
		t.Fatal("a value landed under an unset mode")
	}
}
