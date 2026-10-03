package snapshot_test

import (
	"fmt"
	"maps"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/concur/snapshot"
)

// TestStoreSwapAndLoad asserts the container's states through the public
// alias: the zero value and a nil initial are empty, NewValue holds what it was
// given, Store replaces it, and Swap replaces it and hands back the previous
// pointer.
func TestStoreSwapAndLoad(t *testing.T) {
	t.Parallel()
	var zero snapshot.Value[int]
	if zero.Load() != nil || snapshot.NewValue[int](nil).Load() != nil {
		t.Fatal("an empty Value loads a pointer")
	}
	one, two, three := 1, 2, 3
	v := snapshot.NewValue(&one)
	if v.Load() != &one {
		t.Errorf("NewValue(&one).Load() = %v, want the pointer it was given", v.Load())
	}
	v.Store(&two)
	if v.Load() != &two {
		t.Errorf("Load after Store = %v, want &two", v.Load())
	}
	if prev := v.Swap(&three); prev != &two || v.Load() != &three {
		t.Errorf("Swap = %v then Load = %v, want &two then &three", prev, v.Load())
	}
}

// TestUpdateLosesNoConcurrentWrite asserts that read-modify-write publishes
// racing on one Value are serialised: every goroutine's key is in the final
// map, which two writers cloning the same value would lose.
func TestUpdateLosesNoConcurrentWrite(t *testing.T) {
	t.Parallel()
	v := snapshot.NewValue(&map[int]bool{})
	const writers int = 64
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			v.Update(func(cur *map[int]bool) *map[int]bool {
				next := maps.Clone(*cur)
				next[i] = true
				return &next
			})
		})
	}
	wg.Wait()
	if got := len(*v.Load()); got != writers {
		t.Errorf("%d keys after %d concurrent Updates, want %d", got, writers, writers)
	}
}

// TestUpdateReturningTheCurrentPointerPublishesNothing asserts the abort
// idiom: an Update function that hands back what it was given leaves the
// value as it was.
func TestUpdateReturningTheCurrentPointerPublishesNothing(t *testing.T) {
	t.Parallel()
	start := "kept"
	v := snapshot.NewValue(&start)
	v.Update(func(cur *string) *string { return cur })
	if v.Load() != &start {
		t.Errorf("Load = %v after an aborted Update, want the original pointer", v.Load())
	}
}

// ExampleValue keeps a table copy-on-write: a reader loads it without a lock,
// a writer publishes a changed clone.
func ExampleValue() {
	prices := snapshot.NewValue(&map[string]int{"tea": 3})
	prices.Update(func(cur *map[string]int) *map[string]int {
		next := maps.Clone(*cur)
		next["coffee"] = 4
		return &next
	})
	table := *prices.Load()
	fmt.Println(table["tea"], table["coffee"], len(table))
	// Output: 3 4 2
}
