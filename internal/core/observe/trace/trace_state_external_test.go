package trace_test

import (
	"errors"
	"strconv"
	"testing"

	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
)

// TestTraceStateInsertMovesTheKeyToTheFront pins §3.5: "modified keys SHOULD be
// moved to the beginning (left) of the list", and "the order of unmodified
// key/value pairs MUST be preserved".
//
// The order is the only information the list carries beyond its values — leftmost
// is the system that touched the trace most recently — which is why this type is
// an ordered slice and not a map.
func TestTraceStateInsertMovesTheKeyToTheFront(t *testing.T) {
	state := stateOf(t, "a", "1", "b", "2", "c", "3")
	updated, err := state.Insert("b", "9")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if got, want := updated.String(), "b=9,a=1,c=3"; got != want {
		t.Errorf("Insert = %q, want %q", got, want)
	}
	if got, want := state.String(), "a=1,b=2,c=3"; got != want {
		t.Errorf("the receiver was mutated: %q, want %q — StateValue is immutable", got, want)
	}
}

// TestTraceStateInsertTruncatesWholeEntriesFromTheRight pins §3.3.1.5's "the
// vendor MUST truncate whole entries", and the choice of which end: the rightmost
// entry is the oldest, and the new one has just been moved to the front.
func TestTraceStateInsertTruncatesWholeEntriesFromTheRight(t *testing.T) {
	pairs := make([]string, 0, 2*coretrace.MaxTraceStateMembers)
	for i := range coretrace.MaxTraceStateMembers {
		pairs = append(pairs, "k"+strconv.Itoa(i), "v")
	}
	state := stateOf(t, pairs...)
	updated, err := state.Insert("mine", "x")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if updated.Len() != coretrace.MaxTraceStateMembers {
		t.Errorf("Len = %d, want the list capped at %d", updated.Len(), coretrace.MaxTraceStateMembers)
	}
	if _, ok := updated.Get("mine"); !ok {
		t.Error("the inserted entry was dropped instead of the oldest one")
	}
	if _, ok := updated.Get("k" + strconv.Itoa(coretrace.MaxTraceStateMembers-1)); ok {
		t.Error("the rightmost (oldest) entry should have been the one truncated")
	}
	if _, ok := updated.Get("k0"); !ok {
		t.Error("truncation must come from the right, so the leftmost entries survive")
	}
}

// TestTraceStateInsertRefusesAnUnspellableEntry pins the refusal at the WRITE
// side: an entry the grammar cannot spell would poison every downstream hop,
// where it would be refused as an unparseable header — far from the call that
// wrote it.
func TestTraceStateInsertRefusesAnUnspellableEntry(t *testing.T) {
	cases := []struct{ key, value string }{
		{"Upper", "1"},
		{"ok", "has,comma"},
		{"ok", "has=equals"},
		{"ok", ""},
		{"", "1"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			var empty coretrace.StateValue
			if _, err := empty.Insert(tc.key, tc.value); !errors.Is(err, coretrace.InvalidTraceState) {
				t.Fatalf("want InvalidTraceState, got %v", err)
			}
		})
	}
}

// TestTraceStateDeleteKeepsTheSurroundingOrder pins the third mutation §3.5
// allows, and that it does not disturb the entries around the hole.
func TestTraceStateDeleteKeepsTheSurroundingOrder(t *testing.T) {
	state := stateOf(t, "a", "1", "b", "2", "c", "3")
	if got, want := state.Delete("b").String(), "a=1,c=3"; got != want {
		t.Errorf("Delete = %q, want %q", got, want)
	}
	if got, want := state.Delete("absent").String(), "a=1,b=2,c=3"; got != want {
		t.Errorf("deleting an absent key changed the list: %q, want %q", got, want)
	}
	if got, want := state.String(), "a=1,b=2,c=3"; got != want {
		t.Errorf("the receiver was mutated: %q, want %q", got, want)
	}
}

// TestStateBuilderKeepsTheOrderItWasGiven pins the builder's one difference
// from Insert: members arrive LEFTMOST FIRST, the order a header lists them in,
// so the list renders back in the order it was read — where Insert puts the
// newest at the front.
func TestStateBuilderKeepsTheOrderItWasGiven(t *testing.T) {
	builder := coretrace.NewStateBuilder(3)
	for _, member := range [][2]string{{"rojo", "00f067aa0ba902b7"}, {"congo", "t61rcWkgMzE"}, {"fw529a3039@dt", "foo"}} {
		if !builder.Add(member[0], member[1]) {
			t.Fatalf("Add(%q, %q) refused a member the grammar spells", member[0], member[1])
		}
	}
	if got, want := builder.State().String(), "rojo=00f067aa0ba902b7,congo=t61rcWkgMzE,fw529a3039@dt=foo"; got != want {
		t.Errorf("State = %q, want %q", got, want)
	}
}

// TestStateBuilderRefusesWhatInsertRefuses pins that the builder is not a
// second way in: every member Insert refuses, it refuses, and so does the list
// past its rules — a repeated key (§3.3.1.4) and a 33rd member. A refusal adds
// nothing, so the list built so far is unchanged by it.
func TestStateBuilderRefusesWhatInsertRefuses(t *testing.T) {
	cases := []struct{ name, key, value string }{
		{"uppercase key", "Upper", "1"},
		{"value with a comma", "ok", "has,comma"},
		{"value with an equals sign", "ok", "has=equals"},
		{"empty value", "ok", ""},
		{"empty key", "", "1"},
		{"value ending in a space", "ok", "1 "},
		{"repeated key", "a", "2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			builder := coretrace.NewStateBuilder(2)
			if !builder.Add("a", "1") {
				t.Fatal("the seed member was refused")
			}
			if builder.Add(tc.key, tc.value) {
				t.Fatalf("Add(%q, %q) accepted a member the list cannot hold", tc.key, tc.value)
			}
			if got, want := builder.State().String(), "a=1"; got != want {
				t.Errorf("a refusal changed the list: %q, want %q", got, want)
			}
		})
	}
	t.Run("a 33rd member", func(t *testing.T) {
		builder := coretrace.NewStateBuilder(coretrace.MaxTraceStateMembers + 1)
		for i := range coretrace.MaxTraceStateMembers {
			if !builder.Add("k"+strconv.Itoa(i), "v") {
				t.Fatalf("member %d of %d was refused", i+1, coretrace.MaxTraceStateMembers)
			}
		}
		if builder.Add("one-too-many", "v") {
			t.Fatal("the list took a 33rd member")
		}
		if got := builder.State().Len(); got != coretrace.MaxTraceStateMembers {
			t.Errorf("Len = %d, want %d", got, coretrace.MaxTraceStateMembers)
		}
	})
}

// TestStateBuilderLetsGoOfTheListItHandedOver pins that a value State returned
// stays immutable: the builder starts a new list after it, so a later Add
// cannot write into the one already handed out.
func TestStateBuilderLetsGoOfTheListItHandedOver(t *testing.T) {
	var builder coretrace.StateBuilder
	if !builder.Add("a", "1") {
		t.Fatal("the zero builder refused a member")
	}
	first := builder.State()
	if !builder.Add("b", "2") {
		t.Fatal("the builder refused a member after State")
	}
	if got, want := first.String(), "a=1"; got != want {
		t.Errorf("the handed-out list changed to %q, want %q", got, want)
	}
	if got, want := builder.State().String(), "b=2"; got != want {
		t.Errorf("the builder kept the old list: %q, want %q", got, want)
	}
}
