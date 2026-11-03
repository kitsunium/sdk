package queue

import "testing"

// TestTheAckedWindowForgetsTheOldestFirst pins the window's bound: it holds
// the last size identifiers, forgets the oldest when full, and does not count
// one remembered twice; a nil window holds nothing.
func TestTheAckedWindowForgetsTheOldestFirst(t *testing.T) {
	t.Parallel()
	window := newAckedWindow(2)
	for _, id := range []string{"a", "b", "b", "c"} {
		window.remember(id)
	}
	for id, want := range map[string]bool{"a": false, "b": true, "c": true} {
		if got := window.holds(id); got != want {
			t.Errorf("holds(%q) = %v, want %v", id, got, want)
		}
	}
	window.remember("d")
	if window.holds("b") || !window.holds("c") || !window.holds("d") {
		t.Errorf("after a fourth, the window holds b %v c %v d %v, want c and d", window.holds("b"), window.holds("c"), window.holds("d"))
	}
	var none *ackedWindow
	none.remember("a")
	if none.holds("a") {
		t.Error("a nil window remembered")
	}
}
