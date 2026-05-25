package snapshot_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// TestNew validates the constructor contract: a nil initial yields an empty
// container (Load returns nil), a non-nil initial is observable on first Load.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		initial *int
		wantNil bool
	}
	tests := []tc{
		{"nil initial is an empty container", nil, true},
		{"non-nil initial is observable", new(42), false},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the constructed container must reflect the initial argument.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		v := snapshot.New(tc.initial)
		got := v.Load()
		//: nil-initial path: Load must observe the empty (nil) state.
		if tc.wantNil {
			//: an empty container hands back nil until the first Store.
			if got != nil {
				t.Fatalf("Load() = %v, want nil", got)
			}
			return
		}
		//: seeded path: Load must hand back the exact initial pointer.
		if got != tc.initial {
			t.Fatalf("Load() = %p, want %p", got, tc.initial)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestValue_ZeroValueUsable pins the documented contract that the zero value
// is ready to use: Load returns nil before any Store, and a subsequent Store
// becomes observable.
func TestValue_ZeroValueUsable(t *testing.T) {
	t.Parallel()
	type tc struct{ name string }
	tests := []tc{
		{"zero value loads nil then accepts a store"},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; a zero-value Value must behave like an empty container.
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		var v snapshot.Value[int]
		//: an unstored zero value must read as empty.
		if got := v.Load(); got != nil {
			t.Fatalf("zero-value Load() = %v, want nil", got)
		}
		want := 7
		v.Store(&want)
		//: after Store the same pointer must be observable.
		if got := v.Load(); got != &want {
			t.Fatalf("Load() = %p, want %p", got, &want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestValue_Swap verifies Swap installs the new pointer and returns the
// previous one (nil when the container was empty).
func TestValue_Swap(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		seeded    bool
		wantOlder bool
	}
	tests := []tc{
		{"swap on empty returns nil old", false, false},
		{"swap on seeded returns prior old", true, true},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; Swap must return the displaced pointer and install the new one.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		older, newer := 1, 2
		var v snapshot.Value[int]
		//: seed the container only for the "prior value" case.
		if tc.seeded {
			v.Store(&older)
		}
		old := v.Swap(&newer)
		//: the empty case must report no displaced value.
		if !tc.wantOlder {
			//: nothing was seeded, so Swap returns nil.
			if old != nil {
				t.Fatalf("Swap() old = %v, want nil", old)
			}
		}
		//: the seeded case must return the exact prior pointer.
		if tc.wantOlder && old != &older {
			t.Fatalf("Swap() old = %p, want %p", old, &older)
		}
		//: the new pointer must be installed regardless of the prior state.
		if got := v.Load(); got != &newer {
			t.Fatalf("Load() after Swap = %p, want %p", got, &newer)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestValue_Update verifies Update transforms the current value and that
// returning the unchanged current pointer is a no-op publish (conditional
// abort).
func TestValue_Update(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		abort bool
		want  int
	}
	tests := []tc{
		{"transform publishes the new value", false, 11},
		{"returning current is a no-op abort", true, 10},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; an abort leaves the prior value installed unchanged.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		v := snapshot.New(new(10))
		v.Update(func(current *int) *int {
			//: the abort case returns the current pointer unchanged.
			if tc.abort {
				//: no-op publish — the prior value stays installed.
				return current
			}
			//: build a fresh value so readers of the old pointer are unaffected.
			next := *current + 1
			return &next
		})
		//: the observable value must match the expected post-Update state.
		if got := *v.Load(); got != tc.want {
			t.Fatalf("Load() = %d, want %d", got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestValue_ConcurrentUpdateLoad hammers the container with concurrent writers
// and readers to surface races under -race: every Update must be serialised
// (no lost increments) while Loads run lock-free.
func TestValue_ConcurrentUpdateLoad(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		writers int
		perW    int
	}
	tests := []tc{
		{"16 writers x 256 increments + readers", 16, 256},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the final count must equal the total number of Update calls.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		v := snapshot.New(new(0))
		var reads atomic.Uint64
		var wg sync.WaitGroup
		//: spawn writer goroutines that each increment via serialised Update.
		for range tc.writers {
			wg.Go(func() {
				for range tc.perW {
					v.Update(func(current *int) *int {
						//: build a fresh int so concurrent Loads never see a torn write.
						next := *current + 1
						return &next
					})
				}
			})
		}
		//: spawn reader goroutines that race lock-free Loads against the writers.
		for range tc.writers {
			wg.Go(func() {
				for range tc.perW {
					//: Load must never return nil here — the container was seeded.
					if v.Load() != nil {
						reads.Add(1)
					}
				}
			})
		}
		wg.Wait()
		//: serialised Updates must produce exactly writers*perW increments.
		want := tc.writers * tc.perW
		if got := *v.Load(); got != want {
			t.Errorf("final value = %d, want %d (lost updates)", got, want)
		}
		//: every Load observed a non-nil seeded snapshot.
		if reads.Load() != uint64(want) {
			t.Errorf("non-nil reads = %d, want %d", reads.Load(), want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
