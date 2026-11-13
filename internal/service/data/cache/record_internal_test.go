package cache

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	kcache "github.com/kitsunium/sdk/internal/kernel/collections/cache"
)

// Test_record_size pins the record's budget (#1059): seven words for a
// string value, so a hit's record and its found flag come back in the
// amd64 ABI's nine result registers at every level. The deadline (three
// words) and the weight (one) are the primitive's; carrying them again had
// made the record eleven words, returned through memory three times a hit.
func Test_record_size(t *testing.T) {
	t.Parallel()
	word := reflect.TypeFor[uintptr]().Size()
	type tc struct {
		name  string
		size  uintptr
		words uintptr
	}
	tests := []tc{
		{name: "a string value", size: reflect.TypeFor[record[string]]().Size(), words: 7},
		{name: "a slice value", size: reflect.TypeFor[record[[]byte]]().Size(), words: 8},
		{name: "a pointer value", size: reflect.TypeFor[record[*int]]().Size(), words: 6},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if c.size > c.words*word {
			t.Errorf("record is %d bytes, over its %d words", c.size, c.words)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestThePrimitiveWeighsWhatValidateMeasured pins the weight's hand-over
// (#1059): the record no longer carries its weight, so the primitive's
// WeightOf reads the one validate measured for the write in progress. After
// every write path — a Set, an overwrite, a batch, a negative entry, a
// refused entry — the stripes' primitives hold exactly the weights of the
// entries the store keeps, and the caller's weigher ran once per write.
func TestThePrimitiveWeighsWhatValidateMeasured(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		write func(t *testing.T, store *memoryStore[string])
		// held is the weight the primitives must hold; calls how many times
		// the weigher ran.
		held  int64
		calls int
	}
	tests := []tc{
		{name: "a Set", held: 3, calls: 1, write: func(t *testing.T, store *memoryStore[string]) {
			mustSet(t, store, "a", "abc")
		}},
		{name: "an overwrite weighs again", held: 7, calls: 2, write: func(t *testing.T, store *memoryStore[string]) {
			mustSet(t, store, "a", "abc")
			mustSet(t, store, "a", "abcdefg")
		}},
		{name: "two keys", held: 5, calls: 2, write: func(t *testing.T, store *memoryStore[string]) {
			mustSet(t, store, "a", "abc")
			mustSet(t, store, "b", "de")
		}},
		{name: "a batch", held: 6, calls: 3, write: func(t *testing.T, store *memoryStore[string]) {
			err := store.SetMany(t.Context(), map[string]corecache.EntryValue[string]{"a": {Value: "a"}, "b": {Value: "bb"}, "c": {Value: "ccc"}})
			if err != nil {
				t.Fatalf("SetMany: %v", err)
			}
		}},
		{name: "a negative entry weighs one unit", held: 1, calls: 0, write: func(t *testing.T, store *memoryStore[string]) {
			absent := func(context.Context) (corecache.EntryValue[string], error) {
				return corecache.EntryValue[string]{}, corecache.Absent(errors.New("no such row"), time.Minute)
			}
			if _, err := store.Load(t.Context(), "a", absent); err == nil {
				t.Fatal("Load answered no error for an absent row")
			}
		}},
		{name: "a refused entry weighs nothing", held: 3, calls: 2, write: func(t *testing.T, store *memoryStore[string]) {
			mustSet(t, store, "a", "abc")
			if err := store.Set(t.Context(), "b", corecache.EntryValue[string]{Value: string(make([]byte, 1000))}); err == nil {
				t.Fatal("Set stored an entry over the byte bound")
			}
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		forEachShape(t, func(t *testing.T, stripes int, policy kcache.Policy) {
			calls := 0
			built, err := NewWeighedMemory(MemoryConfig{MaxEntries: 64, MaxBytes: 800, Stripes: stripes, Policy: policy},
				func(value string) int64 { calls++; return int64(len(value)) })
			if err != nil {
				t.Fatal(err)
			}
			store := built.(*memoryStore[string])
			c.write(t, store)
			var held int64
			for _, stripe := range store.stripes {
				held += stripe.lru.Stats().Weight
			}
			if held != c.held || calls != c.calls {
				t.Errorf("the primitives hold %d after %d weighings, want %d after %d", held, calls, c.held, c.calls)
			}
		})
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// mustSet stores value under key or fails the test.
func mustSet(t *testing.T, store *memoryStore[string], key, value string) {
	t.Helper()
	if err := store.Set(t.Context(), key, corecache.EntryValue[string]{Value: value}); err != nil {
		t.Fatalf("Set(%q): %v", key, err)
	}
}
